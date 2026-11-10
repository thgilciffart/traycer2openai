package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

func wireObject(data []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("not a JSON object: %w", err)
	}
	if m == nil {
		return nil, errors.New("not a JSON object")
	}
	return m, nil
}

func wireMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func wireSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func wireNumInt(v any) int {
	switch n := v.(type) {
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
		if f, err := n.Float64(); err == nil {
			return int(f)
		}
	case float64:
		return int(n)
	}
	return 0
}

func wireTextParts(content any) []string {
	switch c := content.(type) {
	case string:
		if c != "" {
			return []string{c}
		}
	case []any:
		var out []string
		for _, p := range c {
			pm := wireMap(p)
			if pm == nil {
				continue
			}
			switch pt, _ := pm["type"].(string); pt {
			case "text", "input_text", "output_text":
				if s, ok := pm["text"].(string); ok && s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	}
	return nil
}

func wireImageURL(v any) (string, bool) {
	switch u := v.(type) {
	case string:
		if u != "" {
			return u, true
		}
	case map[string]any:
		if s, ok := u["url"].(string); ok && s != "" {
			return s, true
		}
	}
	return "", false
}

func wireContentParts(content any, textType string, withImages bool) []any {
	parts := []any{}
	switch c := content.(type) {
	case string:
		if c != "" {
			parts = append(parts, map[string]any{"type": textType, "text": c})
		}
	case []any:
		for _, p := range c {
			pm := wireMap(p)
			if pm == nil {
				continue
			}
			switch pt, _ := pm["type"].(string); pt {
			case "text", "input_text", "output_text":
				if s, ok := pm["text"].(string); ok && s != "" {
					parts = append(parts, map[string]any{"type": textType, "text": s})
				}
			case "image_url":
				if withImages {
					if url, ok := wireImageURL(pm["image_url"]); ok {
						parts = append(parts, map[string]any{"type": "input_image", "image_url": url})
					}
				}
			}
		}
	}
	return parts
}

func wireNewID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func wireErrText(code, msg string) string {
	if code == "" {
		return "upstream error: " + msg
	}
	return "upstream error " + code + ": " + msg
}

// wireUpstreamError detects Traycer error envelopes: {"error":{...}} or a
// top-level "code" field.
func wireUpstreamError(r map[string]any) (msg, code string, ok bool) {
	if e, present := r["error"]; present && e != nil {
		switch v := e.(type) {
		case string:
			return v, "", true
		case map[string]any:
			m, _ := v["message"].(string)
			c, _ := v["code"].(string)
			if c == "" {
				c, _ = v["type"].(string)
			}
			return m, c, true
		}
	}
	if c, isStr := r["code"].(string); isStr && c != "" {
		m, _ := r["message"].(string)
		return m, c, true
	}
	return "", "", false
}

func ChatToResponses(body []byte) (out []byte, stream bool, err error) {
	req, err := wireObject(body)
	if err != nil {
		return nil, false, err
	}
	model, _ := req["model"].(string)
	if model == "" {
		return nil, false, errors.New("chat request: model is required")
	}
	messages := wireSlice(req["messages"])
	if len(messages) == 0 {
		return nil, false, errors.New("chat request: messages must be a non-empty array")
	}

	outReq := map[string]any{"model": model}
	if s, ok := req["stream"].(bool); ok {
		stream = s
	}
	outReq["stream"] = stream

	var instructions []string
	if s, ok := req["instructions"].(string); ok && s != "" {
		instructions = append(instructions, s)
	}
	input := []any{}
	for _, raw := range messages {
		msg := wireMap(raw)
		if msg == nil {
			continue
		}
		role, _ := msg["role"].(string)
		switch role {
		case "system", "developer":
			if t := strings.Join(wireTextParts(msg["content"]), ""); t != "" {
				instructions = append(instructions, t)
			}
		case "user":
			input = append(input, map[string]any{
				"type":    "message",
				"role":    "user",
				"content": wireContentParts(msg["content"], "input_text", true),
			})
		case "assistant":
			parts := wireContentParts(msg["content"], "output_text", false)
			toolCalls := wireSlice(msg["tool_calls"])
			if len(parts) == 0 && len(toolCalls) == 0 {
				continue
			}
			if len(parts) > 0 {
				input = append(input, map[string]any{
					"type":    "message",
					"role":    "assistant",
					"content": parts,
				})
			}
			for _, rawCall := range toolCalls {
				call := wireMap(rawCall)
				if call == nil {
					continue
				}
				fn := wireMap(call["function"])
				name, _ := fn["name"].(string)
				arguments, _ := fn["arguments"].(string)
				callID, _ := call["id"].(string)
				input = append(input, map[string]any{
					"type":      "function_call",
					"call_id":   callID,
					"name":      name,
					"arguments": arguments,
				})
			}
		case "tool":
			callID, _ := msg["tool_call_id"].(string)
			input = append(input, map[string]any{
				"type":    "function_call_output",
				"call_id": callID,
				"output":  strings.Join(wireTextParts(msg["content"]), ""),
			})
		}
	}
	if len(instructions) > 0 {
		outReq["instructions"] = strings.Join(instructions, "\n\n")
	}
	outReq["input"] = input

	if v, ok := req["max_completion_tokens"]; ok && v != nil {
		outReq["max_output_tokens"] = v
	} else if v, ok := req["max_tokens"]; ok && v != nil {
		outReq["max_output_tokens"] = v
	}
	if v, ok := req["temperature"]; ok && v != nil {
		outReq["temperature"] = v
	}
	if v, ok := req["top_p"]; ok && v != nil {
		outReq["top_p"] = v
	}

	if rawTools := wireSlice(req["tools"]); len(rawTools) > 0 {
		tools := []any{}
		for _, rawTool := range rawTools {
			tm := wireMap(rawTool)
			if tm == nil {
				continue
			}
			toolType, _ := tm["type"].(string)
			if toolType != "function" {
				tools = append(tools, rawTool)
				continue
			}
			fn := wireMap(tm["function"])
			if fn == nil {
				fn = tm
			}
			tool := map[string]any{"type": "function"}
			if name, ok := fn["name"].(string); ok {
				tool["name"] = name
			}
			if desc, ok := fn["description"].(string); ok {
				tool["description"] = desc
			}
			if params, ok := fn["parameters"]; ok && params != nil {
				tool["parameters"] = params
			}
			strict, hasStrict := fn["strict"]
			if !hasStrict {
				strict, hasStrict = tm["strict"]
			}
			if hasStrict {
				if b, isBool := strict.(bool); isBool {
					tool["strict"] = b
				}
			}
			tools = append(tools, tool)
		}
		outReq["tools"] = tools
	}

	if tc, ok := req["tool_choice"]; ok && tc != nil {
		switch v := tc.(type) {
		case string:
			outReq["tool_choice"] = v
		case map[string]any:
			if t, _ := v["type"].(string); t == "function" {
				name := ""
				if fn := wireMap(v["function"]); fn != nil {
					name, _ = fn["name"].(string)
				}
				if name == "" {
					name, _ = v["name"].(string)
				}
				outReq["tool_choice"] = map[string]any{"type": "function", "name": name}
			} else {
				outReq["tool_choice"] = v
			}
		}
	}

	if rf, ok := req["response_format"]; ok && rf != nil {
		var rfm map[string]any
		switch v := rf.(type) {
		case string:
			rfm = map[string]any{"type": v}
		case map[string]any:
			rfm = v
		}
		if rfm != nil {
			switch ft, _ := rfm["type"].(string); ft {
			case "json_object":
				outReq["text"] = map[string]any{"format": map[string]any{"type": "json_object"}}
			case "json_schema":
				js := wireMap(rfm["json_schema"])
				if js == nil {
					js = rfm
				}
				format := map[string]any{"type": "json_schema"}
				if name, ok := js["name"].(string); ok {
					format["name"] = name
				}
				if schema, ok := js["schema"]; ok && schema != nil {
					format["schema"] = schema
				}
				if strict, ok := js["strict"]; ok {
					if b, isBool := strict.(bool); isBool {
						format["strict"] = b
					}
				}
				outReq["text"] = map[string]any{"format": format}
			}
		}
	}

	if re, ok := req["reasoning_effort"]; ok && re != nil {
		switch v := re.(type) {
		case string:
			if v != "" {
				outReq["reasoning"] = map[string]any{"effort": v}
			}
		case map[string]any:
			if effort, ok := v["effort"].(string); ok && effort != "" {
				outReq["reasoning"] = map[string]any{"effort": effort}
			}
		}
	}
	// Pass a Responses-style reasoning object through when the client already
	// speaks that shape.
	if _, ok := outReq["reasoning"]; !ok {
		if r := wireMap(req["reasoning"]); r != nil {
			outReq["reasoning"] = r
		}
	}

	out, err = json.Marshal(outReq)
	if err != nil {
		return nil, stream, err
	}
	return out, stream, nil
}

func ResponsesToChat(resp []byte, model string) ([]byte, error) {
	r, err := wireObject(resp)
	if err != nil {
		return nil, err
	}
	if msg, code, ok := wireUpstreamError(r); ok {
		return nil, errors.New(wireErrText(code, msg))
	}

	var content, reasoning strings.Builder
	toolCalls := []any{}
	for _, rawItem := range wireSlice(r["output"]) {
		item := wireMap(rawItem)
		if item == nil {
			continue
		}
		switch itemType, _ := item["type"].(string); itemType {
		case "message":
			for _, rawPart := range wireSlice(item["content"]) {
				part := wireMap(rawPart)
				if part == nil {
					continue
				}
				if pt, _ := part["type"].(string); pt == "output_text" {
					if s, ok := part["text"].(string); ok {
						content.WriteString(s)
					}
				}
			}
		case "reasoning":
			appendReasoning(&reasoning, item)
		case "function_call":
			callID, _ := item["call_id"].(string)
			name, _ := item["name"].(string)
			arguments, _ := item["arguments"].(string)
			toolCalls = append(toolCalls, map[string]any{
				"id":   callID,
				"type": "function",
				"function": map[string]any{
					"name":      name,
					"arguments": arguments,
				},
			})
		}
	}

	finishReason := "stop"
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
	} else if status, _ := r["status"].(string); status == "incomplete" {
		finishReason = "length"
	}

	message := map[string]any{"role": "assistant"}
	// OpenAI sends content:null when the turn is purely tool calls; strict clients
	// branch on that instead of an empty string.
	if content.Len() > 0 || len(toolCalls) == 0 {
		message["content"] = content.String()
	} else {
		message["content"] = nil
	}
	if reasoning.Len() > 0 {
		message["reasoning_content"] = reasoning.String()
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}

	completion := map[string]any{
		"id":      "chatcmpl-" + wireNewID(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": finishReason,
		}},
	}
	if usage := wireMap(r["usage"]); usage != nil {
		in := wireNumInt(usage["input_tokens"])
		outT := wireNumInt(usage["output_tokens"])
		u := map[string]any{
			"prompt_tokens":     in,
			"completion_tokens": outT,
			"total_tokens":      in + outT,
		}
		if det := wireMap(usage["output_tokens_details"]); det != nil {
			if n := wireNumInt(det["reasoning_tokens"]); n > 0 {
				u["completion_tokens_details"] = map[string]any{"reasoning_tokens": n}
			}
		}
		completion["usage"] = u
	}
	return json.Marshal(completion)
}

// appendReasoning flattens a Responses reasoning item into one string: the
// visible summary when present, otherwise the raw reasoning content.
func appendReasoning(b *strings.Builder, item map[string]any) {
	var texts []string
	for _, rawPart := range wireSlice(item["summary"]) {
		if part := wireMap(rawPart); part != nil {
			if s, ok := part["text"].(string); ok && s != "" {
				texts = append(texts, s)
			}
		}
	}
	if len(texts) == 0 {
		for _, rawPart := range wireSlice(item["content"]) {
			if part := wireMap(rawPart); part != nil {
				if s, ok := part["text"].(string); ok && s != "" {
					texts = append(texts, s)
				}
			}
		}
	}
	if len(texts) == 0 {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	b.WriteString(strings.Join(texts, "\n\n"))
}

func StreamResponsesToChat(w io.Writer, r io.Reader, model string) (inTokens, outTokens int, err error) {
	type flushWriter interface{ Flush() }

	chunkID := "chatcmpl-" + wireNewID()
	created := time.Now().Unix()
	itemIndex := map[string]int{}
	toolCalls := 0
	sawToolCall := false
	reasoningTokens := 0

	writeChunk := func(delta map[string]any, finish any, usage map[string]any) error {
		chunk := map[string]any{
			"id":      chunkID,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
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
		if f, ok := w.(flushWriter); ok {
			f.Flush()
		}
		return nil
	}

	if err := writeChunk(map[string]any{"role": "assistant", "content": ""}, nil, nil); err != nil {
		return 0, 0, err
	}

	errDone := errors.New("stream done")
	errTerminal := errors.New("stream terminal")

	var dataLines []string
	var eventName string
	dispatch := func() error {
		defer func() { dataLines = nil; eventName = "" }()
		if len(dataLines) == 0 {
			return nil
		}
		payload := strings.Join(dataLines, "\n")
		if payload == "[DONE]" {
			return errDone
		}
		ev, err := wireObject([]byte(payload))
		if err != nil {
			return nil
		}
		if msg, code, ok := wireUpstreamError(ev); ok {
			return errors.New(wireErrText(code, msg))
		}
		t, _ := ev["type"].(string)
		if t == "" {
			t = eventName
		}
		switch t {
		case "response.output_text.delta":
			delta, _ := ev["delta"].(string)
			if delta == "" {
				return nil
			}
			return writeChunk(map[string]any{"content": delta}, nil, nil)
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			delta, _ := ev["delta"].(string)
			if delta == "" {
				return nil
			}
			return writeChunk(map[string]any{"reasoning_content": delta}, nil, nil)
		case "response.output_item.added":
			item := wireMap(ev["item"])
			if item == nil {
				return nil
			}
			if itype, _ := item["type"].(string); itype == "function_call" {
				idx := toolCalls
				toolCalls++
				sawToolCall = true
				if id, ok := item["id"].(string); ok && id != "" {
					itemIndex[id] = idx
				}
				callID, _ := item["call_id"].(string)
				name, _ := item["name"].(string)
				return writeChunk(map[string]any{"tool_calls": []any{map[string]any{
					"index":    idx,
					"id":       callID,
					"type":     "function",
					"function": map[string]any{"name": name, "arguments": ""},
				}}}, nil, nil)
			}
			return nil
		case "response.function_call_arguments.delta":
			itemID, _ := ev["item_id"].(string)
			idx, ok := itemIndex[itemID]
			if !ok {
				if toolCalls > 0 {
					idx = toolCalls - 1
				} else {
					idx = 0
				}
			}
			delta, _ := ev["delta"].(string)
			if delta == "" {
				return nil
			}
			return writeChunk(map[string]any{"tool_calls": []any{map[string]any{
				"index":    idx,
				"function": map[string]any{"arguments": delta},
			}}}, nil, nil)
		case "response.completed", "response.incomplete":
			if resp := wireMap(ev["response"]); resp != nil {
				if u := wireMap(resp["usage"]); u != nil {
					inTokens = wireNumInt(u["input_tokens"])
					outTokens = wireNumInt(u["output_tokens"])
					if det := wireMap(u["output_tokens_details"]); det != nil {
						reasoningTokens = wireNumInt(det["reasoning_tokens"])
					}
				}
			}
			return errTerminal
		case "error":
			msg, _ := ev["message"].(string)
			if msg == "" {
				if e := wireMap(ev["error"]); e != nil {
					msg, _ = e["message"].(string)
				}
			}
			if msg == "" {
				msg = "unknown"
			}
			return errors.New(wireErrText("", msg))
		default:
			return nil
		}
	}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
loop:
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			derr := dispatch()
			if derr == nil {
				continue
			}
			if errors.Is(derr, errDone) || errors.Is(derr, errTerminal) {
				break loop
			}
			return inTokens, outTokens, derr
		}
		switch {
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "data:"):
			d := strings.TrimPrefix(line, "data:")
			d = strings.TrimPrefix(d, " ")
			dataLines = append(dataLines, d)
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimPrefix(strings.TrimPrefix(line, "event:"), " ")
		}
	}
	if err := sc.Err(); err != nil {
		return inTokens, outTokens, err
	}
	if len(dataLines) > 0 {
		derr := dispatch()
		if derr != nil && !errors.Is(derr, errDone) && !errors.Is(derr, errTerminal) {
			return inTokens, outTokens, derr
		}
	}

	finish := "stop"
	if sawToolCall {
		finish = "tool_calls"
	}
	var usage map[string]any
	if inTokens > 0 || outTokens > 0 {
		usage = map[string]any{
			"prompt_tokens":     inTokens,
			"completion_tokens": outTokens,
			"total_tokens":      inTokens + outTokens,
		}
		if reasoningTokens > 0 {
			usage["completion_tokens_details"] = map[string]any{"reasoning_tokens": reasoningTokens}
		}
	}
	if err := writeChunk(map[string]any{}, finish, usage); err != nil {
		return inTokens, outTokens, err
	}
	if _, err := io.WriteString(w, "data: [DONE]\n\n"); err != nil {
		return inTokens, outTokens, err
	}
	if f, ok := w.(flushWriter); ok {
		f.Flush()
	}
	return inTokens, outTokens, nil
}
