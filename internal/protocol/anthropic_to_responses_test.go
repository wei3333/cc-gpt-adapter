// SPDX-License-Identifier: LGPL-3.0-only

package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAnthropicToResponsesTextAndDefaults(t *testing.T) {
	t.Parallel()

	temperature := 0.7
	topP := 0.9
	got := mustConvert(t, &AnthropicRequest{
		Model:         "claude-sonnet-client-label",
		MaxTokens:     1024,
		Stream:        true,
		Temperature:   &temperature,
		TopP:          &topP,
		StopSequences: []string{"STOP"},
		Messages: []AnthropicMessage{{
			Role:    "user",
			Content: raw(`"Hello"`),
		}},
	})

	if got.Model != "claude-sonnet-client-label" {
		t.Fatalf("Model = %q", got.Model)
	}
	if !got.Stream {
		t.Fatal("Stream = false, want true")
	}
	if got.MaxOutputTokens == nil || *got.MaxOutputTokens != 1024 {
		t.Fatalf("MaxOutputTokens = %v, want 1024", got.MaxOutputTokens)
	}
	if got.Store == nil || *got.Store {
		t.Fatalf("Store = %v, want false", got.Store)
	}
	if got.ParallelToolCalls == nil || !*got.ParallelToolCalls {
		t.Fatalf("ParallelToolCalls = %v, want true", got.ParallelToolCalls)
	}
	if len(got.Include) != 1 || got.Include[0] != "reasoning.encrypted_content" {
		t.Fatalf("Include = %#v", got.Include)
	}
	if got.Reasoning == nil || got.Reasoning.Effort != "medium" || got.Reasoning.Summary != "auto" {
		t.Fatalf("Reasoning = %#v", got.Reasoning)
	}
	if got.Text == nil || got.Text.Verbosity != "medium" {
		t.Fatalf("Text = %#v", got.Text)
	}
	if len(got.Input) != 1 {
		t.Fatalf("len(Input) = %d, want 1", len(got.Input))
	}
	assertMessagePart(t, got.Input[0], "user", "input_text", "Hello")

	wire, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, unsupported := range []string{"temperature", "top_p", "stop_sequences"} {
		if strings.Contains(string(wire), unsupported) {
			t.Fatalf("unsupported field %q leaked into Responses request: %s", unsupported, wire)
		}
	}
}

func TestAnthropicToResponsesSystemBecomesDeveloper(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		system json.RawMessage
		want   []string
	}{
		{name: "string", system: raw(`"You are helpful."`), want: []string{"You are helpful."}},
		{
			name: "structured and billing header filtered",
			system: raw(`[
				{"type":"text","text":"x-anthropic-billing-header: cc_version=1;"},
				{"type":"text","text":"Project prompt"},
				{"type":"text","text":"Repository prompt","cache_control":{"type":"ephemeral"}}
			]`),
			want: []string{"Project prompt", "Repository prompt"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := mustConvert(t, &AnthropicRequest{
				System:   test.system,
				Messages: []AnthropicMessage{{Role: "user", Content: raw(`"Hi"`)}},
			})
			if len(got.Input) != 2 {
				t.Fatalf("len(Input) = %d, want 2", len(got.Input))
			}
			developer := got.Input[0]
			if developer.Type != "message" || developer.Role != "developer" {
				t.Fatalf("developer item = %#v", developer)
			}
			if len(developer.Content) != len(test.want) {
				t.Fatalf("developer parts = %#v", developer.Content)
			}
			for i, want := range test.want {
				if developer.Content[i].Type != "input_text" || developer.Content[i].Text != want {
					t.Fatalf("developer part %d = %#v, want %q", i, developer.Content[i], want)
				}
			}
		})
	}
}

func TestAnthropicToResponsesParallelToolsPreservePairing(t *testing.T) {
	t.Parallel()

	got := mustConvert(t, &AnthropicRequest{
		Messages: []AnthropicMessage{
			{Role: "user", Content: raw(`"Inspect both files"`)},
			{Role: "assistant", Content: raw(`[
				{"type":"text","text":"I will inspect both."},
				{"type":"tool_use","id":"toolu_alpha","name":"Read","input":{"file_path":"a.go"}},
				{"type":"tool_use","id":"call_beta","name":"Read","input":{"file_path":"b.go"}}
			]`)},
			{Role: "user", Content: raw(`[
				{"type":"tool_result","tool_use_id":"toolu_alpha","content":"alpha"},
				{"type":"tool_result","tool_use_id":"call_beta","content":[{"type":"text","text":"beta"}]},
				{"type":"text","text":"Compare them."}
			]`)},
		},
		Tools: []AnthropicTool{
			{Name: "Read", Description: "Read a file", InputSchema: raw(`{"type":"object","required":["file_path"]}`)},
			{Name: "NoArgs"},
		},
	})

	if len(got.Tools) != 2 {
		t.Fatalf("len(Tools) = %d, want 2", len(got.Tools))
	}
	for i, tool := range got.Tools {
		if tool.Type != "function" || tool.Strict == nil || *tool.Strict {
			t.Fatalf("tool %d = %#v", i, tool)
		}
		var schema map[string]json.RawMessage
		if err := json.Unmarshal(tool.Parameters, &schema); err != nil {
			t.Fatalf("tool %d parameters: %v", i, err)
		}
		if string(schema["type"]) != `"object"` || string(schema["properties"]) != `{}` {
			t.Fatalf("tool %d schema = %s", i, tool.Parameters)
		}
	}

	if len(got.Input) != 7 {
		t.Fatalf("len(Input) = %d, want 7: %#v", len(got.Input), got.Input)
	}
	assertMessagePart(t, got.Input[1], "assistant", "output_text", "I will inspect both.")
	assertCall(t, got.Input[2], "toolu_alpha", "Read", `{"file_path":"a.go"}`)
	assertCall(t, got.Input[3], "call_beta", "Read", `{"file_path":"b.go"}`)
	assertOutput(t, got.Input[4], "toolu_alpha", "alpha")
	assertOutput(t, got.Input[5], "call_beta", "beta")
	assertMessagePart(t, got.Input[6], "user", "input_text", "Compare them.")
}

func TestAnthropicToResponsesParallelToolSequence(t *testing.T) {
	t.Parallel()

	got := mustConvert(t, &AnthropicRequest{Messages: []AnthropicMessage{
		{Role: "assistant", Content: raw(`[
			{"type":"tool_use","id":"one","name":"A","input":{}},
			{"type":"tool_use","id":"two","name":"B","input":{}}
		]`)},
		{Role: "user", Content: raw(`[
			{"type":"tool_result","tool_use_id":"one","content":"1"},
			{"type":"tool_result","tool_use_id":"two","content":"2"},
			{"type":"text","text":"continue"}
		]`)},
	}})

	if len(got.Input) != 5 {
		t.Fatalf("len(Input) = %d, want 5: %#v", len(got.Input), got.Input)
	}
	assertCall(t, got.Input[0], "one", "A", `{}`)
	assertCall(t, got.Input[1], "two", "B", `{}`)
	assertOutput(t, got.Input[2], "one", "1")
	assertOutput(t, got.Input[3], "two", "2")
	assertMessagePart(t, got.Input[4], "user", "input_text", "continue")
}

func TestAnthropicToResponsesToolResultErrorSemantics(t *testing.T) {
	t.Parallel()

	got := mustConvert(t, &AnthropicRequest{Messages: []AnthropicMessage{{
		Role: "user",
		Content: raw(`[
			{"type":"tool_result","tool_use_id":"ok","content":"done"},
			{"type":"tool_result","tool_use_id":"failed_text","is_error":true,"content":"permission denied"},
			{"type":"tool_result","tool_use_id":"failed_empty","is_error":true,"content":""},
			{"type":"tool_result","tool_use_id":"failed_blocks","is_error":true,"content":[
				{"type":"text","text":"exit status 1"},
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":"ERRORIMAGE"}}
			]}
		]`),
	}}})

	if len(got.Input) != 5 {
		t.Fatalf("len(Input) = %d, want 5: %#v", len(got.Input), got.Input)
	}
	assertOutput(t, got.Input[0], "ok", "done")
	assertOutput(t, got.Input[1], "failed_text", "[tool_error]\npermission denied")
	assertOutput(t, got.Input[2], "failed_empty", "[tool_error]\nTool execution failed without an error message.")
	assertOutput(t, got.Input[3], "failed_blocks", "[tool_error]\nexit status 1")
	if got.Input[4].Role != "user" || len(got.Input[4].Content) != 1 || got.Input[4].Content[0].ImageURL != "data:image/png;base64,ERRORIMAGE" {
		t.Fatalf("failed tool-result image = %#v", got.Input[4])
	}
}

func TestAnthropicToResponsesThinkingRoundTripInput(t *testing.T) {
	t.Parallel()

	got := mustConvert(t, &AnthropicRequest{Messages: []AnthropicMessage{
		{Role: "user", Content: raw(`"Plan"`)},
		{Role: "assistant", Content: raw(`[
			{"type":"thinking","thinking":"private one","signature":"enc-rs-1"},
			{"type":"thinking","thinking":"private two","signature":"gAAAA-codex-ciphertext"},
			{"type":"thinking","thinking":"unsigned"},
			{"type":"text","text":"First"},
			{"type":"text","text":"Second"},
			{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"pwd"}}
		]`)},
	}})

	if len(got.Input) != 5 {
		t.Fatalf("len(Input) = %d, want 5: %#v", len(got.Input), got.Input)
	}
	if got.Input[1].Type != "reasoning" || got.Input[1].EncryptedContent != "enc-rs-1" {
		t.Fatalf("first reasoning = %#v", got.Input[1])
	}
	if got.Input[2].Type != "reasoning" || got.Input[2].EncryptedContent != "gAAAA-codex-ciphertext" {
		t.Fatalf("Codex reasoning signature not preserved: %#v", got.Input[2])
	}
	assertMessagePart(t, got.Input[3], "assistant", "output_text", "First\n\nSecond")
	assertCall(t, got.Input[4], "toolu_1", "Bash", `{"command":"pwd"}`)

	wire, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "private one") || strings.Contains(string(wire), "private two") {
		t.Fatalf("plaintext thinking leaked into request: %s", wire)
	}
}

func TestAnthropicToResponsesImages(t *testing.T) {
	t.Parallel()

	got := mustConvert(t, &AnthropicRequest{Messages: []AnthropicMessage{
		{Role: "user", Content: raw(`[
			{"type":"text","text":"What is shown?"},
			{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"JPEGDATA"}}
		]`)},
		{Role: "assistant", Content: raw(`[{"type":"tool_use","id":"read_1","name":"Read","input":{}}]`)},
		{Role: "user", Content: raw(`[
			{"type":"tool_result","tool_use_id":"read_1","content":[
				{"type":"text","text":"metadata"},
				{"type":"image","source":{"type":"base64","media_type":"","data":"PNGDATA"}}
			]}
		]`)},
	}})

	if len(got.Input) != 4 {
		t.Fatalf("len(Input) = %d, want 4: %#v", len(got.Input), got.Input)
	}
	if got.Input[0].Content[1].Type != "input_image" || got.Input[0].Content[1].ImageURL != "data:image/jpeg;base64,JPEGDATA" {
		t.Fatalf("direct image = %#v", got.Input[0].Content[1])
	}
	assertOutput(t, got.Input[2], "read_1", "metadata")
	if got.Input[3].Role != "user" || len(got.Input[3].Content) != 1 || got.Input[3].Content[0].ImageURL != "data:image/png;base64,PNGDATA" {
		t.Fatalf("tool-result image message = %#v", got.Input[3])
	}
}

func TestAnthropicToResponsesToolChoice(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "auto", raw: `{"type":"auto"}`, want: `"auto"`},
		{name: "any", raw: `{"type":"any"}`, want: `"required"`},
		{name: "none", raw: `{"type":"none"}`, want: `"none"`},
		{name: "specific", raw: `{"type":"tool","name":"Read"}`, want: `{"type":"function","name":"Read"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := mustConvert(t, &AnthropicRequest{ToolChoice: raw(test.raw)})
			assertJSONEqual(t, got.ToolChoice, raw(test.want))
		})
	}
}

func TestAnthropicToResponsesEffort(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		anthropic string
		responses string
	}{
		{anthropic: "low", responses: "low"},
		{anthropic: "medium", responses: "medium"},
		{anthropic: "high", responses: "high"},
		{anthropic: "max", responses: "xhigh"},
	} {
		t.Run(test.anthropic, func(t *testing.T) {
			t.Parallel()
			got := mustConvert(t, &AnthropicRequest{
				Thinking:     &AnthropicThinking{Type: "enabled", BudgetTokens: 4096},
				OutputConfig: &AnthropicOutputConfig{Effort: test.anthropic},
			})
			if got.Reasoning == nil || got.Reasoning.Effort != test.responses {
				t.Fatalf("Reasoning = %#v, want effort %q", got.Reasoning, test.responses)
			}
		})
	}
}

func TestAnthropicToResponsesMaxTokensFloor(t *testing.T) {
	t.Parallel()

	got := mustConvert(t, &AnthropicRequest{MaxTokens: 10})
	if got.MaxOutputTokens == nil || *got.MaxOutputTokens != minimumMaxOutputTokens {
		t.Fatalf("MaxOutputTokens = %v, want %d", got.MaxOutputTokens, minimumMaxOutputTokens)
	}
}

func TestAnthropicToResponsesRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		req  *AnthropicRequest
		want string
	}{
		{name: "nil request", req: nil, want: "nil request"},
		{name: "system", req: &AnthropicRequest{System: raw(`{"bad":true}`)}, want: "convert system"},
		{name: "role", req: &AnthropicRequest{Messages: []AnthropicMessage{{Role: "system", Content: raw(`"x"`)}}}, want: "unsupported role"},
		{name: "content", req: &AnthropicRequest{Messages: []AnthropicMessage{{Role: "user", Content: raw(`{`)}}}, want: "user content"},
		{name: "tool schema", req: &AnthropicRequest{Tools: []AnthropicTool{{Name: "bad", InputSchema: raw(`{`)}}}, want: "input_schema"},
		{name: "effort", req: &AnthropicRequest{OutputConfig: &AnthropicOutputConfig{Effort: "extreme"}}, want: "unsupported output_config.effort"},
		{name: "tool choice", req: &AnthropicRequest{ToolChoice: raw(`{"type":"tool"}`)}, want: "requires a name"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := AnthropicToResponses(test.req)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func mustConvert(t *testing.T, req *AnthropicRequest) *ResponsesRequest {
	t.Helper()
	got, err := AnthropicToResponses(req)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func raw(value string) json.RawMessage {
	return json.RawMessage(value)
}

func assertMessagePart(t *testing.T, item ResponsesInputItem, role, partType, text string) {
	t.Helper()
	if item.Type != "message" || item.Role != role || len(item.Content) != 1 {
		t.Fatalf("message item = %#v", item)
	}
	if item.Content[0].Type != partType || item.Content[0].Text != text {
		t.Fatalf("content part = %#v, want type=%q text=%q", item.Content[0], partType, text)
	}
}

func assertCall(t *testing.T, item ResponsesInputItem, callID, name, arguments string) {
	t.Helper()
	if item.Type != "function_call" || item.CallID != callID || item.Name != name || item.Arguments != arguments {
		t.Fatalf("function call = %#v", item)
	}
}

func assertOutput(t *testing.T, item ResponsesInputItem, callID, output string) {
	t.Helper()
	if item.Type != "function_call_output" || item.CallID != callID || item.Output != output {
		t.Fatalf("function output = %#v", item)
	}
}

func assertJSONEqual(t *testing.T, got, want json.RawMessage) {
	t.Helper()
	var gotValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("unmarshal got JSON: %v", err)
	}
	var wantValue any
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("unmarshal want JSON: %v", err)
	}
	gotCanonical, _ := json.Marshal(gotValue)
	wantCanonical, _ := json.Marshal(wantValue)
	if string(gotCanonical) != string(wantCanonical) {
		t.Fatalf("JSON = %s, want %s", gotCanonical, wantCanonical)
	}
}
