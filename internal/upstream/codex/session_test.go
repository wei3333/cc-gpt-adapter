// SPDX-License-Identifier: LGPL-3.0-only

package codex

import (
	"encoding/json"
	"net/http"
	"regexp"
	"testing"

	"github.com/Wei-Shaw/cc-gpt-adapter/internal/protocol"
)

func TestResolveSessionPriorityAndConvergence(t *testing.T) {
	t.Parallel()
	request := &protocol.AnthropicRequest{
		Metadata: json.RawMessage(`{"user_id":"user_session_metadata-id"}`),
		Messages: []protocol.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"hello"`)}},
	}
	headers := http.Header{ClaudeCodeSessionHeader: []string{"abc-def"}}
	fromHeader := ResolveSession(headers, request)
	if fromHeader.Source != "header" {
		t.Fatalf("source = %q, want header", fromHeader.Source)
	}

	request.Metadata = json.RawMessage(`{"user_id":"user_session_abc-def"}`)
	fromMetadata := ResolveSession(nil, request)
	if fromMetadata.Source != "metadata" {
		t.Fatalf("source = %q, want metadata", fromMetadata.Source)
	}
	if fromHeader.ID != fromMetadata.ID || fromHeader.Key != fromMetadata.Key {
		t.Fatalf("same Claude session did not converge: %#v != %#v", fromHeader, fromMetadata)
	}

	headers.Set(ClaudeCodeSessionHeader, "preferred-id")
	preferred := ResolveSession(headers, request)
	if preferred.ID == fromMetadata.ID {
		t.Fatal("explicit header did not take priority over metadata")
	}
}

func TestResolveSessionEmbeddedMetadata(t *testing.T) {
	t.Parallel()
	request := &protocol.AnthropicRequest{
		Metadata: json.RawMessage(`{"user_id":"{\"session_id\":\"embedded-id\"}"}`),
	}
	got := ResolveSession(nil, request)
	want := newSession("metadata", "embedded-id")
	if got != want {
		t.Fatalf("ResolveSession() = %#v, want %#v", got, want)
	}
}

func TestResolveSessionFallbackUsesStablePrefix(t *testing.T) {
	t.Parallel()
	base := &protocol.AnthropicRequest{
		System: json.RawMessage(`[{"type":"text","text":"system"}]`),
		Messages: []protocol.AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`"first"`)},
			{Role: "assistant", Content: json.RawMessage(`"answer"`)},
		},
		Tools: []protocol.AnthropicTool{{Name: "shell", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	}
	first := ResolveSession(nil, base)
	base.Messages = append(base.Messages, protocol.AnthropicMessage{Role: "user", Content: json.RawMessage(`"later turn"`)})
	second := ResolveSession(nil, base)
	if first.ID != second.ID {
		t.Fatalf("appended turns changed stable session: %q != %q", first.ID, second.ID)
	}

	base.Messages[0].Content = json.RawMessage(`"different first turn"`)
	third := ResolveSession(nil, base)
	if first.ID == third.ID {
		t.Fatal("different stable prefix reused a session ID")
	}
}

func TestSessionIDIsDeterministicUUID(t *testing.T) {
	t.Parallel()
	first := newSession("header", "session-secret")
	second := newSession("header", "session-secret")
	if first != second {
		t.Fatalf("session derivation changed: %#v != %#v", first, second)
	}
	if matched := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(first.ID); !matched {
		t.Fatalf("session ID is not UUID-shaped: %q", first.ID)
	}
	if first.ID == "session-secret" || first.Key == "session-secret" {
		t.Fatal("raw Claude session escaped the one-way derivation")
	}
}
