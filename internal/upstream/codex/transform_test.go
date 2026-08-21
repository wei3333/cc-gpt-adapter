// SPDX-License-Identifier: LGPL-3.0-only

package codex

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestTransformSnapshot(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model":"claude-opus-4-1",
  "stream":false,
  "store":true,
  "max_output_tokens":4096,
  "temperature":0.2,
  "metadata":{"user_id":"secret"},
  "prompt_cache_key":"must-not-leak",
  "instructions":"  keep this instruction  ",
  "include":["file_search_call.results"],
  "reasoning":{"effort":"high","summary":"auto"},
  "input":[
    {"type":"reasoning","id":"rs_server_id","encrypted_content":"gAAAA","summary":null},
    {"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}],"call_id":"wrong-place"},
    {"type":"function_call","call_id":"call_short","name":"shell","arguments":"{}"},
    {"type":"function_call_output","call_id":"call_short","output":"ok"}
  ],
  "tools":[
    {"type":"function","name":"shell","description":"run","parameters":{"type":"object","properties":{}},"strict":false},
    {"type":"function","function":{"name":"read_file","description":"read","parameters":{"type":"object"}}},
    {"type":"function","function":null}
  ],
  "tool_choice":{"type":"function","function":{"name":"read_file"}}
}`)

	got, err := Transform(body)
	if err != nil {
		t.Fatalf("Transform() error = %v", err)
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, got, "", "  "); err != nil {
		t.Fatalf("indent transformed JSON: %v", err)
	}
	indented.WriteByte('\n')

	want, err := os.ReadFile("testdata/request.golden.json")
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if indented.String() != string(want) {
		t.Fatalf("transformed request snapshot mismatch\nwant:\n%s\ngot:\n%s", want, indented.String())
	}
}

func TestTransformPreservesAndCompactsPairedCallIDs(t *testing.T) {
	t.Parallel()
	shortID := "toolu_01A"
	longID := "toolu_" + strings.Repeat("x", 100)
	body, err := json.Marshal(map[string]any{
		"reasoning": map[string]any{"effort": "medium"},
		"input": []any{
			map[string]any{"type": "function_call", "call_id": shortID, "name": "tool"},
			map[string]any{"type": "function_call_output", "call_id": shortID, "output": "one"},
			map[string]any{"type": "function_call", "call_id": longID, "name": "tool"},
			map[string]any{"type": "function_call_output", "call_id": longID, "output": "two"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Transform(body)
	if err != nil {
		t.Fatalf("Transform() error = %v", err)
	}
	var decoded struct {
		Input []struct {
			CallID string `json:"call_id"`
		} `json:"input"`
	}
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Input[0].CallID != shortID || decoded.Input[1].CallID != shortID {
		t.Fatalf("short call ID changed: %#v", decoded.Input)
	}
	compressed := decoded.Input[2].CallID
	if compressed != decoded.Input[3].CallID {
		t.Fatalf("paired long IDs diverged: %q != %q", compressed, decoded.Input[3].CallID)
	}
	if len(compressed) != callIDMaxLength || !strings.HasPrefix(compressed, callIDPrefix) {
		t.Fatalf("compressed call ID = %q, want %d-byte fc_ ID", compressed, callIDMaxLength)
	}
	if again := compactCallIDIfNeeded(longID); again != compressed {
		t.Fatalf("compression is not deterministic: %q != %q", again, compressed)
	}
}

func TestTransformStringInputAndLegacyFunctions(t *testing.T) {
	t.Parallel()
	got, err := Transform([]byte(`{
      "input":"hello",
      "instructions":null,
      "reasoning":{"effort":"medium"},
      "functions":[{"name":"lookup","parameters":{"type":"object"}}],
      "function_call":{"name":"lookup"}
    }`))
	if err != nil {
		t.Fatalf("Transform() error = %v", err)
	}
	var request map[string]any
	if err := json.Unmarshal(got, &request); err != nil {
		t.Fatal(err)
	}
	if request["instructions"] != defaultInstructions {
		t.Fatalf("instructions = %#v", request["instructions"])
	}
	input := request["input"].([]any)
	message := input[0].(map[string]any)
	if message["role"] != "user" || message["content"] != "hello" {
		t.Fatalf("normalized input = %#v", input)
	}
	choice := request["tool_choice"].(map[string]any)
	if choice["name"] != "lookup" {
		t.Fatalf("tool_choice = %#v", choice)
	}
	if _, exists := request["functions"]; exists {
		t.Fatal("legacy functions field was retained")
	}
}

func TestTransformRejectsInvalidOrMultipleJSONValues(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`[]`, `{`, `{} {}`} {
		if _, err := Transform([]byte(body)); err == nil {
			t.Fatalf("Transform(%q) unexpectedly succeeded", body)
		}
	}
}
