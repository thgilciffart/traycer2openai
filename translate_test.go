package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func tstObj(t *testing.T, b []byte) map[string]any {
	t.Helper()
	m, err := wireObject(b)
	if err != nil {
		t.Fatalf("not a JSON object: %v; body=%s", err, b)
	}
	return m
}

func tstJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "<marshal error>"
	}
	return string(b)
}

func tstHas(m map[string]any, path ...string) bool {
	var cur any = m
	for i, p := range path {
		cm := wireMap(cur)
		if cm == nil {
			return false
		}
		v, ok := cm[p]
		if !ok {
			return false
		}
		if i == len(path)-1 {
			return true
		}
		cur = v
	}
	return false
}

func tstChoice(t *testing.T, chunk map[string]any) map[string]any {
	t.Helper()
	choices := wireSlice(chunk["choices"])
	if len(choices) != 1 {
		t.Fatalf("chunk has %d choices, want 1", len(choices))
	}
	return wireMap(choices[0])
}

func TestTranslateChatToResponses(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		stream bool
		check  func(t *testing.T, out map[string]any)
	}{
		{
			name: "system folded into instructions",
			in:   `{"model":"gpt-6-astra","messages":[{"role":"system","content":"You are terse."},{"role":"user","content":"Hi"}]}`,
			check: func(t *testing.T, out map[string]any) {
				if out["instructions"] != "You are terse." {
					t.Errorf("instructions = %v", out["instructions"])
				}
				input := wireSlice(out["input"])
				if len(input) != 1 {
					t.Fatalf("input len = %d", len(input))
				}
				if got := tstJSON(input[0]); got != `{"content":[{"text":"Hi","type":"input_text"}],"role":"user","type":"message"}` {
					t.Errorf("input[0] = %s", got)
				}
			},
		},
		{
			name: "developer role and existing instructions merge with blank line",
			in:   `{"model":"m","instructions":"Be nice.","messages":[{"role":"developer","content":"Answer in French."},{"role":"user","content":"hi"}]}`,
			check: func(t *testing.T, out map[string]any) {
				if out["instructions"] != "Be nice.\n\nAnswer in French." {
					t.Errorf("instructions = %q", out["instructions"])
				}
				if got := len(wireSlice(out["input"])); got != 1 {
					t.Errorf("input len = %d", got)
				}
			},
		},
		{
			name: "assistant tool_calls become sibling function_call items, tool role maps to output",
			in: `{"model":"m","messages":[` +
				`{"role":"user","content":"Weather?"},` +
				`{"role":"assistant","content":"Let me check.","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]},` +
				`{"role":"tool","tool_call_id":"call_1","content":"Sunny, 22C"}]}`,
			check: func(t *testing.T, out map[string]any) {
				input := wireSlice(out["input"])
				if len(input) != 4 {
					t.Fatalf("input len = %d: %s", len(input), tstJSON(input))
				}
				asst := wireMap(input[1])
				if asst["role"] != "assistant" {
					t.Errorf("input[1].role = %v", asst["role"])
				}
				if got := tstJSON(wireSlice(asst["content"])[0]); got != `{"text":"Let me check.","type":"output_text"}` {
					t.Errorf("input[1].content[0] = %s", got)
				}
				if got := tstJSON(input[2]); got != `{"arguments":"{\"city\":\"Paris\"}","call_id":"call_1","name":"get_weather","type":"function_call"}` {
					t.Errorf("input[2] = %s", got)
				}
				if got := tstJSON(input[3]); got != `{"call_id":"call_1","output":"Sunny, 22C","type":"function_call_output"}` {
					t.Errorf("input[3] = %s", got)
				}
			},
		},
		{
			name: "assistant with empty text but tool_calls emits no empty message",
			in:   `{"model":"m","messages":[{"role":"user","content":"q"},{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]}]}`,
			check: func(t *testing.T, out map[string]any) {
				input := wireSlice(out["input"])
				if len(input) != 2 {
					t.Fatalf("input len = %d", len(input))
				}
				for _, it := range input {
					im := wireMap(it)
					if im["type"] == "message" && im["role"] == "assistant" {
						t.Errorf("unexpected assistant message emitted: %s", tstJSON(it))
					}
				}
				if wireMap(input[1])["type"] != "function_call" {
					t.Errorf("input[1].type = %v", wireMap(input[1])["type"])
				}
			},
		},
		{
			name: "assistant with neither text nor tool_calls is omitted",
			in:   `{"model":"m","messages":[{"role":"user","content":"q"},{"role":"assistant","content":null}]}`,
			check: func(t *testing.T, out map[string]any) {
				if got := len(wireSlice(out["input"])); got != 1 {
					t.Errorf("input len = %d, want 1", got)
				}
			},
		},
		{
			name: "image_url parts accept string and object forms",
			in:   `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"What is this?"},{"type":"image_url","image_url":"https://x/a.png"},{"type":"image_url","image_url":{"url":"https://x/b.png","detail":"high"}}]}]}`,
			check: func(t *testing.T, out map[string]any) {
				input := wireSlice(out["input"])
				content := wireSlice(wireMap(input[0])["content"])
				want := `[{"text":"What is this?","type":"input_text"},{"image_url":"https://x/a.png","type":"input_image"},{"image_url":"https://x/b.png","type":"input_image"}]`
				if got := tstJSON(content); got != want {
					t.Errorf("content = %s, want %s", got, want)
				}
			},
		},
		{
			name:   "max_tokens, temperature, null top_p, stream flag",
			in:     `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}],"max_tokens":512,"temperature":0.7,"top_p":null}`,
			stream: true,
			check: func(t *testing.T, out map[string]any) {
				if got := tstJSON(out["max_output_tokens"]); got != "512" {
					t.Errorf("max_output_tokens = %s", got)
				}
				if got := tstJSON(out["temperature"]); got != "0.7" {
					t.Errorf("temperature = %s", got)
				}
				if tstHas(out, "top_p") {
					t.Error("top_p should be omitted when null")
				}
			},
		},
		{
			name: "max_completion_tokens wins over max_tokens",
			in:   `{"model":"m","messages":[{"role":"user","content":"hi"}],"max_completion_tokens":256,"max_tokens":512}`,
			check: func(t *testing.T, out map[string]any) {
				if got := tstJSON(out["max_output_tokens"]); got != "256" {
					t.Errorf("max_output_tokens = %s, want 256", got)
				}
			},
		},
		{
			name: "tools and function tool_choice",
			in: `{"model":"m","messages":[{"role":"user","content":"hi"}],` +
				`"tools":[{"type":"function","function":{"name":"f","description":"do f","parameters":{"type":"object"},"strict":true}}],` +
				`"tool_choice":{"type":"function","function":{"name":"f"}}}`,
			check: func(t *testing.T, out map[string]any) {
				tools := wireSlice(out["tools"])
				if len(tools) != 1 {
					t.Fatalf("tools len = %d", len(tools))
				}
				want := `{"description":"do f","name":"f","parameters":{"type":"object"},"strict":true,"type":"function"}`
				if got := tstJSON(tools[0]); got != want {
					t.Errorf("tools[0] = %s, want %s", got, want)
				}
				if got := tstJSON(out["tool_choice"]); got != `{"name":"f","type":"function"}` {
					t.Errorf("tool_choice = %s", got)
				}
			},
		},
		{
			name: "strict dropped when nil, top-level strict accepted, auto choice passthrough",
			in: `{"model":"m","messages":[{"role":"user","content":"hi"}],` +
				`"tools":[{"type":"function","function":{"name":"g","parameters":{}}},{"type":"function","function":{"name":"h"},"strict":false}],` +
				`"tool_choice":"auto"}`,
			check: func(t *testing.T, out map[string]any) {
				tools := wireSlice(out["tools"])
				if _, ok := wireMap(tools[0])["strict"]; ok {
					t.Error("tools[0] should have no strict key")
				}
				if s := wireMap(tools[1])["strict"]; s != false {
					t.Errorf("tools[1].strict = %v, want false", s)
				}
				if out["tool_choice"] != "auto" {
					t.Errorf("tool_choice = %v", out["tool_choice"])
				}
			},
		},
		{
			name: "reasoning_effort maps to reasoning.effort",
			in:   `{"model":"m","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`,
			check: func(t *testing.T, out map[string]any) {
				if got := tstJSON(out["reasoning"]); got != `{"effort":"high"}` {
					t.Errorf("reasoning = %s", got)
				}
			},
		},
		{
			name: "response_format json_schema maps to text.format",
			in:   `{"model":"m","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_schema","json_schema":{"name":"out","schema":{"type":"object"},"strict":true}}}`,
			check: func(t *testing.T, out map[string]any) {
				want := `{"name":"out","schema":{"type":"object"},"strict":true,"type":"json_schema"}`
				if got := tstJSON(wireMap(out["text"])["format"]); got != want {
					t.Errorf("text.format = %s, want %s", got, want)
				}
			},
		},
		{
			name: "response_format json_object",
			in:   `{"model":"m","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_object"}}`,
			check: func(t *testing.T, out map[string]any) {
				if got := tstJSON(wireMap(out["text"])["format"]); got != `{"type":"json_object"}` {
					t.Errorf("text.format = %s", got)
				}
			},
		},
		{
			name: "response_format text is omitted",
			in:   `{"model":"m","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"text"}}`,
			check: func(t *testing.T, out map[string]any) {
				if tstHas(out, "text") {
					t.Error("text key should be omitted for response_format text")
				}
			},
		},
		{
			name: "unknown fields and stop are ignored",
			in:   `{"model":"m","messages":[{"role":"user","content":"hi"}],"stop":["x"],"n":5,"frequency_penalty":0.5,"user":"u1"}`,
			check: func(t *testing.T, out map[string]any) {
				for _, k := range []string{"stop", "n", "frequency_penalty", "user"} {
					if tstHas(out, k) {
						t.Errorf("key %q should not be present", k)
					}
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, stream, err := ChatToResponses([]byte(tc.in))
			if err != nil {
				t.Fatalf("ChatToResponses: %v", err)
			}
			if stream != tc.stream {
				t.Errorf("stream = %v, want %v", stream, tc.stream)
			}
			m := tstObj(t, out)
			if m["stream"] != tc.stream {
				t.Errorf(`out["stream"] = %v, want %v`, m["stream"], tc.stream)
			}
			if m["model"] == nil {
				t.Error("model missing")
			}
			tc.check(t, m)
		})
	}
}

func TestTranslateChatToResponsesErrors(t *testing.T) {
	bad := []string{
		`{"messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"m"}`,
		`{"model":"m","messages":[]}`,
		`{"model":"m","messages":"nope"}`,
		`[1,2,3]`,
		`not json at all`,
		`null`,
		``,
	}
	for _, in := range bad {
		if out, _, err := ChatToResponses([]byte(in)); err == nil {
			t.Errorf("ChatToResponses(%q) = %s, want error", in, out)
		}
	}
}

func TestTranslateResponsesToChat(t *testing.T) {
	t.Run("text and usage", func(t *testing.T) {
		resp := `{"id":"resp_1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello "},{"type":"output_text","text":"world"}]}],"usage":{"input_tokens":10,"output_tokens":5}}`
		out, err := ResponsesToChat([]byte(resp), "gpt-6-astra")
		if err != nil {
			t.Fatalf("ResponsesToChat: %v", err)
		}
		m := tstObj(t, out)
		if m["object"] != "chat.completion" {
			t.Errorf("object = %v", m["object"])
		}
		if m["model"] != "gpt-6-astra" {
			t.Errorf("model = %v", m["model"])
		}
		if id, _ := m["id"].(string); !strings.HasPrefix(id, "chatcmpl-") {
			t.Errorf("id = %v", id)
		}
		ch := tstChoice(t, m)
		msg := wireMap(ch["message"])
		if msg["role"] != "assistant" || msg["content"] != "Hello world" {
			t.Errorf("message = %s", tstJSON(msg))
		}
		if ch["finish_reason"] != "stop" {
			t.Errorf("finish_reason = %v", ch["finish_reason"])
		}
		usage := wireMap(m["usage"])
		if wireNumInt(usage["prompt_tokens"]) != 10 || wireNumInt(usage["completion_tokens"]) != 5 || wireNumInt(usage["total_tokens"]) != 15 {
			t.Errorf("usage = %s", tstJSON(usage))
		}
	})

	t.Run("function_call output", func(t *testing.T) {
		resp := `{"id":"resp_2","status":"completed","output":[{"type":"function_call","call_id":"c1","name":"get_weather","arguments":"{\"city\":\"Paris\"}"}],"usage":{"input_tokens":3,"output_tokens":4}}`
		out, err := ResponsesToChat([]byte(resp), "m")
		if err != nil {
			t.Fatalf("ResponsesToChat: %v", err)
		}
		m := tstObj(t, out)
		ch := tstChoice(t, m)
		if ch["finish_reason"] != "tool_calls" {
			t.Errorf("finish_reason = %v", ch["finish_reason"])
		}
		calls := wireSlice(wireMap(ch["message"])["tool_calls"])
		if len(calls) != 1 {
			t.Fatalf("tool_calls len = %d", len(calls))
		}
		call := wireMap(calls[0])
		fn := wireMap(call["function"])
		if call["id"] != "c1" || call["type"] != "function" || fn["name"] != "get_weather" || fn["arguments"] != `{"city":"Paris"}` {
			t.Errorf("tool_call = %s", tstJSON(call))
		}
	})

	t.Run("incomplete status maps to length", func(t *testing.T) {
		resp := `{"id":"resp_3","status":"incomplete","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"partial"}]}]}`
		out, err := ResponsesToChat([]byte(resp), "m")
		if err != nil {
			t.Fatalf("ResponsesToChat: %v", err)
		}
		m := tstObj(t, out)
		if got := tstChoice(t, m)["finish_reason"]; got != "length" {
			t.Errorf("finish_reason = %v", got)
		}
		if tstHas(m, "usage") {
			t.Error("usage should be omitted when absent upstream")
		}
	})

	t.Run("error envelope object", func(t *testing.T) {
		_, err := ResponsesToChat([]byte(`{"error":{"code":"OUT_OF_CREDITS","message":"no credits left"}}`), "m")
		if err == nil {
			t.Fatal("want error")
		}
		if !strings.Contains(err.Error(), "OUT_OF_CREDITS") || !strings.Contains(err.Error(), "no credits left") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("error envelope top-level code", func(t *testing.T) {
		_, err := ResponsesToChat([]byte(`{"code":"UNAUTHORIZED","message":"bad token"}`), "m")
		if err == nil {
			t.Fatal("want error")
		}
		if !strings.Contains(err.Error(), "UNAUTHORIZED") || !strings.Contains(err.Error(), "bad token") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestTranslateStreamResponsesToChat(t *testing.T) {
	fixture := `event: response.created
data: {"type":"response.created","response":{"id":"resp_abc"}}

: keep-alive comment

event: response.output_item.added
data: {"type":"response.output_item.added","item":{"type":"function_call","id":"item_1","call_id":"call_9","name":"get_weather","arguments":""}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"item_1","delta":"{\"city\":"}

data: {"type":"response.function_call_arguments.delta","item_id":"item_1","delta":"\"Paris\"}"}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"It is "}

data: {"type":"response.output_text.delta","delta":"sunny."}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"message","id":"msg_1"}}

event: response.in_progress
data: {"type":"response.in_progress","response":{"id":"resp_abc"}}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_abc","status":"completed","usage":{"input_tokens":11,"output_tokens":7}}}

data: [DONE]
`
	fixture = strings.ReplaceAll(fixture, "\n", "\r\n") // exercise CRLF tolerance

	var buf bytes.Buffer
	in, out, err := StreamResponsesToChat(&buf, strings.NewReader(fixture), "gpt-6-astra")
	if err != nil {
		t.Fatalf("StreamResponsesToChat: %v", err)
	}
	if in != 11 || out != 7 {
		t.Errorf("tokens = %d/%d, want 11/7", in, out)
	}
	s := buf.String()
	if !strings.HasSuffix(s, "data: [DONE]\n\n") {
		t.Fatalf("stream does not end with [DONE]: %q", s)
	}

	var chunks []map[string]any
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			continue
		}
		chunks = append(chunks, tstObj(t, []byte(payload)))
	}
	// initial + tool open + 2 arg deltas + 2 text deltas + final
	if len(chunks) != 7 {
		t.Fatalf("got %d chunks, want 7:\n%s", len(chunks), s)
	}
	for i, chunk := range chunks {
		if chunk["object"] != "chat.completion.chunk" {
			t.Errorf("chunk %d object = %v", i, chunk["object"])
		}
		if chunk["model"] != "gpt-6-astra" {
			t.Errorf("chunk %d model = %v", i, chunk["model"])
		}
		if id, _ := chunk["id"].(string); !strings.HasPrefix(id, "chatcmpl-") {
			t.Errorf("chunk %d id = %v", i, id)
		}
		ch := tstChoice(t, chunk)
		if _, ok := ch["finish_reason"]; !ok {
			t.Errorf("chunk %d missing finish_reason key", i)
		}
		if i < len(chunks)-1 && tstHas(chunk, "usage") {
			t.Errorf("chunk %d should not carry usage", i)
		}
	}

	c0 := tstChoice(t, chunks[0])
	d0 := wireMap(c0["delta"])
	if d0["role"] != "assistant" || d0["content"] != "" || c0["finish_reason"] != nil {
		t.Errorf("initial chunk = %s", tstJSON(c0))
	}

	c1 := tstChoice(t, chunks[1])
	tc1 := wireSlice(wireMap(c1["delta"])["tool_calls"])
	open := wireMap(tc1[0])
	if open["index"] != 0.0 && wireNumInt(open["index"]) != 0 {
		t.Errorf("tool index = %v", open["index"])
	}
	if open["id"] != "call_9" || open["type"] != "function" {
		t.Errorf("tool open = %s", tstJSON(open))
	}
	openFn := wireMap(open["function"])
	if openFn["name"] != "get_weather" || openFn["arguments"] != "" {
		t.Errorf("tool open function = %s", tstJSON(openFn))
	}

	c2 := tstChoice(t, chunks[2])
	arg1 := wireMap(wireSlice(wireMap(c2["delta"])["tool_calls"])[0])
	if got := wireMap(arg1["function"])["arguments"]; got != `{"city":` {
		t.Errorf("args delta 1 = %v", got)
	}
	c3 := tstChoice(t, chunks[3])
	arg2 := wireMap(wireSlice(wireMap(c3["delta"])["tool_calls"])[0])
	if got := wireMap(arg2["function"])["arguments"]; got != `"Paris"}` {
		t.Errorf("args delta 2 = %v", got)
	}
	if wireNumInt(arg2["index"]) != 0 {
		t.Errorf("args delta 2 index = %v", arg2["index"])
	}

	if got := wireMap(tstChoice(t, chunks[4])["delta"])["content"]; got != "It is " {
		t.Errorf("text delta 1 = %v", got)
	}
	if got := wireMap(tstChoice(t, chunks[5])["delta"])["content"]; got != "sunny." {
		t.Errorf("text delta 2 = %v", got)
	}

	final := tstChoice(t, chunks[6])
	if final["finish_reason"] != "tool_calls" {
		t.Errorf("final finish_reason = %v", final["finish_reason"])
	}
	usage := wireMap(chunks[6]["usage"])
	if wireNumInt(usage["prompt_tokens"]) != 11 || wireNumInt(usage["completion_tokens"]) != 7 || wireNumInt(usage["total_tokens"]) != 18 {
		t.Errorf("final usage = %s", tstJSON(usage))
	}
}

func TestTranslateStreamErrors(t *testing.T) {
	fixtures := map[string]string{
		"error event":      "data: {\"type\":\"error\",\"message\":\"boom\"}\n\n",
		"error envelope":   "data: {\"error\":{\"code\":\"X\",\"message\":\"bad news\"}}\n\n",
		"error no message": "data: {\"type\":\"error\"}\n\n",
	}
	for name, fixture := range fixtures {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			_, _, err := StreamResponsesToChat(&buf, strings.NewReader(fixture), "m")
			if err == nil {
				t.Fatalf("want error for %s", fixture)
			}
		})
	}
	var buf bytes.Buffer
	_, _, err := StreamResponsesToChat(&buf, strings.NewReader("data: {\"type\":\"error\",\"message\":\"boom\"}\n\n"), "m")
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v, want it to contain boom", err)
	}
}

func TestTranslateStreamDoneOnly(t *testing.T) {
	var buf bytes.Buffer
	in, out, err := StreamResponsesToChat(&buf, strings.NewReader("data: [DONE]\n\n"), "m")
	if err != nil {
		t.Fatalf("StreamResponsesToChat: %v", err)
	}
	if in != 0 || out != 0 {
		t.Errorf("tokens = %d/%d, want 0/0", in, out)
	}
	s := buf.String()
	if !strings.HasSuffix(s, "data: [DONE]\n\n") {
		t.Errorf("output = %q", s)
	}
	var finishes []any
	for _, line := range strings.Split(s, "\n") {
		if !strings.HasPrefix(line, "data: ") || strings.TrimPrefix(line, "data: ") == "[DONE]" {
			continue
		}
		chunks := tstObj(t, []byte(strings.TrimPrefix(line, "data: ")))
		finishes = append(finishes, tstChoice(t, chunks)["finish_reason"])
	}
	if len(finishes) != 2 || finishes[0] != nil || finishes[1] != "stop" {
		t.Errorf("finishes = %v, want [nil stop]", finishes)
	}
}

func TestTranslateStreamMultiLineData(t *testing.T) {
	fixture := "data: {\"type\":\"response.output_text.delta\",\n" +
		"data: \"delta\":\"multi\"}\n\n" +
		"data: [DONE]\n\n"
	var buf bytes.Buffer
	_, _, err := StreamResponsesToChat(&buf, strings.NewReader(fixture), "m")
	if err != nil {
		t.Fatalf("StreamResponsesToChat: %v", err)
	}
	if !strings.Contains(buf.String(), `"content":"multi"`) {
		t.Errorf("output missing multi-line delta: %s", buf.String())
	}
}

type flushRecorder struct {
	bytes.Buffer
	flushes int
}

func (f *flushRecorder) Flush() { f.flushes++ }

func TestTranslateStreamFlushes(t *testing.T) {
	fw := &flushRecorder{}
	fixture := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\ndata: [DONE]\n\n"
	if _, _, err := StreamResponsesToChat(fw, strings.NewReader(fixture), "m"); err != nil {
		t.Fatalf("StreamResponsesToChat: %v", err)
	}
	if fw.flushes == 0 {
		t.Error("Flush was never called on a flushing writer")
	}
}
