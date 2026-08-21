// SPDX-License-Identifier: LGPL-3.0-only

package protocol

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestResponsesToAnthropicCompleteOutput(t *testing.T) {
	t.Parallel()

	response := &ResponsesResponse{
		ID:     "resp_full",
		Model:  "gpt-upstream",
		Status: "completed",
		Output: []ResponsesOutput{
			{
				Type:             "reasoning",
				EncryptedContent: "gAAAA-signature",
				Summary: []ResponsesSummary{
					{Type: "summary_text", Text: "First "},
					{Type: "summary_text", Text: "second"},
				},
			},
			{Type: "message", Content: []ResponsesContentPart{{Type: "output_text", Text: "Answer"}}},
			{Type: "function_call", CallID: "call_a", Name: "Read", Arguments: `{"file_path":"a.go"}`},
			{Type: "function_call", CallID: "call_b", Name: "Bash", Arguments: `{"command":"go test ./..."}`},
			{Type: "custom_tool_call", CallID: "call_patch", Name: "apply_patch", Input: "*** Begin Patch"},
		},
		Usage: &ResponsesUsage{
			InputTokens:              100,
			OutputTokens:             20,
			CacheCreationInputTokens: 5,
			InputTokensDetails:       &ResponsesInputTokensDetails{CachedTokens: 80},
		},
	}

	got, err := ResponsesToAnthropic(response, "claude-client-model")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "resp_full" || got.Model != "claude-client-model" || got.StopReason == nil || *got.StopReason != "tool_use" {
		t.Fatalf("response metadata = %#v", got)
	}
	if len(got.Content) != 5 {
		t.Fatalf("content = %#v", got.Content)
	}
	if got.Content[0].Type != "thinking" || got.Content[0].Thinking != "First second" || got.Content[0].Signature != "gAAAA-signature" {
		t.Fatalf("thinking = %#v", got.Content[0])
	}
	if got.Content[1].Type != "text" || got.Content[1].Text != "Answer" {
		t.Fatalf("text = %#v", got.Content[1])
	}
	assertToolBlock(t, got.Content[2], "call_a", "Read", `{"file_path":"a.go"}`)
	assertToolBlock(t, got.Content[3], "call_b", "Bash", `{"command":"go test ./..."}`)
	assertToolBlock(t, got.Content[4], "call_patch", "apply_patch", `{"input":"*** Begin Patch"}`)
	if got.Usage.InputTokens != 15 || got.Usage.CacheReadInputTokens != 80 || got.Usage.CacheCreationInputTokens != 5 || got.Usage.OutputTokens != 20 {
		t.Fatalf("usage = %#v", got.Usage)
	}
}

func TestResponsesToAnthropicIncompleteAndFailed(t *testing.T) {
	t.Parallel()

	partial, err := ResponsesToAnthropic(&ResponsesResponse{
		ID:                "resp_partial",
		Status:            "incomplete",
		IncompleteDetails: &ResponsesIncompleteDetails{Reason: "max_output_tokens"},
		Output:            []ResponsesOutput{{Type: "message", Content: []ResponsesContentPart{{Type: "output_text", Text: "partial"}}}},
	}, "client-model")
	if err != nil {
		t.Fatal(err)
	}
	if partial.StopReason == nil || *partial.StopReason != "max_tokens" {
		t.Fatalf("StopReason = %v", partial.StopReason)
	}

	_, err = ResponsesToAnthropic(&ResponsesResponse{
		Status: "failed",
		Error:  &ResponsesError{Code: "server_error", Message: "boom"},
	}, "client-model")
	var upstream *UpstreamResponseError
	if !errors.As(err, &upstream) || upstream.Code != "server_error" || upstream.Message != "boom" {
		t.Fatalf("error = %#v", err)
	}
}

func TestResponsesStreamTextStrictOrder(t *testing.T) {
	t.Parallel()

	converter := NewResponsesStreamConverter("claude-model")
	var output []AnthropicStreamEvent
	output = processStreamEvent(t, converter, output, ResponsesStreamEvent{
		Type:     "response.created",
		Response: &ResponsesResponse{ID: "resp_text", Model: "gpt-upstream"},
	})
	output = processStreamEvent(t, converter, output, ResponsesStreamEvent{
		Type:        "response.output_item.added",
		OutputIndex: 0,
		Item:        &ResponsesOutput{Type: "message", ID: "msg_1"},
	})
	output = processStreamEvent(t, converter, output, ResponsesStreamEvent{Type: "response.output_text.delta", OutputIndex: 0, Delta: "Hello"})
	output = processStreamEvent(t, converter, output, ResponsesStreamEvent{Type: "response.output_text.delta", OutputIndex: 0, Delta: " world"})
	output = processStreamEvent(t, converter, output, ResponsesStreamEvent{Type: "response.output_text.done", OutputIndex: 0, Text: "Hello world"})
	output = processStreamEvent(t, converter, output, ResponsesStreamEvent{
		Type:     "response.completed",
		Response: &ResponsesResponse{Status: "completed"},
		Usage:    &ResponsesUsage{InputTokens: 10, OutputTokens: 2},
	})

	want := []string{
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	}
	assertEventTypes(t, output, want)
	assertStrictAnthropicOrder(t, output)
	if output[0].Message.Model != "claude-model" || output[0].Message.StopReason != nil {
		t.Fatalf("message_start = %#v", output[0].Message)
	}
	if output[5].Delta.StopReason != "end_turn" || output[5].Usage.InputTokens != 10 || output[5].Usage.OutputTokens != 2 {
		t.Fatalf("message_delta = %#v", output[5])
	}
	if err := converter.Finalize(); err != nil {
		t.Fatal(err)
	}
}

func TestResponsesStreamReasoningParallelAndCustomTools(t *testing.T) {
	t.Parallel()

	converter := NewResponsesStreamConverter("claude-model")
	var output []AnthropicStreamEvent
	output = processStreamEvent(t, converter, output, ResponsesStreamEvent{Type: "response.created", Response: &ResponsesResponse{ID: "resp_tools"}})

	output = processStreamEvent(t, converter, output, ResponsesStreamEvent{
		Type: "response.output_item.added", OutputIndex: 0,
		Item: &ResponsesOutput{Type: "reasoning"},
	})
	output = processStreamEvent(t, converter, output, ResponsesStreamEvent{
		Type: "response.reasoning_summary_text.delta", OutputIndex: 0, Delta: "Planning",
	})
	output = processStreamEvent(t, converter, output, ResponsesStreamEvent{
		Type: "response.output_item.done", OutputIndex: 0,
		Item: &ResponsesOutput{Type: "reasoning", EncryptedContent: "gAAAA-stream", Status: "completed"},
	})

	// First parallel call supplies arguments only in the done event.
	output = processStreamEvent(t, converter, output, ResponsesStreamEvent{
		Type: "response.output_item.added", OutputIndex: 1,
		Item: &ResponsesOutput{Type: "function_call", CallID: "call_one", Name: "Read"},
	})
	output = processStreamEvent(t, converter, output, ResponsesStreamEvent{
		Type: "response.function_call_arguments.done", OutputIndex: 1, Arguments: `{"file_path":"one.go"}`,
	})

	// Second parallel call streams its JSON in multiple fragments.
	output = processStreamEvent(t, converter, output, ResponsesStreamEvent{
		Type: "response.output_item.added", OutputIndex: 2,
		Item: &ResponsesOutput{Type: "function_call", CallID: "call_two", Name: "Bash"},
	})
	output = processStreamEvent(t, converter, output, ResponsesStreamEvent{
		Type: "response.function_call_arguments.delta", OutputIndex: 2, Delta: `{"command":`,
	})
	output = processStreamEvent(t, converter, output, ResponsesStreamEvent{
		Type: "response.function_call_arguments.delta", OutputIndex: 2, Delta: `"pwd"}`,
	})
	output = processStreamEvent(t, converter, output, ResponsesStreamEvent{
		Type: "response.function_call_arguments.done", OutputIndex: 2, Arguments: `{"command":"pwd"}`,
	})

	output = processStreamEvent(t, converter, output, ResponsesStreamEvent{
		Type: "response.output_item.added", OutputIndex: 3,
		Item: &ResponsesOutput{Type: "custom_tool_call", CallID: "call_patch", Name: "apply_patch"},
	})
	output = processStreamEvent(t, converter, output, ResponsesStreamEvent{
		Type: "response.custom_tool_call_input.delta", OutputIndex: 3, Delta: "*** Begin ",
	})
	output = processStreamEvent(t, converter, output, ResponsesStreamEvent{
		Type: "response.custom_tool_call_input.delta", OutputIndex: 3, Delta: "Patch",
	})
	output = processStreamEvent(t, converter, output, ResponsesStreamEvent{
		Type: "response.custom_tool_call_input.done", OutputIndex: 3, Input: "*** Begin Patch",
	})

	output = processStreamEvent(t, converter, output, ResponsesStreamEvent{
		Type: "response.done",
		Response: &ResponsesResponse{
			Status: "completed",
			Usage: &ResponsesUsage{
				InputTokens:        50,
				OutputTokens:       12,
				InputTokensDetails: &ResponsesInputTokensDetails{CachedTokens: 20},
			},
		},
	})

	assertStrictAnthropicOrder(t, output)
	if countEvent(output, "content_block_start") != 4 || countEvent(output, "content_block_stop") != 4 {
		t.Fatalf("block lifecycle mismatch: %#v", eventTypes(output))
	}
	if !hasDelta(output, "signature_delta", "gAAAA-stream") {
		t.Fatal("reasoning signature_delta missing")
	}
	if !hasPartialJSON(output, `{"file_path":"one.go"}`) {
		t.Fatal("done-only function arguments missing")
	}
	if !hasPartialJSON(output, `{"input":"*** Begin Patch"}`) {
		t.Fatal("custom tool input wrapper missing")
	}
	final := output[len(output)-2]
	if final.Type != "message_delta" || final.Delta.StopReason != "tool_use" || final.Usage.InputTokens != 30 || final.Usage.CacheReadInputTokens != 20 {
		t.Fatalf("final event = %#v", final)
	}
}

func TestResponsesStreamTerminalOnlyAndIncompleteUsage(t *testing.T) {
	t.Parallel()

	converter := NewResponsesStreamConverter("client-model")
	events, err := converter.Process(ResponsesStreamEvent{
		Type: "response.incomplete",
		Response: &ResponsesResponse{
			ID:                "resp_terminal",
			Status:            "incomplete",
			IncompleteDetails: &ResponsesIncompleteDetails{Reason: "max_output_tokens"},
			Output: []ResponsesOutput{{
				Type:    "message",
				Content: []ResponsesContentPart{{Type: "output_text", Text: "partial"}},
			}},
		},
		Usage: &ResponsesUsage{InputTokens: 15, OutputTokens: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEventTypes(t, events, []string{
		"message_start", "content_block_start", "content_block_delta",
		"content_block_stop", "message_delta", "message_stop",
	})
	assertStrictAnthropicOrder(t, events)
	if events[len(events)-2].Delta.StopReason != "max_tokens" || events[len(events)-2].Usage.InputTokens != 15 {
		t.Fatalf("terminal event = %#v", events[len(events)-2])
	}
}

func TestResponsesStreamFailuresNeverFabricateSuccess(t *testing.T) {
	t.Parallel()

	converter := NewResponsesStreamConverter("client-model")
	var emitted []AnthropicStreamEvent
	emitted = processStreamEvent(t, converter, emitted, ResponsesStreamEvent{Type: "response.created", Response: &ResponsesResponse{ID: "resp_fail"}})
	emitted = processStreamEvent(t, converter, emitted, ResponsesStreamEvent{Type: "response.output_text.delta", Delta: "partial"})
	events, err := converter.Process(ResponsesStreamEvent{
		Type: "response.failed",
		Response: &ResponsesResponse{
			Status: "failed",
			Error:  &ResponsesError{Code: "server_error", Message: "boom"},
		},
	})
	if len(events) != 0 {
		t.Fatalf("failure emitted normal events: %#v", events)
	}
	var upstream *UpstreamResponseError
	if !errors.As(err, &upstream) || upstream.Code != "server_error" {
		t.Fatalf("error = %#v", err)
	}
	if countEvent(emitted, "message_stop") != 0 || countEvent(emitted, "message_delta") != 0 {
		t.Fatalf("success terminal leaked before failure: %#v", eventTypes(emitted))
	}
	if !errors.As(converter.Finalize(), &upstream) {
		t.Fatalf("Finalize error = %v", converter.Finalize())
	}

	bare := NewResponsesStreamConverter("")
	_, err = bare.Process(ResponsesStreamEvent{
		Type:  "error",
		Error: &ResponsesError{Code: "rate_limit_error", Message: "slow down"},
	})
	if !errors.As(err, &upstream) || upstream.Code != "rate_limit_error" {
		t.Fatalf("bare error = %#v", err)
	}
	anthropicError := NewAnthropicErrorEvent(upstream.Code, upstream.Message)
	if anthropicError.Type != "error" || anthropicError.Error.Message != "slow down" {
		t.Fatalf("Anthropic error = %#v", anthropicError)
	}
}

func TestResponsesStreamMissingTerminalReturnsError(t *testing.T) {
	t.Parallel()

	converter := NewResponsesStreamConverter("client-model")
	var emitted []AnthropicStreamEvent
	emitted = processStreamEvent(t, converter, emitted, ResponsesStreamEvent{Type: "response.created"})
	emitted = processStreamEvent(t, converter, emitted, ResponsesStreamEvent{Type: "response.output_text.delta", Delta: "interrupted"})
	if err := converter.Finalize(); !errors.Is(err, ErrMissingTerminalEvent) {
		t.Fatalf("Finalize error = %v", err)
	}
	if countEvent(emitted, "content_block_stop") != 0 || countEvent(emitted, "message_stop") != 0 {
		t.Fatalf("interrupted stream was fabricated as complete: %#v", eventTypes(emitted))
	}
}

func TestResponsesStreamRejectsInvalidDoneOnlyArguments(t *testing.T) {
	t.Parallel()

	converter := NewResponsesStreamConverter("client-model")
	if _, err := converter.Process(ResponsesStreamEvent{
		Type: "response.output_item.added", OutputIndex: 0,
		Item: &ResponsesOutput{Type: "function_call", CallID: "bad", Name: "Bad"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := converter.Process(ResponsesStreamEvent{
		Type: "response.function_call_arguments.done", OutputIndex: 0, Arguments: "not-json",
	}); err == nil || !strings.Contains(err.Error(), "JSON object") {
		t.Fatalf("error = %v", err)
	}
}

func processStreamEvent(t *testing.T, converter *ResponsesStreamConverter, output []AnthropicStreamEvent, event ResponsesStreamEvent) []AnthropicStreamEvent {
	t.Helper()
	events, err := converter.Process(event)
	if err != nil {
		t.Fatal(err)
	}
	return append(output, events...)
}

func assertToolBlock(t *testing.T, block AnthropicContentBlock, id, name, input string) {
	t.Helper()
	if block.Type != "tool_use" || block.ID != id || block.Name != name {
		t.Fatalf("tool block = %#v", block)
	}
	assertJSONEqual(t, block.Input, json.RawMessage(input))
}

func assertEventTypes(t *testing.T, got []AnthropicStreamEvent, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("event types = %#v, want %#v", eventTypes(got), want)
	}
	for index := range want {
		if got[index].Type != want[index] {
			t.Fatalf("event types = %#v, want %#v", eventTypes(got), want)
		}
	}
}

func eventTypes(events []AnthropicStreamEvent) []string {
	types := make([]string, len(events))
	for index, event := range events {
		types[index] = event.Type
	}
	return types
}

func countEvent(events []AnthropicStreamEvent, eventType string) int {
	count := 0
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

func hasDelta(events []AnthropicStreamEvent, deltaType, value string) bool {
	for _, event := range events {
		if event.Delta == nil || event.Delta.Type != deltaType {
			continue
		}
		if event.Delta.Signature == value || event.Delta.Text == value || event.Delta.Thinking == value {
			return true
		}
	}
	return false
}

func hasPartialJSON(events []AnthropicStreamEvent, value string) bool {
	for _, event := range events {
		if event.Delta != nil && event.Delta.Type == "input_json_delta" && event.Delta.PartialJSON == value {
			return true
		}
	}
	return false
}

func assertStrictAnthropicOrder(t *testing.T, events []AnthropicStreamEvent) {
	t.Helper()
	started := false
	blockOpen := false
	messageDelta := false
	stopped := false
	for index, event := range events {
		switch event.Type {
		case "message_start":
			if started || index != 0 {
				t.Fatalf("message_start out of order at %d: %#v", index, eventTypes(events))
			}
			started = true
		case "content_block_start":
			if !started || blockOpen || messageDelta || stopped {
				t.Fatalf("content_block_start out of order at %d: %#v", index, eventTypes(events))
			}
			blockOpen = true
		case "content_block_delta":
			if !blockOpen || messageDelta || stopped {
				t.Fatalf("content_block_delta out of order at %d: %#v", index, eventTypes(events))
			}
		case "content_block_stop":
			if !blockOpen || messageDelta || stopped {
				t.Fatalf("content_block_stop out of order at %d: %#v", index, eventTypes(events))
			}
			blockOpen = false
		case "message_delta":
			if !started || blockOpen || messageDelta || stopped {
				t.Fatalf("message_delta out of order at %d: %#v", index, eventTypes(events))
			}
			messageDelta = true
		case "message_stop":
			if !messageDelta || blockOpen || stopped || index != len(events)-1 {
				t.Fatalf("message_stop out of order at %d: %#v", index, eventTypes(events))
			}
			stopped = true
		default:
			t.Fatalf("unexpected normal event %q", event.Type)
		}
	}
	if !started || !messageDelta || !stopped || blockOpen {
		t.Fatalf("incomplete normal stream: %#v", eventTypes(events))
	}
}
