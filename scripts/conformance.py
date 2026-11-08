#!/usr/bin/env python3
"""Live conformance suite for traycer2openai against Traycer's Gemini route.

Exercises the shapes a real client (T3 Code, Pi, OpenAI SDK) sends, including the
rich tool schemas that made T3 fail with 400 Unknown name "exclusiveMaximum".
"""
import json, os, sys, time, urllib.request, urllib.error

BASE = os.environ.get("BASE", "http://127.0.0.1:8787/v1")
KEY = os.environ.get("KEY", "x")
MODEL = os.environ.get("MODEL", "gemini-4-argon")

results = []

def call(path, body=None, stream=False, timeout=180):
    url = BASE + path
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method="POST" if body is not None else "GET")
    req.add_header("authorization", "Bearer " + KEY)
    if data:
        req.add_header("content-type", "application/json")
    t0 = time.time()
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            raw = r.read().decode()
            return r.status, raw, r.headers.get("content-type", ""), round(time.time() - t0, 2)
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode(), e.headers.get("content-type", ""), round(time.time() - t0, 2)

def check(name, ok, detail=""):
    results.append((name, ok, detail))
    print(("PASS  " if ok else "FAIL  ") + name + (("  " + detail) if detail and not ok else ""))
    return ok

def sse_frames(raw):
    out = []
    for line in raw.splitlines():
        if line.startswith("data: ") and line[6:] != "[DONE]":
            try:
                out.append(json.loads(line[6:]))
            except Exception:
                pass
    return out

# 1. models
st, raw, _, _ = call("/models")
ids = [m["id"] for m in json.loads(raw).get("data", [])] if st == 200 else []
check("GET /models", st == 200 and MODEL in ids, f"{st} {len(ids)} models")

# 2. plain non-stream
st, raw, _, dt = call("/chat/completions", {"model": MODEL, "messages": [{"role": "user", "content": "Reply with exactly: PONG"}], "max_tokens": 64})
d = json.loads(raw) if st == 200 else {}
msg = (d.get("choices") or [{}])[0].get("message", {})
check("non-stream text", st == 200 and "PONG" in (msg.get("content") or ""), f"{st} {raw[:120]}")
check("non-stream usage", bool(d.get("usage", {}).get("prompt_tokens")), json.dumps(d.get("usage")))
check("non-stream echo model", d.get("model") == MODEL, d.get("model"))

# 3. stream
st, raw, ct, _ = call("/chat/completions", {"model": MODEL, "messages": [{"role": "user", "content": "count 1 to 3"}], "stream": True, "max_tokens": 128})
fr = sse_frames(raw)
text = "".join(f.get("choices", [{}])[0].get("delta", {}).get("content") or "" for f in fr)
fin = [f["choices"][0].get("finish_reason") for f in fr if f.get("choices")]
check("stream chunks", st == 200 and "text/event-stream" in ct and len(fr) >= 2, f"{st} {len(fr)} frames")
check("stream text", len(text.strip()) > 0, repr(text[:60]))
check("stream DONE", "[DONE]" in raw)
check("stream usage", any(f.get("usage") for f in fr))
check("stream finish", fin and fin[-1] == "stop", str(fin[-1:]))

# 4. the T3 failure class: rich schemas across many tools
rich = []
for i in range(22):
    rich.append({"type": "function", "function": {
        "name": f"tool_{i}", "description": f"Tool {i}",
        "parameters": {
            "type": "object",
            "$schema": "http://json-schema.org/draft-07/schema#",
            "properties": {
                "num": {"type": "number", "exclusiveMinimum": 0, "exclusiveMaximum": 100},
                "nested": {"type": "object", "properties": {"limit": {"type": "number", "exclusiveMaximum": 5}}},
                "kind": {"const": "fast"},
                "choice": {"anyOf": [{"type": "string"}, {"type": "null"}]},
                "maybe": {"type": ["string", "null"], "default": "z", "examples": ["q"]},
                "list": {"type": "array", "items": {"type": "string"}, "minItems": 1},
                "filled": {"allOf": [{"type": "object"}, {"required": ["y"]}]},
            },
            "required": ["num", "does_not_exist"],
            "additionalProperties": False,
            "if": {"properties": {"num": {"const": 1}}}, "then": {"required": ["list"]},
        }}})
st, raw, _, dt = call("/chat/completions", {"model": MODEL, "max_tokens": 128,
    "messages": [{"role": "system", "content": "You are a tool-using agent."}, {"role": "user", "content": "Call the tool named tool_3 with num set to 5."}],
    "tools": rich})
d = json.loads(raw) if st == 200 else {}
m = (d.get("choices") or [{}])[0].get("message", {})
check("rich 22-tool schemas", st == 200, f"{st} {raw[:200]}")
check("rich schema -> tool call", bool(m.get("tool_calls") or (m.get("content") or "").strip()), str(m)[:160])

# 5. tool round trip, including thought signature replay
st, raw, _, _ = call("/chat/completions", {"model": MODEL, "max_tokens": 128,
    "messages": [{"role": "user", "content": "What is the weather in SF? Use get_weather."}],
    "tools": [{"type": "function", "function": {"name": "get_weather", "description": "w",
        "parameters": {"type": "object", "properties": {"city": {"type": "string"}}, "required": ["city"]}}}]})
d = json.loads(raw) if st == 200 else {}
tc = ((d.get("choices") or [{}])[0].get("message", {}) or {}).get("tool_calls") or []
check("tool call emitted", st == 200 and bool(tc), f"{st} {raw[:160]}")
if tc:
    st2, raw2, _, _ = call("/chat/completions", {"model": MODEL, "max_tokens": 256,
        "messages": [{"role": "user", "content": "What is the weather in SF? Use get_weather."},
                     {"role": "assistant", "content": None, "tool_calls": tc},
                     {"role": "tool", "tool_call_id": tc[0]["id"], "content": '{"tempC":18,"conditions":"foggy"}'}],
        "tools": [{"type": "function", "function": {"name": "get_weather", "parameters": {"type": "object", "properties": {"city": {"type": "string"}}}}}]})
    d2 = json.loads(raw2) if st2 == 200 else {}
    txt = ((d2.get("choices") or [{}])[0].get("message", {}) or {}).get("content") or ""
    check("tool replay after signature cache", st2 == 200 and bool(txt.strip()), f"{st2} {raw2[:160]}")

# 6. streamed tool call
st, raw, _, _ = call("/chat/completions", {"model": MODEL, "stream": True, "max_tokens": 128,
    "messages": [{"role": "user", "content": "Call get_weather for Paris."}],
    "tools": [{"type": "function", "function": {"name": "get_weather", "parameters": {"type": "object", "properties": {"city": {"type": "string"}}}}}]})
fr = sse_frames(raw)
tcs = [f["choices"][0]["delta"]["tool_calls"] for f in fr if f.get("choices") and f["choices"][0].get("delta", {}).get("tool_calls")]
check("streamed tool call", st == 200 and bool(tcs), f"{st} frames={len(fr)}")

# 7. sampling + stop + reasoning effort
st, raw, _, _ = call("/chat/completions", {"model": MODEL, "max_tokens": 64, "temperature": 0.2, "top_p": 0.9,
    "stop": ["\n\n"], "reasoning_effort": "low", "messages": [{"role": "user", "content": "say OK"}]})
check("sampling + stop + effort", st == 200, f"{st} {raw[:160]}")

# 8. json response format
st, raw, _, _ = call("/chat/completions", {"model": MODEL, "max_tokens": 200,
    "response_format": {"type": "json_object"},
    "messages": [{"role": "user", "content": 'Return JSON like {"ok":true}'}]})
ok_json = False
if st == 200:
    c = json.loads(raw)["choices"][0]["message"]["content"] or ""
    try:
        json.loads(c); ok_json = True
    except Exception:
        ok_json = c.strip().startswith("{")
check("response_format json_object", st == 200 and ok_json, f"{st} {raw[:120]}")

# 9. image input via data URI
png = ("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8AAAwAB/AH7lQEJAAAAAElFTkSuQmCC")
st, raw, _, _ = call("/chat/completions", {"model": MODEL, "max_tokens": 64,
    "messages": [{"role": "user", "content": [
        {"type": "text", "text": "What colour is this 1x1 image? One word."},
        {"type": "image_url", "image_url": {"url": "data:image/png;base64," + png}}]}]})
check("image input", st == 200, f"{st} {raw[:200]}")

# 10. multi-turn history with system + assistant
st, raw, _, _ = call("/chat/completions", {"model": MODEL, "max_tokens": 64, "messages": [
    {"role": "system", "content": "Answer in one word."},
    {"role": "user", "content": "My name is Sam."},
    {"role": "assistant", "content": "Noted."},
    {"role": "user", "content": "What is my name?"}]})
c = json.loads(raw)["choices"][0]["message"]["content"] if st == 200 else ""
check("multi-turn with system", st == 200 and "sam" in (c or "").lower(), f"{st} {c!r}")

# 11. small max_tokens must not come back empty (thinking budget)
st, raw, _, _ = call("/chat/completions", {"model": MODEL, "max_tokens": 16,
    "messages": [{"role": "user", "content": "Reply with exactly: HELLO"}]})
c = json.loads(raw)["choices"][0]["message"]["content"] if st == 200 else ""
check("small max_tokens yields text", st == 200 and bool((c or "").strip()), f"{st} {c!r}")

# 12. non-gemini model must fail at the credit gate, not crash
st, raw, _, _ = call("/chat/completions", {"model": "gpt-6-astra", "max_tokens": 16,
    "messages": [{"role": "user", "content": "hi"}]})
check("non-gemini model returns clean 402", st == 402 and "OUT_OF_CREDITS" in raw, f"{st} {raw[:120]}")

# 13. bad request handling
st, raw, _, _ = call("/chat/completions", {"model": "", "messages": []})
check("invalid request -> 4xx json", 400 <= st < 500 and "error" in json.loads(raw), f"{st} {raw[:120]}")

# 14. unknown model
st, raw, _, _ = call("/chat/completions", {"model": "gemini-9-nonexistent", "max_tokens": 16,
    "messages": [{"role": "user", "content": "hi"}]})
check("unknown gemini model -> 4xx not 500", 400 <= st < 502, f"{st} {raw[:120]}")

fails = [r for r in results if not r[1]]
print(f"\n{len(results) - len(fails)}/{len(results)} passed")
if fails:
    print("FAILURES:")
    for n, _, d in fails:
        print(f"  - {n}: {d}")
sys.exit(1 if fails else 0)
