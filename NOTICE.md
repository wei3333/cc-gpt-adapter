# Source provenance and modification notice

## sub2api reference baseline

- Project: `Wei-Shaw/sub2api`
- Source: https://github.com/Wei-Shaw/sub2api
- Pinned reference commit: `1b5dc676a9d35532ac2d88dbbe0ee2638b2ab05f`
- Baseline recorded: 2026-08-20
- Upstream license: GNU Lesser General Public License v3.0
- Local reference tree: `sub2api/` (an independent repository ignored by the
  root repository)

The complete LGPL-3.0 license text is included in `LICENSES/LGPL-3.0.txt`.

At phase 0, no functional source file was copied from `sub2api`. The reference
implementation informed the proposed package boundaries, default
configuration, licensing choice, and provenance process.

## Phase 1 adapted work

The following files at the pinned commit were used as the functional and test
baseline:

- `backend/internal/pkg/apicompat/types.go`
- `backend/internal/pkg/apicompat/anthropic_to_responses.go`
- `backend/internal/pkg/apicompat/anthropic_responses_test.go`

The adapted implementation is in `internal/protocol/types.go` and
`internal/protocol/anthropic_to_responses.go`, with tests in
`internal/protocol/anthropic_to_responses_test.go` and
`internal/protocol/types_test.go`.

Modifications for this adapter include removing response/SSE, Chat
Completions, Grok, server-tool, database, service, and gateway concerns;
replacing third-party test assertions with the Go standard library; using
typed Responses input slices instead of intermediate raw JSON; rejecting
malformed roles, schemas, efforts, and tool choices early; unconditionally
dropping unsupported sampling and stop fields; and preserving every non-empty
thinking signature, including Codex-style `gAAAA` ciphertext.

## Phase 2 adapted work

The response-side implementation and tests use these files at the pinned
commit as their behavioral baseline:

- `backend/internal/pkg/apicompat/types.go`
- `backend/internal/pkg/apicompat/responses_to_anthropic.go`
- `backend/internal/pkg/apicompat/responses_stream_event_wire.go`
- `backend/internal/pkg/apicompat/anthropic_responses_test.go`
- `backend/internal/pkg/apicompat/responses_to_chatcompletions.go`
- `backend/internal/service/openai_gateway_response_handling.go`
- `backend/internal/service/openai_gateway_messages.go`

The phase-2 implementation is in `internal/protocol/sse.go`,
`internal/protocol/responses_to_anthropic.go`, and
`internal/protocol/accumulator.go`; response wire types extend
`internal/protocol/types.go`. Corresponding standard-library-only tests are in
the same package.

Modifications include replacing Scanner/line-oriented service code with a
standalone 16 MiB-bounded SSE frame decoder; removing Gin, gateway, billing,
Grok, web-search, and Read-tool special cases; enforcing valid function-call
JSON; wrapping custom/freeform input as `{"input":"..."}`; supporting
terminal-only full Responses objects; and treating `response.failed`, bare
errors, malformed tool completion, and missing terminal events as failures.
Unlike the reference converter, abnormal EOF and failed responses never
fabricate a successful Anthropic `message_stop`.

Public OpenAI Responses API documentation may be used as a separate protocol
reference. Compatibility with the ChatGPT Codex subscription backend remains
an internal, change-prone integration and is not represented as public API
stability.

## Phase 3 adapted work

The upstream request transform, identity, session, and turn-state behavior use
these files at the pinned commit as their behavioral baseline:

- `backend/internal/service/openai_codex_transform.go`
- `backend/internal/service/openai_codex_identity.go`
- `backend/internal/service/openai_gateway_service.go`
- `backend/internal/service/openai_gateway_grok_cache.go`
- `backend/internal/service/gateway_claude_oauth_body.go`
- `backend/internal/service/openai_codex_turn_state.go`
- `backend/internal/service/openai_gateway_messages.go`
- `backend/internal/pkg/openai/request.go`

The phase-3 implementation is isolated in `internal/upstream/codex`. It keeps
only the single-account Messages bridge behavior: one fixed model, the Codex
OAuth request-body constraints, one coherent Codex TUI identity, deterministic
Claude session derivation, and an in-memory per-session turn-state TTL map.

Modifications include replacing gjson/sjson, Gin, UUID, xxhash, Redis, account
scheduling, model routing, compact, WebSocket, image-generation, Grok, and
failover branches with standard-library-only code; requiring an injectable
HTTP endpoint for tests; hashing raw Claude session identifiers before using
them locally or upstream; preserving call IDs through 64 bytes and applying
the reference implementation's versioned SHA-256 compression above that
limit; and removing `conversation_id` by default. The request snapshot contains
only synthetic credentials, prompts, and turn-state values.

## Phase 4 adapted work

The OAuth constants, PKCE flow, token forms, ID Token account extraction, and
state validation use these files at the pinned commit as their behavioral
baseline:

- `backend/internal/pkg/openai/oauth.go`
- `backend/internal/pkg/openai/oauth_test.go`
- `backend/internal/repository/openai_oauth_service.go`
- `backend/internal/service/openai_oauth_service.go`
- `backend/internal/service/openai_oauth_service_state_test.go`
- `backend/internal/service/openai_codex_identity.go`

The adapted implementation is in `internal/auth`, with CLI wiring in
`cmd/cc-gpt-adapter/main.go` and credential-path resolution in
`internal/config/config.go`.

Modifications include removing proxy, database, account, privacy, plan,
organization, admin-session, and background cleanup concerns; replacing the
server-side OAuth session map with one short-lived CLI callback; never storing
the ID Token; adding a synced `0600` temporary-file plus atomic-rename store;
preserving one random local secret across re-login and token rotation; and
deduplicating expiry and rejected-token refreshes with an in-process mutex.
Mock HTTP servers use only synthetic token values and never connect to the
real authorization or token endpoints.

## Phase 5 adapted work

The loopback handler, Messages orchestration, streaming behavior, request-body
limit, and local token-count shape use these files at the pinned commit as
behavioral references:

- `backend/internal/handler/gateway_handler.go`
- `backend/internal/server/middleware/request_body_limit.go`
- `backend/internal/server/routes/gateway.go`
- `backend/internal/service/openai_gateway_messages.go`
- `backend/internal/service/openai_gateway_response_handling.go`
- `backend/internal/service/openai_gateway_count_tokens.go`
- `backend/internal/service/openai_gateway_count_tokens_test.go`

The adapted implementation is in `internal/server`, with runtime wiring in
`cmd/cc-gpt-adapter/main.go`. It composes the phase-1 through phase-4 packages
without importing Gin, Redis, database, billing, account-pool, routing,
failover, or logging infrastructure.

Modifications include a standard-library `ServeMux`; strict IPv4 loopback
binding; one local secret; bounded JSON bodies and SSE events; activity-based
SSE idle cancellation; JSON errors before a response starts and Anthropic SSE
errors after it starts; exactly one refresh retry after an upstream 401; and a
dependency-free local token estimate rather than a provider/tokenizer request.
Integration tests connect only to synthetic `httptest` endpoints and cover
streaming, buffered responses, refresh, cancellation, failed events, and
multi-turn parallel tools.
