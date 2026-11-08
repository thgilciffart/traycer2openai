package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Traycer serves Gemini models on a native Gemini route rather than through the
// OpenAI proxy:
//
//	POST /inference/gemini/models/<model>:streamGenerateContent?alt=sse
//
// That route is billed differently from /inference/openai/responses: an account
// can be out of Traycer inference credits and still answer here, which is why the
// desktop app works while the OpenAI-shaped path returns 402. So Gemini models are
// translated to Gemini's own request shape instead of the Responses shape.

// skipSignature is the sentinel Traycer's own host sends when replaying a
// function call without its original thought signature. Gemini accepts it.
const skipSignature = "skip_thought_signature_validator"

// geminiSchemaKeys are JSON Schema keywords Gemini rejects in function declarations.
// geminiSchemaAllowed is an allowlist of the JSON Schema keywords Gemini's Schema
// object accepts. A denylist was the wrong shape: every client invents another
// keyword (const, exclusiveMinimum, if/then, ...) and Gemini 400s on all of them.
// Anything not listed here is dropped, so an unknown keyword can never break a
// request. Rewrites that need semantics (const, allOf, type arrays) are handled
// before this check.
var geminiSchemaAllowed = map[string]bool{
	"type": true, "format": true, "title": true, "description": true,
	"nullable": true, "enum": true, "items": true, "properties": true,
	"required": true, "minItems": true, "maxItems": true,
	"minProperties": true, "maxProperties": true, "minimum": true, "maximum": true,
	"minLength": true, "maxLength": true, "pattern": true, "example": true,
	"anyOf": true, "propertyOrdering": true,
}

// scrubGeminiSchema rewrites a JSON Schema into the OpenAPI subset Gemini accepts:
// const becomes enum, a union of constants collapses into one enum, nullable type
// arrays become type plus nullable, and keywords Gemini refuses are dropped.
func scrubGeminiSchema(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			switch k {
			case "const":
				out["enum"] = []any{val}
				continue
			case "anyOf", "oneOf":
				for mk, mv := range geminiUnion(t, k) {
					out[mk] = mv
				}
				continue
			case "allOf":
				// Intersection: fold every branch into one flat schema.
				for _, branch := range wireSlice(val) {
					for mk, mv := range wireMap(scrubGeminiSchema(branch)) {
						out[mk] = mv
					}
				}
				continue
			case "exclusiveMinimum":
				// Gemini has no exclusive bounds; a plain bound is the closest
				// it accepts.
				if _, has := t["minimum"]; !has {
					out["minimum"] = val
				}
				continue
			case "exclusiveMaximum":
				if _, has := t["maximum"]; !has {
					out["maximum"] = val
				}
				continue
			case "properties":
				// Property names are user data, not keywords: recurse into the
				// values and keep every name.
				props := map[string]any{}
				for pk, pv := range wireMap(val) {
					props[pk] = scrubGeminiSchema(pv)
				}
				out["properties"] = props
				continue
			case "items":
				out["items"] = scrubGeminiSchema(val)
				continue
			}
			if !geminiSchemaAllowed[k] {
				continue
			}
			out[k] = scrubGeminiSchema(val)
		}
		// Gemini validates that every required name exists in properties. Clients
		// (and our own allOf folding) produce required lists without a matching
		// property, which is a 400 upstream.
		if req, ok := out["required"].([]any); ok {
			props, _ := out["properties"].(map[string]any)
			kept := make([]any, 0, len(req))
			for _, name := range req {
				if s, ok := name.(string); ok {
					if _, exists := props[s]; exists {
						kept = append(kept, name)
					}
				}
			}
			if len(kept) == 0 {
				delete(out, "required")
			} else {
				out["required"] = kept
			}
		}
		if arr, ok := out["type"].([]any); ok {
			var rest []any
			nullable := false
			for _, item := range arr {
				if s, _ := item.(string); s == "null" {
					nullable = true
					continue
				}
				rest = append(rest, item)
			}
			if len(rest) == 1 {
				out["type"] = rest[0]
				if nullable {
					out["nullable"] = true
				}
			}
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = scrubGeminiSchema(val)
		}
		return out
	}
	return v
}

// geminiUnion turns a union of constant branches into a single enum. For richer
// unions it keeps the first branch, which is still a schema Gemini accepts.
func geminiUnion(parent map[string]any, key string) map[string]any {
	branches := wireSlice(parent[key])
	enums := make([]any, 0, len(branches))
	allConst := len(branches) > 0
	for _, raw := range branches {
		branch := wireMap(scrubGeminiSchema(raw))
		if branch == nil {
			allConst = false
			break
		}
		for k := range branch {
			if k != "enum" && k != "description" && k != "title" {
				allConst = false
			}
		}
		vals := wireSlice(branch["enum"])
		if len(vals) == 0 {
			allConst = false
			break
		}
		enums = append(enums, vals...)
	}
	if allConst {
		out := map[string]any{"enum": enums}
		if d, ok := parent["description"].(string); ok && d != "" {
			out["description"] = d
		}
		return out
	}
	first := wireMap(scrubGeminiSchema(branches[0]))
	if first == nil {
		return map[string]any{}
	}
	if d, ok := parent["description"].(string); ok && d != "" {
		if _, has := first["description"]; !has {
			first["description"] = d
		}
	}
	return first
}

func geminiTextParts(content any) []any {
	parts := []any{}
	switch c := content.(type) {
	case string:
		if c != "" {
			parts = append(parts, map[string]any{"text": c})
		}
	case []any:
		for _, raw := range c {
			pm := wireMap(raw)
			if pm == nil {
				continue
			}
			switch pt, _ := pm["type"].(string); pt {
			case "text", "input_text", "output_text":
				if s, ok := pm["text"].(string); ok && s != "" {
					parts = append(parts, map[string]any{"text": s})
				}
			case "image_url":
				url, ok := wireImageURL(pm["image_url"])
				if !ok {
					continue
				}
				if mime, data, found := strings.Cut(strings.TrimPrefix(url, "data:"), ";base64,"); found {
					parts = append(parts, map[string]any{
						"inlineData": map[string]any{"mimeType": mime, "data": data},
					})
				} else if strings.HasPrefix(url, "http") {
					parts = append(parts, map[string]any{
						"fileData": map[string]any{"fileUri": url},
					})
				}
			}
		}
	}
	return parts
}

// ChatToGemini converts an OpenAI chat-completions request into a native Gemini
// generateContent body.
func ChatToGemini(body []byte, sigFor func(string) string) (out []byte, stream bool, model string, err error) {
	req, err := wireObject(body)
	if err != nil {
		return nil, false, "", err
	}
	model, _ = req["model"].(string)
	if model == "" {
		return nil, false, "", errors.New("chat request: model is required")
	}
	messages := wireSlice(req["messages"])
	if len(messages) == 0 {
		return nil, false, "", errors.New("chat request: messages must be a non-empty array")
	}
	if s, ok := req["stream"].(bool); ok {
		stream = s
	}

	gReq := map[string]any{}
	contents := []any{}
	var system []string
	// Gemini's functionResponse needs the function name, which OpenAI's tool
	// message omits; recover it from the earlier assistant tool_calls.
	callNames := map[string]string{}

	for _, raw := range messages {
		msg := wireMap(raw)
		if msg == nil {
			continue
		}
		role, _ := msg["role"].(string)
		switch role {
		case "system", "developer":
			if t := strings.Join(wireTextParts(msg["content"]), " "); t != "" {
				system = append(system, t)
			}
		case "user":
			if parts := geminiTextParts(msg["content"]); len(parts) > 0 {
				contents = append(contents, map[string]any{"role": "user", "parts": parts})
			}
		case "assistant":
			parts := geminiTextParts(msg["content"])
			rawCalls := wireSlice(msg["tool_calls"])
			// Gemini validates thought signatures per response and rejects a
			// replay that MIXES a real signature on some functionCall parts
			// with the skip sentinel on others ("Corrupted thought signature",
			// observed live with parallel tool calls, where upstream attaches
			// the signature to only one part of the turn). So use real
			// signatures only when every call in this message has one cached;
			// otherwise use the sentinel for all of them (all-sentinel replays
			// are accepted, verified against the live upstream).
			realSigs := map[string]string{}
			nCalls := 0
			for _, rawCall := range rawCalls {
				call := wireMap(rawCall)
				if call == nil {
					continue
				}
				nCalls++
				id, _ := call["id"].(string)
				if id != "" && sigFor != nil {
					if s := sigFor(id); s != "" {
						realSigs[id] = s
					}
				}
			}
			allReal := nCalls > 0 && len(realSigs) == nCalls
			for _, rawCall := range rawCalls {
				call := wireMap(rawCall)
				fn := wireMap(call["function"])
				if fn == nil {
					continue
				}
				name, _ := fn["name"].(string)
				id, _ := call["id"].(string)
				if id != "" {
					callNames[id] = name
				}
				args := any(map[string]any{})
				if raw, ok := fn["arguments"].(string); ok && raw != "" {
					if parsed, perr := wireObject([]byte(raw)); perr == nil {
						args = parsed
					} else {
						args = map[string]any{"input": raw}
					}
				}
				// Gemini rejects a function-call part that carries no
				// thoughtSignature, even when replaying a call the model made
				// itself. Prefer the real signature we cached from the response
				// that produced the call; fall back to Traycer's own sentinel,
				// which the host also uses, when we no longer have it.
				sig := ""
				if allReal && sigFor != nil {
					sig = sigFor(id)
				}
				if sig == "" {
					sig = skipSignature
				}
				parts = append(parts, map[string]any{
					"functionCall":     map[string]any{"id": id, "name": name, "args": args},
					"thoughtSignature": sig,
				})
			}
			if len(parts) > 0 {
				contents = append(contents, map[string]any{"role": "model", "parts": parts})
			}
		case "tool":
			callID, _ := msg["tool_call_id"].(string)
			name := callNames[callID]
			if name == "" {
				name = "tool"
			}
			text := strings.Join(wireTextParts(msg["content"]), "")
			contents = append(contents, map[string]any{
				"role": "user",
				"parts": []any{map[string]any{
					"functionResponse": map[string]any{
						"id":       callID,
						"name":     name,
						"response": map[string]any{"name": name, "content": text},
					},
				}},
			})
		}
	}
	gReq["contents"] = contents
	if len(system) > 0 {
		gReq["systemInstruction"] = map[string]any{
			"parts": []any{map[string]any{"text": strings.Join(system, "\n\n")}},
		}
	}

	gen := map[string]any{}
	if v, ok := req["max_completion_tokens"]; ok && v != nil {
		gen["maxOutputTokens"] = v
	} else if v, ok := req["max_tokens"]; ok && v != nil {
		gen["maxOutputTokens"] = v
	}
	if v, ok := req["temperature"]; ok && v != nil {
		gen["temperature"] = v
	}
	if v, ok := req["top_p"]; ok && v != nil {
		gen["topP"] = v
	}
	if v, ok := req["stop"]; ok && v != nil {
		switch s := v.(type) {
		case string:
			gen["stopSequences"] = []any{s}
		case []any:
			gen["stopSequences"] = s
		}
	}
	// Thinking is billed from the same output budget, so a client asking for a
	// small max_tokens otherwise gets an empty reply: the whole allowance goes to
	// thoughts. Disable thinking for small budgets unless the client asked for it.
	if effort, ok := req["reasoning_effort"].(string); ok && effort != "" {
		gen["thinkingConfig"] = map[string]any{"thinkingLevel": geminiEffort(effort), "includeThoughts": false}
	} else if n, ok := numAsInt(gen["maxOutputTokens"]); ok && n > 0 && n < 512 {
		gen["thinkingConfig"] = map[string]any{"thinkingBudget": 0, "includeThoughts": false}
	}
	// JSON mode is a generationConfig concern in Gemini, not a top-level field.
	if rf := wireMap(req["response_format"]); rf != nil {
		switch t, _ := rf["type"].(string); t {
		case "json_object":
			gen["responseMimeType"] = "application/json"
		case "json_schema":
			js := wireMap(rf["json_schema"])
			if js == nil {
				js = rf
			}
			gen["responseMimeType"] = "application/json"
			if schema, ok := js["schema"]; ok && schema != nil {
				gen["responseSchema"] = scrubGeminiSchema(schema)
			}
		}
	}
	if len(gen) > 0 {
		gReq["generationConfig"] = gen
	}

	if rawTools := wireSlice(req["tools"]); len(rawTools) > 0 {
		decls := []any{}
		for _, rawTool := range rawTools {
			tm := wireMap(rawTool)
			fn := wireMap(tm["function"])
			if fn == nil {
				fn = tm
			}
			if t, _ := tm["type"].(string); t != "" && t != "function" {
				continue
			}
			decl := map[string]any{}
			if n, ok := fn["name"].(string); ok {
				decl["name"] = n
			}
			if d, ok := fn["description"].(string); ok {
				decl["description"] = d
			}
			if p, ok := fn["parameters"]; ok && p != nil {
				decl["parameters"] = scrubGeminiSchema(p)
			}
			decls = append(decls, decl)
		}
		if len(decls) > 0 {
			gReq["tools"] = []any{map[string]any{"functionDeclarations": decls}}
		}
	}

	mode := ""
	switch tc := req["tool_choice"].(type) {
	case string:
		switch tc {
		case "auto":
			mode = "AUTO"
		case "none":
			mode = "NONE"
		case "required":
			mode = "ANY"
		}
	case map[string]any:
		mode = "ANY"
	}
	if mode != "" {
		gReq["toolConfig"] = map[string]any{"functionCallingConfig": map[string]any{"mode": mode}}
	}

	out, err = json.Marshal(gReq)
	return out, stream, model, err
}

// ---------------------------------------------------------------- responses

type geminiCall struct {
	Name string
	Args any
	ID   string
	Sig  string
}

type geminiDelta struct {
	Text   string
	Parts  []geminiCall
	Finish string
	In     int
	Out    int
}

var geminiFinish = map[string]string{
	"MAX_TOKENS":         "length",
	"SAFETY":             "content_filter",
	"RECITATION":         "content_filter",
	"BLOCKLIST":          "content_filter",
	"PROHIBITED_CONTENT": "content_filter",
	"OTHER":              "stop",
	"STOP":               "stop",
}

// parseGeminiEvent reads one SSE payload from the native route.
func parseGeminiEvent(payload []byte) (geminiDelta, error) {
	var d geminiDelta
	var env map[string]any
	if err := json.Unmarshal(payload, &env); err != nil {
		return d, nil // tolerate non-JSON keepalives
	}
	if msg, code, ok := wireUpstreamError(env); ok {
		return d, errors.New(wireErrText(code, msg))
	}
	cands := wireSlice(env["candidates"])
	if len(cands) > 0 {
		c := wireMap(cands[0])
		if c == nil {
			return d, nil
		}
		if content := wireMap(c["content"]); content != nil {
			for _, rawPart := range wireSlice(content["parts"]) {
				part := wireMap(rawPart)
				if part == nil {
					continue
				}
				// Skip thinking parts: they are not assistant-visible content.
				if thought, _ := part["thought"].(bool); thought {
					continue
				}
				if s, ok := part["text"].(string); ok && s != "" {
					d.Text += s
				}
				if fc := wireMap(part["functionCall"]); fc != nil {
					name, _ := fc["name"].(string)
					id, _ := fc["id"].(string)
					sig, _ := part["thoughtSignature"].(string)
					d.Parts = append(d.Parts, geminiCall{Name: name, Args: fc["args"], ID: id, Sig: sig})
				}
			}
		}
		if fr, ok := c["finishReason"].(string); ok && fr != "" {
			d.Finish = geminiFinish[fr]
			if d.Finish == "" {
				d.Finish = "stop"
			}
		}
	}
	if u := wireMap(env["usageMetadata"]); u != nil {
		d.In = wireNumInt(u["promptTokenCount"])
		d.Out = wireNumInt(u["candidatesTokenCount"])
	}
	return d, nil
}

// StreamGeminiToChat rewrites the native Gemini SSE stream as chat completion chunks.
func StreamGeminiToChat(w io.Writer, r io.Reader, model string, sigs *sigCache) (inTokens, outTokens int, err error) {
	chunkID := "chatcmpl-" + wireNewID()
	created := time.Now().Unix()
	toolIndex := 0
	sawTool := false
	finish := "stop"

	writeChunk := func(delta map[string]any, fr any, usage map[string]any) error {
		chunk := map[string]any{
			"id": chunkID, "object": "chat.completion.chunk", "created": created, "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": fr}},
		}
		if usage != nil {
			chunk["usage"] = usage
		}
		b, err := json.Marshal(chunk)
		if err != nil {
			return err
		}
		if _, err := w.Write([]byte("data: " + string(b) + "\n\n")); err != nil {
			return err
		}
		if f, ok := w.(interface{ Flush() }); ok {
			f.Flush()
		}
		return nil
	}

	if err := writeChunk(map[string]any{"role": "assistant", "content": ""}, nil, nil); err != nil {
		return 0, 0, err
	}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := bytes.TrimSpace([]byte(strings.TrimPrefix(line, "data:")))
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		d, derr := parseGeminiEvent(payload)
		if derr != nil {
			return inTokens, outTokens, derr
		}
		if d.In > 0 {
			inTokens = d.In
		}
		if d.Out > 0 {
			outTokens = d.Out
		}
		if d.Finish != "" {
			finish = d.Finish
		}
		if d.Text != "" {
			if err := writeChunk(map[string]any{"content": d.Text}, nil, nil); err != nil {
				return inTokens, outTokens, err
			}
		}
		for _, call := range d.Parts {
			sawTool = true
			args, _ := json.Marshal(call.Args)
			if call.Args == nil {
				args = []byte("{}")
			}
			id := call.ID
			if id == "" {
				id = "call_" + wireNewID()
			}
			if sigs != nil {
				sigs.put(id, call.Sig)
			}
			if err := writeChunk(map[string]any{"tool_calls": []any{map[string]any{
				"index": toolIndex, "id": id, "type": "function",
				"function": map[string]any{"name": call.Name, "arguments": string(args)},
			}}}, nil, nil); err != nil {
				return inTokens, outTokens, err
			}
			toolIndex++
		}
	}
	if err := sc.Err(); err != nil {
		return inTokens, outTokens, err
	}

	if sawTool {
		finish = "tool_calls"
	}
	var usage map[string]any
	if inTokens > 0 || outTokens > 0 {
		usage = map[string]any{
			"prompt_tokens": inTokens, "completion_tokens": outTokens,
			"total_tokens": inTokens + outTokens,
		}
	}
	if err := writeChunk(map[string]any{}, finish, usage); err != nil {
		return inTokens, outTokens, err
	}
	if _, err := io.WriteString(w, "data: [DONE]\n\n"); err != nil {
		return inTokens, outTokens, err
	}
	if f, ok := w.(interface{ Flush() }); ok {
		f.Flush()
	}
	return inTokens, outTokens, nil
}

// GeminiToChat aggregates the native SSE stream into one chat.completion, for
// clients that asked for stream=false.
func GeminiToChat(r io.Reader, model string, sigs *sigCache) ([]byte, int, int, error) {
	var content strings.Builder
	toolCalls := []any{}
	finish := "stop"
	inTok, outTok := 0, 0

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := bytes.TrimSpace([]byte(strings.TrimPrefix(line, "data:")))
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		d, err := parseGeminiEvent(payload)
		if err != nil {
			return nil, 0, 0, err
		}
		if d.In > 0 {
			inTok = d.In
		}
		if d.Out > 0 {
			outTok = d.Out
		}
		if d.Finish != "" {
			finish = d.Finish
		}
		content.WriteString(d.Text)
		for _, call := range d.Parts {
			args, _ := json.Marshal(call.Args)
			if call.Args == nil {
				args = []byte("{}")
			}
			id := call.ID
			if id == "" {
				id = "call_" + wireNewID()
			}
			if sigs != nil {
				sigs.put(id, call.Sig)
			}
			toolCalls = append(toolCalls, map[string]any{
				"id": id, "type": "function",
				"function": map[string]any{"name": call.Name, "arguments": string(args)},
			})
		}
	}
	if err := sc.Err(); err != nil {
		return nil, 0, 0, err
	}
	if len(toolCalls) > 0 {
		finish = "tool_calls"
	}

	message := map[string]any{"role": "assistant"}
	if content.Len() > 0 || len(toolCalls) == 0 {
		message["content"] = content.String()
	} else {
		message["content"] = nil
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}
	out := map[string]any{
		"id": "chatcmpl-" + wireNewID(), "object": "chat.completion",
		"created": time.Now().Unix(), "model": model,
		"choices": []any{map[string]any{
			"index": 0, "message": message, "finish_reason": finish,
		}},
	}
	if inTok > 0 || outTok > 0 {
		out["usage"] = map[string]any{
			"prompt_tokens": inTok, "completion_tokens": outTok, "total_tokens": inTok + outTok,
		}
	}
	b, err := json.Marshal(out)
	return b, inTok, outTok, err
}

// geminiEffort clamps OpenAI reasoning effort names onto the levels Gemini accepts.
func geminiEffort(effort string) string {
	switch strings.ToLower(effort) {
	case "low", "minimal":
		return "low"
	case "medium":
		return "medium"
	case "high", "xhigh", "max":
		return "high"
	}
	return "medium"
}

func numAsInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i), true
		}
	}
	return 0, false
}

func geminiURL(server, model string) string {
	return fmt.Sprintf("%s/inference/gemini/models/%s:streamGenerateContent?alt=sse",
		strings.TrimRight(server, "/"), model)
}

// useGemini decides whether a model should go to Traycer's native Gemini route.
// It prefers the live catalog's provider field and falls back to the model name,
// so routing still works when the catalog is unavailable.
func (s *Server) useGemini(ctx context.Context, model string) bool {
	if strings.HasPrefix(strings.ToLower(model), "gemini") {
		return true
	}
	for _, m := range s.modelsSnapshot() {
		if m.ID == model {
			return strings.EqualFold(m.Provider, "gemini")
		}
	}
	models, err := s.catalog(ctx, false)
	if err != nil {
		return false
	}
	for _, m := range models {
		if m.ID == model {
			return strings.EqualFold(m.Provider, "gemini")
		}
	}
	return false
}

// handleGeminiChat serves /v1/chat/completions for models on the native route.
func (s *Server) handleGeminiChat(w http.ResponseWriter, r *http.Request, start time.Time, acct *Account, model string, body []byte, stream bool) {
	reqBody, _, _, err := ChatToGemini(body, s.sigs.get)
	if err != nil {
		oaiError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	callCtx := r.Context()
	var cancel context.CancelFunc
	if !stream {
		callCtx, cancel = context.WithTimeout(r.Context(), s.cfg.UpstreamTimeout)
		defer cancel()
	}
	// Traycer's inference route occasionally answers 5xx under load (an 84s
	// "Internal Server Error" was observed once). One retry before any bytes are
	// written turns that into a normal turn instead of a failed request.
	resp, err := s.up.GeminiStream(callCtx, acct, model, reqBody)
	if err == nil && resp.StatusCode >= 500 {
		resp.Body.Close()
		resp, err = s.up.GeminiStream(callCtx, acct, model, reqBody)
	}
	if err != nil {
		s.record(acct, model, stream, http.StatusBadGateway, start, 0, 0, err.Error())
		oaiError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		msg, code := readUpstreamError(resp.Body)
		s.record(acct, model, stream, resp.StatusCode, start, 0, 0, msg)
		oaiError(w, resp.StatusCode, code, msg)
		return
	}

	if stream {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		in, out, serr := StreamGeminiToChat(w, resp.Body, model, s.sigs)
		s.record(acct, model, true, http.StatusOK, start, in, out, errText(serr))
		return
	}
	chat, in, out, err := GeminiToChat(resp.Body, model, s.sigs)
	if err != nil {
		s.record(acct, model, false, http.StatusBadGateway, start, 0, 0, err.Error())
		oaiError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(chat)
	s.record(acct, model, false, http.StatusOK, start, in, out, "")
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ---------------------------------------------------------------- signatures

// Gemini validates thought signatures cryptographically, so a replayed
// functionCall must carry the signature that came with it. Clients speak OpenAI
// chat format, which has nowhere to store one, so the proxy keeps a short-lived
// side table keyed by the tool-call id it handed out and looks it up when the
// same call comes back in the history.
type sigEntry struct {
	sig     string
	expires time.Time
}

type sigCache struct {
	mu sync.Mutex
	m  map[string]sigEntry
}

func newSigCache() *sigCache { return &sigCache{m: map[string]sigEntry{}} }

func (c *sigCache) put(id, sig string) {
	if id == "" || sig == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) > 2000 {
		now := time.Now()
		for k, v := range c.m {
			if now.After(v.expires) {
				delete(c.m, k)
			}
		}
		if len(c.m) > 2000 {
			c.m = map[string]sigEntry{}
		}
	}
	c.m[id] = sigEntry{sig: sig, expires: time.Now().Add(time.Hour)}
}

func (c *sigCache) get(id string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[id]
	if !ok || time.Now().After(e.expires) {
		return ""
	}
	return e.sig
}
