// SPDX-License-Identifier: LGPL-3.0-only

package protocol

import (
	"errors"
	"testing"
)

func TestResponseAccumulatorReconstructsNonStreamingResponse(t *testing.T) {
	t.Parallel()

	accumulator := NewResponseAccumulator()
	events := []ResponsesStreamEvent{
		{Type: "response.created", Response: &ResponsesResponse{ID: "resp_acc", Model: "gpt-upstream", Status: "in_progress"}},
		{Type: "response.output_item.added", OutputIndex: 0, Item: &ResponsesOutput{Type: "reasoning", ID: "rs_1"}},
		{Type: "response.reasoning_summary_text.delta", OutputIndex: 0, SummaryIndex: 0, Delta: "Think"},
		{Type: "response.reasoning_summary_text.delta", OutputIndex: 0, SummaryIndex: 0, Delta: "ing"},
		{Type: "response.output_item.done", OutputIndex: 0, Item: &ResponsesOutput{Type: "reasoning", EncryptedContent: "enc-acc", Status: "completed"}},
		{Type: "response.output_item.added", OutputIndex: 1, Item: &ResponsesOutput{Type: "message", ID: "msg_1"}},
		{Type: "response.output_text.delta", OutputIndex: 1, ContentIndex: 0, Delta: "Hello"},
		{Type: "response.output_text.delta", OutputIndex: 1, ContentIndex: 0, Delta: " world"},
		{Type: "response.output_text.done", OutputIndex: 1, ContentIndex: 0, Text: "Hello world"},
		{Type: "response.output_item.added", OutputIndex: 2, Item: &ResponsesOutput{Type: "function_call", CallID: "call_a", Name: "Read"}},
		{Type: "response.function_call_arguments.done", OutputIndex: 2, Arguments: `{"file_path":"a.go"}`},
		{Type: "response.output_item.added", OutputIndex: 3, Item: &ResponsesOutput{Type: "custom_tool_call", CallID: "call_patch", Name: "apply_patch"}},
		{Type: "response.custom_tool_call_input.delta", OutputIndex: 3, Delta: "patch-"},
		{Type: "response.custom_tool_call_input.done", OutputIndex: 3, Input: "patch-body"},
		{
			Type:     "response.completed",
			Response: &ResponsesResponse{Status: "completed"},
			Usage: &ResponsesUsage{
				InputTokens:        30,
				OutputTokens:       8,
				InputTokensDetails: &ResponsesInputTokensDetails{CachedTokens: 10},
			},
		},
	}
	for index, event := range events {
		if err := accumulator.Process(event); err != nil {
			t.Fatalf("Process event %d: %v", index, err)
		}
	}
	response, err := accumulator.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	if response.ID != "resp_acc" || response.Model != "gpt-upstream" || response.Status != "completed" {
		t.Fatalf("response metadata = %#v", response)
	}
	if len(response.Output) != 4 {
		t.Fatalf("output = %#v", response.Output)
	}
	if response.Output[0].Summary[0].Text != "Thinking" || response.Output[0].EncryptedContent != "enc-acc" {
		t.Fatalf("reasoning = %#v", response.Output[0])
	}
	if response.Output[1].Content[0].Text != "Hello world" {
		t.Fatalf("message = %#v", response.Output[1])
	}
	if response.Output[2].Arguments != `{"file_path":"a.go"}` || response.Output[3].Input != "patch-body" {
		t.Fatalf("tools = %#v", response.Output[2:])
	}
	if response.Usage == nil || response.Usage.InputTokensDetails.CachedTokens != 10 {
		t.Fatalf("usage = %#v", response.Usage)
	}

	anthropic, err := ResponsesToAnthropic(response, "client-model")
	if err != nil {
		t.Fatal(err)
	}
	if anthropic.StopReason == nil || *anthropic.StopReason != "tool_use" || anthropic.Usage.InputTokens != 20 {
		t.Fatalf("Anthropic response = %#v", anthropic)
	}
}

func TestResponseAccumulatorTerminalResponseOutputIsAuthoritative(t *testing.T) {
	t.Parallel()

	accumulator := NewResponseAccumulator()
	if err := accumulator.Process(ResponsesStreamEvent{
		Type: "response.output_text.delta", OutputIndex: 0, Delta: "partial",
	}); err != nil {
		t.Fatal(err)
	}
	if err := accumulator.Process(ResponsesStreamEvent{
		Type: "response.done",
		Response: &ResponsesResponse{
			ID:     "resp_done",
			Status: "completed",
			Output: []ResponsesOutput{{
				Type:    "message",
				Content: []ResponsesContentPart{{Type: "output_text", Text: "authoritative"}},
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	response, err := accumulator.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	if response.Output[0].Content[0].Text != "authoritative" {
		t.Fatalf("output = %#v", response.Output)
	}
}

func TestResponseAccumulatorFailureAndMissingTerminal(t *testing.T) {
	t.Parallel()

	missing := NewResponseAccumulator()
	if err := missing.Process(ResponsesStreamEvent{Type: "response.output_text.delta", Delta: "partial"}); err != nil {
		t.Fatal(err)
	}
	if _, err := missing.Finalize(); !errors.Is(err, ErrMissingTerminalEvent) {
		t.Fatalf("missing terminal error = %v", err)
	}

	failed := NewResponseAccumulator()
	err := failed.Process(ResponsesStreamEvent{
		Type:  "error",
		Error: &ResponsesError{Code: "bad_gateway", Message: "upstream disconnected"},
	})
	var upstream *UpstreamResponseError
	if !errors.As(err, &upstream) || upstream.Code != "bad_gateway" {
		t.Fatalf("Process error = %#v", err)
	}
	if _, err := failed.Finalize(); !errors.As(err, &upstream) {
		t.Fatalf("Finalize error = %#v", err)
	}
}
