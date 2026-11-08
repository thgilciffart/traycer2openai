package main

import (
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
