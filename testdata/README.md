# Test fixtures

Fixtures must never contain real access tokens, refresh tokens, authorization
codes, prompts, tool output, or `x-codex-turn-state` values. Phase 3 keeps its
synthetic Codex request snapshot beside the package that consumes it at
`internal/upstream/codex/testdata/request.golden.json`.
