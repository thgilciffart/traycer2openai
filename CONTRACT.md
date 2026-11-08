# Traycer inference — verified wire contract

Everything below was extracted from the Traycer Desktop AppImage (Electron `app.asar`) and the
installed host runtime (`~/.traycer/host/install/host-runtime/host-main.cjs`), then confirmed
against the live Traycer server. Do not change these values speculatively.

## Hosts

| purpose | base |
| --- | --- |
| inference + model catalog | `https://server.traycer.ai` |
| token refresh (authn) | `https://authn.traycer.ai` |

## Auth

Per-account credential file written by Traycer (`~/.traycer/host/auth/credentials.json`):

```json
{ "token": "<15 min JWT>", "refreshToken": "<long lived JWE>",
  "ownerUserId": "<uuid>", "hostId": "<uuid>", "familyId": "<uuid>",
  "provisionedAt": "...", "acquiredAt": "..." }
```

Access token is an RS256 JWT, `aud: host`, **lifetime 15 minutes**.

### Refresh

```
POST https://authn.traycer.ai/api/v3/auth/refresh
authorization: Bearer <current access token>     <-- must still be VALID
content-type: application/json

{"refreshToken": "<refreshToken>"}
```

- `200` → `{"token": "...", "refreshToken": "...", "familyId": "..."}` (both tokens rotate; persist the new refreshToken)
- `401`/`403` → `{"statusCode":401,"error":"Access token is invalid","reason":"invalid"}`
  `reason` ∈ `revoked` | `needs_reauth` | `rate_limited` | `invalid`
- `409`/`429`/`5xx` → retryable

**Critical:** the refresh call authenticates with the *existing* access token. If it has already
expired the refresh fails and the account is burned (`needs_reauth`, user must sign in again in the
desktop app). So refresh **proactively** — when `exp - now < 120s`, and never wait for a 401.

## Account selection header

```
x-traycer-account-context: {"type":"PERSONAL"}
x-traycer-account-context: {"type":"TEAM","teamId":"<uuid>"}
```

Traycer forwards headers matching `x-traycer-*` plus `accept`, `content-type`, `content-length`,
`anthropic-version`, `anthropic-beta`, `openai-beta`. It sets `accept-encoding: identity`.

## Inference — Responses API only

```
POST https://server.traycer.ai/inference/openai/responses
authorization: Bearer <access token>
x-traycer-account-context: {"type":"PERSONAL"}
content-type: application/json
accept: application/json
```

Request body is the OpenAI **Responses** shape (`model`, `input`, `instructions`, `stream`,
`max_output_tokens`, `reasoning.effort`, `tools`, `tool_choice`, `text`, `temperature`, `top_p`).

Owned/proxied path whitelist on the server is `/inference/<provider>/<path>` where provider is
`openai` | `anthropic` | `gemini`. **Only `openai/responses` is enabled for this account**;
`/inference/openai/chat/completions`, `/completions`, `/inference/openai/models`, `/inference/models`
all return `404 {"code":"UNSUPPORTED_TRAYCER_PATH"}`.

So an OpenAI chat-completions client must be translated to Responses. Streaming is Responses SSE
events; they must be translated back to `chat.completion.chunk` frames.

Observed error envelopes (JSON, non-2xx):

| status | code | meaning |
| --- | --- | --- |
| 401 | `UNAUTHORIZED` | access token rejected |
| 402 | `OUT_OF_CREDITS` | account has no inference credits |
| 404 | `UNSUPPORTED_TRAYCER_PATH` | path not enabled |
| 404 | `ROUTE_NOT_FOUND` | unknown route |

## Interactive sign-in (device flow)

Traycer has no loopback OAuth redirect. Its only interactive login is the **OAuth 2.0 Device
Authorization Grant** (RFC 8628), and the desktop comment says so directly:

> Device flow is the only login - there is no loopback redirect_uri

Client id is the public `desktop`, there is no client secret, and approval happens in a browser,
so a proxy can drive the same flow and end up with a normal token pair.

### 1. Start

```
POST https://authn.traycer.ai/api/v3/auth/device/authorize
content-type: application/json

{"client_id":"desktop","host_label":"<anything>"}
```

`200`:

```json
{"device_code":"…","user_code":"E4556-XT3CA",
 "verification_uri":"https://traycer.ai/device",
 "verification_uri_complete":"https://traycer.ai/device?user_code=E4556-XT3CA",
 "expires_in":600,"interval":5}
```

Show `user_code` to the human and open `verification_uri_complete`.

### 2. Poll

```
POST https://authn.traycer.ai/api/v3/auth/device/token
content-type: application/json

{"device_code":"…","client_id":"desktop"}
```

| status | body / header | meaning |
| --- | --- | --- |
| `200` | `{"token","refreshToken","familyId"}` | approved, store both tokens |
| `428` | `{"error":"authorization_pending"}` | keep polling |
| `429` | `Retry-After` header | back off, then poll |
| `4xx` | `{"error":"access_denied"}` | user declined, terminal |
| `4xx` | `{"error":"expired"}` | code expired, terminal |
| other | | treat as invalid, terminal |

Poll no faster than `interval` seconds (default 5) and stop at `expires_in` (default 600s). The
resulting `refreshToken` is a normal long-lived one, so this is the clean way to add an account
that the proxy then keeps warm by itself.

## Gemini is a different route, not the OpenAI proxy

This is the single most important thing about Traycer's inference surface. Gemini
models are **not** served by `/inference/openai/responses`. They have their own
native route, and the two are billed differently: an account with no Traycer
inference credits returns `402 OUT_OF_CREDITS` on the OpenAI route while the
Gemini route answers `200` on the very same token.

```
POST https://server.traycer.ai/inference/gemini/models/<model>:streamGenerateContent?alt=sse
authorization: Bearer <access token>
x-traycer-account-context: PERSONAL          <- plain string here, not the JSON form
content-type: application/json
accept: */*
```

The body is raw Gemini, not OpenAI:

```json
{
  "contents": [
    {"role": "user",  "parts": [{"text": "..."}]},
    {"role": "model", "parts": [{"functionCall": {"id": "...", "name": "...", "args": {}},
                                 "thoughtSignature": "..."}]},
    {"role": "user",  "parts": [{"functionResponse": {"id": "...", "name": "...",
                                 "response": {"name": "...", "content": "..."}}}]}
  ],
  "systemInstruction": {"parts": [{"text": "..."}]},
  "generationConfig": {"maxOutputTokens": 32000,
                       "thinkingConfig": {"includeThoughts": true, "thinkingLevel": "medium"}},
  "tools": [{"functionDeclarations": [{"name": "...", "description": "...", "parameters": {}}]}],
  "toolConfig": {"functionCallingConfig": {"mode": "AUTO"}}
}
```

The response is Gemini SSE, one `data:` object per event:

```
data: {"candidates":[{"content":{"parts":[{"text":"Hi"}],"role":"model"},"index":0}],
       "usageMetadata":{"promptTokenCount":25819,"candidatesTokenCount":1,...},
       "modelVersion":"gemini-3.8-flash","responseId":"..."}
data: {"candidates":[{"content":{"parts":[{"text":" there!"}]},...,"finishReason":"STOP"}],
       "usageMetadata":{...}}
```

Notes that cost real debugging time:

- **Thought signatures are mandatory on replay.** Gemini rejects a `functionCall`
  part with no `thoughtSignature` (`Function call is missing a thought_signature`)
  even when replaying a call the model itself made. The signature is validated
  cryptographically: dummy base64 gives `Corrupted thought signature`. The only
  accepted constants are absent (rejected) or the sentinel
  `skip_thought_signature_validator`, which Traycer's own host sends. This proxy
  prefers the real signature captured from the response that produced the call and
  falls back to that sentinel.
- **Thinking spends the output budget.** `maxOutputTokens: 32` can yield empty
  text because `thoughtsTokenCount` consumes the allowance. Setting
  `thinkingConfig.thinkingBudget: 0` for small budgets fixes it deterministically.
- **`thinkingLevel` accepts `low`/`medium`/`high` only.** `minimal` returns
  `400 Thinking level MINIMAL is not supported`.
- Gemini rejects JSON Schema keywords in function declarations
  (`additionalProperties`, `$schema`, `$defs`, `$id`), so they are stripped.
- One user turn can cost tens of thousands of prompt tokens because the whole
  conversation is resent. A one word message measured 25,819 prompt tokens, 23,758
  of them served from Gemini's implicit cache.

## Model catalog

```
GET https://server.traycer.ai/api/traycer/models?nativeProviders=gemini
authorization: Bearer <access token>
x-traycer-account-context: {"type":"PERSONAL"}
```

Returns a **bare JSON array** (not an object). Element shape:

```json
{ "id": "gpt-6-astra", "name": "GPT-6 Astra", "description": null,
  "provider": "openai", "upstreamModelId": "gpt-6-astra",
  "contextWindow": 1050000, "maxOutputTokens": 128000, "supportsImages": true,
  "defaultReasoningEffort": "medium",
  "supportedReasoningEfforts": [{"id":"low","label":"Low","description":null}],
  "deprecationNotice": null, "autoJudgeDefault": false }
```

34 models currently: gpt-6-astra, gpt-6.1-sol, gpt-6-sol, gpt-6-luna, gpt-5.6-sol/terra/luna,
gpt-5.5, gpt-5.4, claude-fable-5, claude-opus-5-5, claude-opus-5, claude-opus-4-8, claude-opus-4-7,
claude-sonnet-5-5, claude-sonnet-5, grok-4.7/4.6/4.5/4.3, muse-wonder-1.0, muse-spark-1.3/1.2/1.1,
gemini-4-argon, gemini-3.8-flash, glm-5.2, glm-5.3-flash, minimax-m3, deepseek-v4-flash,
deepseek-v4-pro, kimi-k3, kimi-k2.6, kimi-k2.7-code.

`provider` values seen: `openai`, `anthropic`, `xai`, `meta`, `gemini`, `openrouter`.

## Billing / credits (authn) — what traycer.ai/billing shows

Both endpoints live on `authn.traycer.ai`, take the same headers as inference
(`authorization: Bearer <access token>`, `x-traycer-account-context`), and were
confirmed live with a **host** token (`aud: host`):

```
GET https://authn.traycer.ai/api/user/credit-campaigns
GET https://authn.traycer.ai/api/v3/user
```

`credit-campaigns` is the billing page's "Credit offers" card (the `$99.938 of $100`
promo):

```json
{"campaigns":[{"id":"…","name":"Gemini Weekend","description":"2 days. $100. Any Gemini models.",
  "grantCredits":100,"startsAt":"…","endsAt":"…","claim":"active","remainingCredits":99.938}]}
```

`claim` is `"active"` once claimed, `"none"` before; `remainingCredits` drains as the
promo is spent.

`api/v3/user` returns the same `userSubscription` shape the web app reads from
`api/v2/user`, and the web-only endpoints reject host tokens:

```
GET /api/v2/user            -> 401 {"statusCode":401,"error":"Invalid token"}   (web session token only)
GET /api/user/subscription  -> 401 with a host token
GET /api/v3/user            -> 200 with a host token
```

`userSubscription` carries the "TOTAL CREDITS REMAINING" inputs:

```json
{"subscriptionStatus":"FREE","hasPaymentMethod":false,
 "bundleSummary":{"bundleTotal":0,"bundleConsumed":0,"bundleRemaining":0},
 "totalPlanCredits":0,
 "credit":{"bonusCredits":0,"consumedFromPlan":0,"consumedFromBonus":0}}
```

Traycer's own billing page (pricing-utils) computes the displayed total as
`planRemaining + bundleRemaining + bonusRemaining` with
`planRemaining = max(0, totalPlanCredits - consumedFromPlan)` and
`bonusRemaining = max(0, bonusCredits - consumedFromBonus)`. Campaign credits are
**not** part of that sum — an account with only a promo shows `$0.000` next to a
`$99.938` offer, exactly like the screenshot.

## How Traycer itself calls it

The host spawns a local OpenCode server configured with `baseURL = serverBaseUrl + "/inference/openai"`
and API key `{env:TRAYCER_ACCOUNT_CONTEXT}`. A loopback-only forwarder then rewrites
`/inference/<provider>/<path>` onto the real server and swaps in `Authorization: Bearer <token>`.
That is why the desktop works while only `responses` is enabled — its client speaks Responses.
