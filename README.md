# cc-gpt-adapter

`cc-gpt-adapter` is a minimal, local, single-account adapter that
accepts Anthropic Messages requests from Claude Code and forwards translated
Responses requests to the ChatGPT Codex subscription backend.

## Current status

Phase 5 completes the loopback HTTP server and full mock request path. The project now has
Anthropic-to-Responses request translation, Responses-to-Anthropic response
translation, SSE decoding/accumulation, mandatory Codex OAuth request-body
normalization, centralized Codex identity headers, deterministic Claude Code
sessions, process-local turn-state continuation, Codex PKCE login, an atomic
local credential store, concurrency-safe token refresh, and an
Anthropic-compatible local server.

The server exposes only `POST /v1/messages`,
`POST /v1/messages/count_tokens`, and `GET /healthz`, and binds only to
`127.0.0.1:8787`. Messages may be streaming or buffered. The local secret is
accepted through either `Authorization: Bearer` or `x-api-key`; upstream 401
responses force one token refresh and exactly one retry. Request-size limits,
SSE idle timeouts, client cancellation, and graceful process shutdown are
enforced.

The request translator currently supports:

- string and structured system prompts, emitted as developer messages;
- user and assistant text plus inline base64 images;
- function tools, `tool_use`, `tool_result`, parallel calls, and `tool_choice`;
- thinking signatures replayed as `reasoning.encrypted_content`;
- `output_config.effort`, including `max` to `xhigh`;
- the basic Anthropic and Responses cache-usage field shapes.

The first adapter version does not enforce `max_tokens` at the Codex upstream.
The generic translator records `max_output_tokens`, and the Codex transform
removes it before sending. `stop_sequences`, `temperature`, `top_p`, and
Anthropic fast mode are unsupported and are not forwarded or locally emulated.

Responses output support now includes text, reasoning summary/signature,
function calls, custom/freeform tool calls, parallel tool-call lifecycles,
`response.completed`, `response.done`, `response.incomplete`, nested or
top-level usage, and cache token fields. Custom/freeform tool text is exposed
to Anthropic as the valid object `{"input":"..."}`.

`response.failed`, top-level `error`, malformed terminal tool arguments, and
EOF without a terminal Responses event are errors. They never synthesize a
successful Anthropic `message_delta` or `message_stop`.

Every request sent by the upstream client is forced to model `gpt-5.6-sol`,
`stream=true`, and `store=false`. Unsupported subscription-backend fields are
removed; reasoning ciphertext, tool-call pairing, normalized tools, and valid
instructions are retained. Call IDs up to 64 bytes remain unchanged and
longer IDs are deterministically compressed on both sides of a call/result
pair.

The upstream request uses one coherent Codex TUI identity and a deterministic
`session_id` derived from the Claude Code session header, metadata, or stable
request prefix. It does not synthesize `conversation_id`. The opaque
`x-codex-turn-state` response header is cached in memory for 30 minutes and is
only replayed within the same derived session. Phase-3 tests use an injected
`httptest.Server`; they do not connect to the real subscription backend.

## Authentication and runtime commands

The CLI provides:

```sh
cc-gpt-adapter login
cc-gpt-adapter serve
cc-gpt-adapter status
cc-gpt-adapter logout
```

`login` starts a temporary `localhost:1455/auth/callback` listener, creates a
fresh state and S256 PKCE verifier, and opens the authorization URL in the
default browser. If the browser command fails, the URL is printed for manual
opening. The callback is one-shot and expires after five minutes.

Credentials are stored in the operating system's per-user configuration
directory under `cc-gpt-adapter/credentials.json`. Set
`CC_GPT_ADAPTER_CONFIG_DIR` to override that directory. The directory is
forced to mode `0700`; credentials are written using a synced temporary file,
mode `0600`, and atomic rename. `status` never prints tokens or the local
secret, and `logout` removes only this local credentials file.

Access tokens are refreshed three minutes before expiry. Refresh-token
rotation is saved atomically; if the provider omits a replacement refresh
token, the previous one is retained. Concurrent expiry or 401 refreshes are
deduplicated in-process. ID Tokens are not persisted: their unsigned payload
is decoded only to obtain `chatgpt_account_id`, never for local authorization.

The OAuth endpoints and the ChatGPT Codex subscription backend are internal
compatibility surfaces. Phase-5 tests use mock OAuth, callback, Codex, and
adapter servers only; no real browser login or subscription request has been
performed by the test suite.

`count_tokens` is a deterministic local approximation. It has no network
side effects and deliberately does not include a tokenizer dependency; it
should not be treated as provider billing data.


## Development

Go is installed locally under `/usr/local/go/bin`. From the repository root:

```sh
PATH=/usr/local/go/bin:$PATH go test ./...
PATH=/usr/local/go/bin:$PATH go test -race ./...
PATH=/usr/local/go/bin:$PATH go vet ./...
PATH=/usr/local/go/bin:$PATH go build ./cmd/cc-gpt-adapter
```

## Licensing and provenance

This project is licensed under LGPL-3.0-only. The complete license text is in
`LICENSES/LGPL-3.0.txt`; source-reference details are in `NOTICE.md`.

The Codex subscription backend is an internal compatibility surface, not a
public OpenAI API. Its behavior can change without notice and must be isolated
behind the upstream package.
