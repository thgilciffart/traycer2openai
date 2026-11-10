package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// sigPart extracts the thoughtSignature of the i-th functionCall part in the
// translated Gemini body.
func sigPart(t *testing.T, body []byte, i int) (sig, id string) {
	t.Helper()
	var req struct {
		Contents []struct {
			Parts []map[string]any `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	n := 0
	for _, c := range req.Contents {
		for _, p := range c.Parts {
			fc, ok := p["functionCall"].(map[string]any)
			if !ok {
				continue
			}
			if n == i {
				s, _ := p["thoughtSignature"].(string)
				id, _ := fc["id"].(string)
				return s, id
			}
			n++
		}
	}
	t.Fatalf("no functionCall part %d", i)
	return "", ""
}

func assistantWithCalls(ids ...string) string {
	calls := make([]string, 0, len(ids))
	for _, id := range ids {
		calls = append(calls, `{"id":"`+id+`","type":"function","function":{"name":"web_search","arguments":"{\"query\":\"x\"}"}}`)
	}
	return `{"role":"assistant","content":null,"tool_calls":[` + strings.Join(calls, ",") + `]}`
}

func chatBody(messages ...string) []byte {
	return []byte(`{"model":"gemini-4-argon","messages":[` + strings.Join(messages, ",") + `]}`)
}

func TestReplayMixedSignaturesFallsBackToSentinelForAll(t *testing.T) {
	// Upstream attached a real signature to only one part of a multi-call
	// turn. A replay must not mix that real signature with sentinels.
	body := chatBody(
		`{"role":"user","content":"hi"}`,
		assistantWithCalls("call_a", "call_b"),
		`{"role":"tool","tool_call_id":"call_a","content":"{}"}`,
		`{"role":"tool","tool_call_id":"call_b","content":"{}"}`,
	)
	sigFor := func(id string) string {
		if id == "call_a" {
			return "REAL_SIG"
		}
		return ""
	}
	out, _, _, err := ChatToGemini(body, sigFor)
	if err != nil {
		t.Fatalf("ChatToGemini: %v", err)
	}
	for i := 0; i < 2; i++ {
		sig, _ := sigPart(t, out, i)
		if sig != skipSignature {
			t.Fatalf("part %d: sig = %q, want sentinel %q (mixed replays are rejected upstream)", i, sig, skipSignature)
		}
	}
}

func TestReplayAllRealSignaturesKeepsThem(t *testing.T) {
	body := chatBody(
		`{"role":"user","content":"hi"}`,
		assistantWithCalls("call_a", "call_b"),
	)
	sigFor := func(id string) string {
		if id == "call_a" {
			return "SIG_A"
		}
		if id == "call_b" {
			return "SIG_B"
		}
		return ""
	}
	out, _, _, err := ChatToGemini(body, sigFor)
	if err != nil {
		t.Fatalf("ChatToGemini: %v", err)
	}
	if sig, _ := sigPart(t, out, 0); sig != "SIG_A" {
		t.Fatalf("part 0: sig = %q, want SIG_A", sig)
	}
	if sig, _ := sigPart(t, out, 1); sig != "SIG_B" {
		t.Fatalf("part 1: sig = %q, want SIG_B", sig)
	}
}

func TestReplaySingleCallKeepsRealSignature(t *testing.T) {
	body := chatBody(
		`{"role":"user","content":"hi"}`,
		assistantWithCalls("call_a"),
	)
	out, _, _, err := ChatToGemini(body, func(id string) string {
		if id == "call_a" {
			return "REAL_SIG"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("ChatToGemini: %v", err)
	}
	if sig, _ := sigPart(t, out, 0); sig != "REAL_SIG" {
		t.Fatalf("sig = %q, want REAL_SIG", sig)
	}
}

func TestReplayNoCachedSignaturesUsesSentinel(t *testing.T) {
	body := chatBody(
		`{"role":"user","content":"hi"}`,
		assistantWithCalls("call_a", "call_b"),
	)
	out, _, _, err := ChatToGemini(body, func(string) string { return "" })
	if err != nil {
		t.Fatalf("ChatToGemini: %v", err)
	}
	for i := 0; i < 2; i++ {
		if sig, _ := sigPart(t, out, i); sig != skipSignature {
			t.Fatalf("part %d: sig = %q, want sentinel", i, sig)
		}
	}
}

func genConfigOf(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var req struct {
		GenerationConfig map[string]any `json:"generationConfig"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return req.GenerationConfig
}

func TestChatToGeminiRequestsThoughtSummaries(t *testing.T) {
	msg := `{"role":"user","content":"hi"}`
	cases := []struct {
		name  string
		body  string
		level string
	}{
		{"reasoning_effort", `{"model":"gemini-4-carbon","messages":[` + msg + `],"reasoning_effort":"high"}`, "high"},
		{"reasoning object alias", `{"model":"gemini-4-carbon","messages":[` + msg + `],"reasoning":{"effort":"low"}}`, "low"},
		{"minimal clamps to low", `{"model":"gemini-4-carbon","messages":[` + msg + `],"reasoning_effort":"minimal"}`, "low"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, _, _, err := ChatToGemini([]byte(c.body), func(string) string { return "" })
			if err != nil {
				t.Fatalf("ChatToGemini: %v", err)
			}
			tc := wireMap(genConfigOf(t, out)["thinkingConfig"])
			if got := tc["thinkingLevel"]; got != c.level {
				t.Errorf("thinkingLevel = %v, want %v", got, c.level)
			}
			if tc["includeThoughts"] != true {
				t.Errorf("includeThoughts = %v, want true", tc["includeThoughts"])
			}
		})
	}

	t.Run("none disables thinking", func(t *testing.T) {
		body := `{"model":"gemini-4-carbon","messages":[` + msg + `],"reasoning_effort":"none"}`
		out, _, _, err := ChatToGemini([]byte(body), func(string) string { return "" })
		if err != nil {
			t.Fatalf("ChatToGemini: %v", err)
		}
		tc := wireMap(genConfigOf(t, out)["thinkingConfig"])
		if got, _ := numAsInt(tc["thinkingBudget"]); got != 0 {
			t.Errorf("thinkingBudget = %v, want 0", tc["thinkingBudget"])
		}
		if tc["includeThoughts"] != false {
			t.Errorf("includeThoughts = %v, want false", tc["includeThoughts"])
		}
	})
}

const geminiReasoningFixture = `data: {"candidates":[{"content":{"parts":[{"text":"weigh ","thought":true}]}}]}

data: {"candidates":[{"content":{"parts":[{"text":"options","thought":true}]}}]}

data: {"candidates":[{"content":{"parts":[{"text":"42"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"thoughtsTokenCount":30,"totalTokenCount":42}}
`

func TestStreamGeminiRelaysReasoning(t *testing.T) {
	var buf bytes.Buffer
	in, out, err := StreamGeminiToChat(&buf, strings.NewReader(geminiReasoningFixture), "gemini-4-carbon", nil)
	if err != nil {
		t.Fatalf("StreamGeminiToChat: %v", err)
	}
	if in != 10 || out != 32 {
		t.Errorf("tokens = %d/%d, want 10/32", in, out)
	}
	var reasoning, content string
	var final map[string]any
	for _, line := range strings.Split(buf.String(), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			continue
		}
		chunk := tstObj(t, []byte(payload))
		ch := tstChoice(t, chunk)
		delta := wireMap(ch["delta"])
		if s, ok := delta["reasoning_content"].(string); ok {
			reasoning += s
		}
		if s, ok := delta["content"].(string); ok {
			content += s
		}
		if ch["finish_reason"] == "stop" {
			final = chunk
		}
	}
	if reasoning != "weigh options" {
		t.Errorf("reasoning = %q, want %q", reasoning, "weigh options")
	}
	if content != "42" {
		t.Errorf("content = %q, want %q", content, "42")
	}
	usage := wireMap(final["usage"])
	if wireNumInt(usage["completion_tokens"]) != 32 || wireNumInt(usage["total_tokens"]) != 42 {
		t.Errorf("usage = %s", tstJSON(usage))
	}
	if got := wireNumInt(wireMap(usage["completion_tokens_details"])["reasoning_tokens"]); got != 30 {
		t.Errorf("reasoning_tokens = %d, want 30", got)
	}
}

func TestGeminiToChatRelaysReasoning(t *testing.T) {
	raw, in, out, err := GeminiToChat(strings.NewReader(geminiReasoningFixture), "gemini-4-carbon", nil)
	if err != nil {
		t.Fatalf("GeminiToChat: %v", err)
	}
	if in != 10 || out != 32 {
		t.Errorf("tokens = %d/%d, want 10/32", in, out)
	}
	m := tstObj(t, raw)
	msg := wireMap(tstChoice(t, m)["message"])
	if msg["reasoning_content"] != "weigh options" || msg["content"] != "42" {
		t.Errorf("message = %s", tstJSON(msg))
	}
	usage := wireMap(m["usage"])
	if wireNumInt(usage["completion_tokens"]) != 32 {
		t.Errorf("usage = %s", tstJSON(usage))
	}
	if got := wireNumInt(wireMap(usage["completion_tokens_details"])["reasoning_tokens"]); got != 30 {
		t.Errorf("reasoning_tokens = %d, want 30", got)
	}
}
