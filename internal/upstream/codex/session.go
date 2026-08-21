// SPDX-License-Identifier: LGPL-3.0-only

package codex

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/Wei-Shaw/cc-gpt-adapter/internal/protocol"
)

const ClaudeCodeSessionHeader = "X-Claude-Code-Session-Id"

var claudeSessionSuffix = regexp.MustCompile(`(?i)_session_([a-f0-9-]+)$`)

// Session is the privacy-preserving identity used for one Claude Code
// conversation. Key is only used locally; ID is the deterministic UUID sent
// to Codex as session_id.
type Session struct {
	Key    string
	ID     string
	Source string
}

// ResolveSession follows the same priority as the reference gateway: Claude
// Code's explicit header, metadata.user_id, then a stable request-prefix hash.
func ResolveSession(headers http.Header, request *protocol.AnthropicRequest) Session {
	if seed := strings.TrimSpace(headers.Get(ClaudeCodeSessionHeader)); seed != "" {
		return newSession("header", seed)
	}
	if seed := metadataSessionID(request); seed != "" {
		return newSession("metadata", seed)
	}
	return newSession("request", "request-prefix:"+stableRequestSeed(request))
}

func newSession(source, seed string) Session {
	// Do not include source: Claude Code may expose the same session through a
	// header on one request and metadata on another; those must converge.
	digest := sha256.Sum256([]byte("cc-gpt-adapter:claude-session:v1:" + seed))
	key := hex.EncodeToString(digest[:])
	return Session{Key: key, ID: deterministicUUID(key), Source: source}
}

func metadataSessionID(request *protocol.AnthropicRequest) string {
	if request == nil || len(bytes.TrimSpace(request.Metadata)) == 0 {
		return ""
	}
	var metadata struct {
		UserID string `json:"user_id"`
	}
	if json.Unmarshal(request.Metadata, &metadata) != nil {
		return ""
	}
	userID := strings.TrimSpace(metadata.UserID)
	if matches := claudeSessionSuffix.FindStringSubmatch(userID); len(matches) == 2 {
		return matches[1]
	}
	if strings.HasPrefix(userID, "{") {
		var embedded struct {
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal([]byte(userID), &embedded) == nil {
			return strings.TrimSpace(embedded.SessionID)
		}
	}
	return ""
}

func stableRequestSeed(request *protocol.AnthropicRequest) string {
	if request == nil {
		return "nil"
	}
	firstUser := json.RawMessage(nil)
	for _, message := range request.Messages {
		if message.Role == "user" {
			firstUser = message.Content
			break
		}
	}
	return strings.Join([]string{
		canonicalJSON(request.System),
		canonicalValue(request.Tools),
		canonicalJSON(firstUser),
	}, "\x00")
}

func canonicalValue(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return canonicalJSON(encoded)
}

func canonicalJSON(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ""
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return string(bytes.TrimSpace(raw))
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return string(bytes.TrimSpace(raw))
	}
	return string(encoded)
}

func deterministicUUID(seed string) string {
	digest := sha256.Sum256([]byte(seed))
	digest[6] = (digest[6] & 0x0f) | 0x40
	digest[8] = (digest[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x",
		digest[0:4], digest[4:6], digest[6:8], digest[8:10], digest[10:16])
}
