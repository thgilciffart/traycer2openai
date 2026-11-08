# traycer2openai

A single-binary proxy that puts an OpenAI-compatible API in front of Traycer's inference
endpoint, with multiple Traycer accounts and per-request account routing.

Go, stdlib only, no build step, no runtime dependencies. ~11 MB static binary.

```
your client ──► /v1/chat/completions ──► translate ──► server.traycer.ai
                /v1/responses ────────► passthrough ─►   /inference/openai/responses
```

## Why the translation exists

Traycer only enables one inference path for its accounts:

| endpoint | Traycer | this proxy |
| --- | --- | --- |
| `POST /inference/openai/responses` | supported | native passthrough |
| `/inference/openai/chat/completions` | `404 UNSUPPORTED_TRAYCER_PATH` | translated |
| `/inference/openai/completions` | not supported | not implemented |

So `/v1/responses` is a pure passthrough (auth swap plus account routing, nothing else), and
`/v1/chat/completions` is translated because nearly every OpenAI-compatible client speaks it.
The translation is stateless: no `response_id` bookkeeping, no conversation store.

`CONTRACT.md` documents the whole verified wire protocol, including how it was extracted from
the desktop binary and confirmed against the live server.

## Quick start

```bash
cp .env.example .env
# edit .env: set LOGIN_TOKEN, and a PORT if 8787 is taken

go build -o traycer2openai .
./traycer2openai
```

Open the dashboard, add an account, then point a client at it:

```bash
curl http://127.0.0.1:8787/v1/chat/completions \
  -H "Authorization: Bearer $LOGIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gemini-3.8-flash","messages":[{"role":"user","content":"hi"}]}'
```

Or with the OpenAI SDK:

```python
from openai import OpenAI
client = OpenAI(base_url="http://127.0.0.1:8787/v1", api_key=LOGIN_TOKEN)
client.chat.completions.create(model="gemini-3.8-flash", messages=[{"role": "user", "content": "hi"}])
```

`.env` is read from the working directory. Real environment variables override it, so
`PORT=9000 ./traycer2openai` works without editing anything.

## Accounts

### Sign in from the dashboard

Press **Sign In with Traycer**, approve the code in the browser tab it opens, and the account is
created and made active. That is the whole flow. It uses Traycer's OAuth device authorization
grant (RFC 8628), the same one the desktop app uses, so whatever you are signed into in the
browser is what gets added.

The proxy finishes the exchange server-side and stores a normal access token plus refresh token,
which it then keeps alive on its own. Repeat it for each account you want to route through.

### Or add one by hand

Two manual modes, under **Add Account Manually**:

**1. Own refresh token.** Paste `token` and `refreshToken` from a Traycer `credentials.json`.
The proxy owns that token chain and keeps it warm: access tokens live 15 minutes and the proxy
refreshes them ahead of expiry for as long as it runs. Works headless, survives the desktop app
being closed.

**2. Borrow the desktop app's credentials.** Give a path instead of a token, usually

```
~/.traycer/host/auth/credentials.json
```

The proxy re-reads that file on every request and never refreshes it, because the Traycer host
already does. Use this when you only have one account and it is signed in on this machine.

> Do not give two independent processes the same refresh token. Traycer rotates it on every
> refresh, so two refreshers invalidate each other and one of them gets `needs_reauth` (a forced
> re-sign-in). Pick mode 1 *or* mode 2 per account, not both. Mode 2 exists precisely to avoid
> this trap.

### Routing

The account is chosen per request:

1. `X-Traycer-Account: <name or id>` header, if present
2. the account marked active in the dashboard
3. `DEFAULT_ACCOUNT` from `.env`
4. the first healthy account

Team billing is a header, not a separate account type: set `teamId` when adding, and the proxy
sends `x-traycer-account-context: {"type":"TEAM","teamId":"..."}` instead of `PERSONAL`.

## Endpoints

| path | auth | notes |
| --- | --- | --- |
| `POST /v1/chat/completions` | Bearer | translated, streaming and non-streaming, tools, images |
| `POST /v1/responses` | Bearer | raw passthrough to Traycer |
| `GET /v1/models` | Bearer | Traycer's real catalog, cached `MODELS_CACHE_SECONDS` |
| `GET /health` | none | liveness |
| `GET /` | cookie | dashboard |
| `/api/*` | cookie or Bearer | dashboard API |
| `/api/auth/device/*` | cookie or Bearer | Traycer sign-in: `start`, `status`, `cancel` |

### Running the API without a key

Set `API_AUTH=false` and `/v1/*` stops checking `Authorization` entirely, while the dashboard
still needs `LOGIN_TOKEN`. Useful for local tools and SDKs that want no key configuration. Most
OpenAI SDKs still require *some* `api_key` value to construct the client, so pass anything.

Auth is also skipped when `LOGIN_TOKEN` is left empty, but then the dashboard is open too, which
is only sane on loopback. The startup banner warns if the bind address is not loopback and auth
is effectively off.

`/v1/models` returns Traycer's own model ids (`gemini-3.8-flash`, `gpt-6-astra`,
`claude-opus-5-5`, ...). Upstream ids such as `gpt-4o` do not exist here.

Cheapest models to test with: `gemini-3.8-flash` (default), `deepseek-v4-flash`,
`gemini-4-argon`. Prefer flash tiers while validating, since credits are consumed per call.

## Capturing Traycer's traffic with mitmproxy

You do not need this to build or run the proxy: `CONTRACT.md` already has the endpoints, headers
and payload shapes, extracted from the shipped binaries and confirmed with live requests. Use a
capture only when you want to see Traycer's own request/response bytes, for example to confirm the
Responses streaming event sequence against a paid account.

Two facts make this easy, both verified in the installed host runtime:

- Traycer's inference calls come from a **Node host process** that builds an
  `EnvHttpProxyAgent` from `HTTPS_PROXY` / `NO_PROXY`, so environment variables are enough.
  It does not use the OS proxy settings.
- Electron's own traffic (renderer, updater) uses Chromium, which needs the `--proxy-server`
  switch and either a trusted CA or `--ignore-certificate-errors`.

Start the capture:

```bash
mitmweb --listen-port 8080 --web-port 8081 --set http2=false
# UI at http://127.0.0.1:8081
```

`--set http2=false` forces HTTP/1.1 upstream, which makes the SSE body readable in the UI.

Trust the CA. Electron/Node need it in three places:

```bash
# Node / undici (this is the one the inference calls use)
export NODE_EXTRA_CA_CERTS="$HOME/.mitmproxy/mitmproxy-ca-cert.pem"

# Chromium/Electron (NSS store)
mkdir -p "$HOME/.pki/nssdb"
certutil -d sql:"$HOME/.pki/nssdb" -A -t "C,," -n mitmproxy \
  -i "$HOME/.mitmproxy/mitmproxy-ca-cert.pem"

# Go binaries and curl
sudo cp "$HOME/.mitmproxy/mitmproxy-ca-cert.pem" /usr/local/share/ca-certificates/mitmproxy.crt
sudo update-ca-certificates
```

Launch the AppImage through the proxy:

```bash
MITM=http://127.0.0.1:8080

HTTPS_PROXY=$MITM HTTP_PROXY=$MITM \
NO_PROXY=127.0.0.1,localhost,::1 \
NODE_EXTRA_CA_CERTS=$HOME/.mitmproxy/mitmproxy-ca-cert.pem \
SSL_CERT_FILE=$HOME/.mitmproxy/mitmproxy-ca-cert.pem \
./traycer-desktop-linux-x86_64.AppImage \
  --proxy-server=$MITM \
  --proxy-bypass-list='127.0.0.1;localhost' \
  --ignore-certificate-errors
```

`NO_PROXY` and `--proxy-bypass-list` matter: the app talks to its own host over
`ws://127.0.0.1:44705/rpc`, and proxying loopback breaks it.

If the AppImage refuses to start because FUSE is unavailable, extract once and launch the
unpacked tree, which keeps flags identical:

```bash
./traycer-desktop-linux-x86_64.AppImage --appimage-extract >/dev/null
./squashfs-root/AppRun --proxy-server=$MITM --proxy-bypass-list='127.0.0.1;localhost' \
  --ignore-certificate-errors
```

Then send any message in Traycer and filter mitmweb for `~d traycer.ai`. The traffic you care
about:

| what | where to look |
| --- | --- |
| inference | `POST server.traycer.ai/inference/openai/responses` |
| auth headers | `authorization: Bearer …`, `x-traycer-account-context: {"type":"PERSONAL"}` |
| request body | Responses shape: `model`, `input[]`, `instructions`, `max_output_tokens`, `reasoning.effort` |
| stream | `text/event-stream`, `response.output_text.delta` / `response.completed` frames |
| catalog | `GET server.traycer.ai/api/traycer/models?nativeProviders=gemini` |
| refresh | `POST authn.traycer.ai/api/v3/auth/refresh` (every ~10 min) |

If nothing shows up in mitmweb, the host ignored the env vars. Fallback is a transparent
redirect, which does not depend on the app cooperating:

```bash
echo '127.0.0.1 server.traycer.ai authn.traycer.ai' | sudo tee -a /etc/hosts
mitmproxy --mode transparent --listen-port 443 --set http2=false
sudo iptables -t nat -A OUTPUT -p tcp --dport 443 -m owner --uid-owner "$(id -u)" \
  -j REDIRECT --to-port 443
```

Revert `/etc/hosts` and the iptables rule afterwards.

### The cheaper alternative

Everything in that table can be obtained without a proxy, in three commands:

```bash
TOK=$(python3 -c "import json;print(json.load(open('$HOME/.traycer/host/auth/credentials.json'))['token'])")

curl -s https://server.traycer.ai/api/traycer/models?nativeProviders=gemini \
  -H "authorization: Bearer $TOK" -H 'x-traycer-account-context: {"type":"PERSONAL"}'

curl -sN https://server.traycer.ai/inference/openai/responses \
  -H "authorization: Bearer $TOK" -H 'content-type: application/json' \
  -H 'x-traycer-account-context: {"type":"PERSONAL"}' \
  -d '{"model":"gemini-3.8-flash","input":"hi","stream":true}'
```

## Configuration

See `.env.example`. Every value has a working default.

| key | default | meaning |
| --- | --- | --- |
| `BIND` | `127.0.0.1` | listen address |
| `PORT` | `8787` | listen port |
| `LOGIN_TOKEN` | empty | client API key and dashboard password. Empty means open, loopback only |
| `API_AUTH` | `true` | require that key on `/v1/*`. `false` leaves the API open while the dashboard still asks for `LOGIN_TOKEN` |
| `ACCOUNTS_FILE` | `./accounts.json` | where accounts live, written `0600` |
| `TRAYCER_SERVER` | `https://server.traycer.ai` | inference and catalog |
| `TRAYCER_AUTHN` | `https://authn.traycer.ai` | token refresh |
| `MODELS_CACHE_SECONDS` | `300` | catalog cache |
| `REQUEST_LOG_SIZE` | `200` | dashboard request ring, `0` disables |
| `LOG_REQUESTS` | `true` | one stdout line per proxied request |
| `DEFAULT_ACCOUNT` | empty | fallback account name |
| `DEFAULT_MODEL` | `gemini-3.8-flash` | used when a request omits `model` |
| `UPSTREAM_TIMEOUT_SECONDS` | `600` | non-streaming call budget |

`accounts.json` holds bearer and refresh tokens. It is written `0600` and should never be
committed; `.gitignore` covers it.

## Troubleshooting

| symptom | cause |
| --- | --- |
| `402 OUT_OF_CREDITS` | the Traycer account has no credits. Checked before the model is resolved, so it is never model-specific |
| `401 UNAUTHORIZED` from upstream | access token rejected. If the account has no refresh token, re-add it |
| `refresh refused (needs_reauth)` | refresh token was burned, usually by two processes refreshing the same account. Sign in again in Traycer and re-add |
| `404 UNSUPPORTED_TRAYCER_PATH` | something asked for a Traycer path that is not enabled. Only `openai/responses` is |
| desktop app randomly signs out | the proxy and the app share one account's refresh token. Use mode 1 or mode 2, not both |
| `model not found` | use ids from `GET /v1/models`, not OpenAI's |

## Layout

```
main.go          HTTP server, /v1 handlers, dashboard API, catalog cache
store.go         .env config, account store, proactive token refresh, request log
authflow.go      OAuth device flow: authorize, poll, attempt state machine
upstream.go      Traycer HTTP client: responses, models, refresh
translate.go     chat-completions <-> Responses, including the SSE transform
verify.go        add-account token probe
web/             dashboard, embedded with go:embed
CONTRACT.md      verified upstream wire protocol
```

## Tests

```bash
go test ./...   # translation and streaming transform
```

The stream transform is also exercised end to end against a fake upstream that speaks the
Responses wire format, and against the official OpenAI SDK (v7.x) for `models.list`,
non-streaming completions, streaming completions with usage, tool-call streaming, and
Responses passthrough.
