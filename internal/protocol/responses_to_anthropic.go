// SPDX-License-Identifier: LGPL-3.0-only

package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var ErrMissingTerminalEvent = errors.New("Responses stream ended without a terminal event")

// UpstreamResponseError represents response.failed or a top-level error event.
type UpstreamResponseError struct {
	Code    string
	Message string
}

func (err *UpstreamResponseError) Error() string {
	if err.Code == "" {
		return "upstream Responses error: " + err.Message
	}
	return fmt.Sprintf("upstream Responses error %s: %s", err.Code, err.Message)
}

// ResponsesToAnthropic converts a terminal Responses object into a
// non-streaming Anthropic Messages response.
func ResponsesToAnthropic(response *ResponsesResponse, model string) (*AnthropicResponse, error) {
	if response == nil {
		return nil, errors.New("convert Responses response: nil response")
	}
	if response.Status == "failed" || response.Error != nil {
		return nil, responseError(response.Error)
	}

	blocks, hasTool, err := responseOutputToBlocks(response.Output)
	if err != nil {
		return nil, err
	}
	if len(blocks) == 0 {
		blocks = []AnthropicContentBlock{{Type: "text", Text: ""}}
	}
	if model == "" {
		model = response.Model
	}

	stopReason := stopReasonFor(response.Status, response.IncompleteDetails, hasTool)
	result := &AnthropicResponse{
		ID:         response.ID,
		Type:       "message",
		Role:       "assistant",
		Content:    blocks,
		Model:      model,
		StopReason: stringPointer(stopReason),
	}
	result.Usage = anthropicUsage(response.Usage)
	return result, nil
}

func responseOutputToBlocks(output []ResponsesOutput) ([]AnthropicContentBlock, bool, error) {
	var blocks []AnthropicContentBlock
	var hasTool bool
	for outputIndex, item := range output {
		switch item.Type {
		case "reasoning":
			var summary strings.Builder
			for _, part := range item.Summary {
				if part.Type == "summary_text" {
					summary.WriteString(part.Text)
				}
			}
			if summary.Len() > 0 || strings.TrimSpace(item.EncryptedContent) != "" {
				blocks = append(blocks, AnthropicContentBlock{
					Type:      "thinking",
					Thinking:  summary.String(),
					Signature: item.EncryptedContent,
				})
			}
		case "message":
			for _, part := range item.Content {
				if part.Type == "output_text" && part.Text != "" {
					blocks = append(blocks, AnthropicContentBlock{Type: "text", Text: part.Text})
				}
			}
		case "function_call":
			input, err := functionToolInput(item.Arguments)
			if err != nil {
				return nil, false, fmt.Errorf("convert output %d function_call: %w", outputIndex, err)
			}
			hasTool = true
			blocks = append(blocks, AnthropicContentBlock{
				Type:  "tool_use",
				ID:    item.CallID,
				Name:  item.Name,
				Input: input,
			})
		case "custom_tool_call":
			hasTool = true
			blocks = append(blocks, AnthropicContentBlock{
				Type:  "tool_use",
				ID:    item.CallID,
				Name:  item.Name,
				Input: customToolInput(item.Input),
			})
		}
	}
	return blocks, hasTool, nil
}

func functionToolInput(arguments string) (json.RawMessage, error) {
	if strings.TrimSpace(arguments) == "" {
		return json.RawMessage(`{}`), nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(arguments), &object); err != nil || object == nil {
		return nil, errors.New("arguments must be a JSON object")
	}
	return json.RawMessage(arguments), nil
}

func customToolInput(input string) json.RawMessage {
	wire, _ := json.Marshal(struct {
		Input string `json:"input"`
	}{Input: input})
	return wire
}

func anthropicUsage(usage *ResponsesUsage) AnthropicUsage {
	if usage == nil {
		return AnthropicUsage{}
	}
	cached := 0
	cacheCreation := usage.CacheCreationInputTokens
	if usage.InputTokensDetails != nil {
		cached = max(usage.InputTokensDetails.CachedTokens, 0)
		if usage.InputTokensDetails.CacheWriteTokens > 0 {
			cacheCreation = usage.InputTokensDetails.CacheWriteTokens
		} else if usage.InputTokensDetails.CacheCreationTokens > 0 {
			cacheCreation = usage.InputTokensDetails.CacheCreationTokens
		}
	}
	cacheCreation = max(cacheCreation, 0)
	input := max(usage.InputTokens-cached-cacheCreation, 0)
	return AnthropicUsage{
		InputTokens:              input,
		OutputTokens:             max(usage.OutputTokens, 0),
		CacheCreationInputTokens: cacheCreation,
		CacheReadInputTokens:     cached,
	}
}

func stopReasonFor(status string, details *ResponsesIncompleteDetails, hasTool bool) string {
	if status == "incomplete" && details != nil && details.Reason == "max_output_tokens" {
		return "max_tokens"
	}
	if status == "completed" && hasTool {
		return "tool_use"
	}
	return "end_turn"
}

func responseError(upstream *ResponsesError) error {
	if upstream == nil {
		return &UpstreamResponseError{Code: "api_error", Message: "upstream response failed"}
	}
	return &UpstreamResponseError{Code: upstream.Code, Message: upstream.Message}
}

func stringPointer(value string) *string {
	return &value
}

// ResponsesStreamConverter converts a sequence of Responses events into a
// correctly ordered Anthropic Messages event stream.
type ResponsesStreamConverter struct {
	model string

	responseID     string
	messageStarted bool
	terminal       bool
	terminalErr    error
	hasTool        bool
	sawContent     bool
	nextBlockIndex int
	active         *streamBlock
}

type streamBlock struct {
	index       int
	outputIndex int
	kind        string
	callID      string
	toolName    string
	custom      bool
	buffer      strings.Builder
	hadDelta    bool
	signature   string
}

// NewResponsesStreamConverter creates a converter. model should be the
// original Anthropic model label returned to Claude Code; an empty value uses
// the upstream response model.
func NewResponsesStreamConverter(model string) *ResponsesStreamConverter {
	return &ResponsesStreamConverter{model: model}
}

// Process converts one Responses event. Failed/error terminal events are
// returned as errors and never produce a successful Anthropic message_stop.
func (converter *ResponsesStreamConverter) Process(event ResponsesStreamEvent) ([]AnthropicStreamEvent, error) {
	if converter.terminal {
		return nil, nil
	}
	converter.captureMetadata(event.Response)

	switch event.Type {
	case "error":
		code := event.Code
		message := event.Message
		if event.Error != nil {
			code = nonEmpty(event.Error.Code, code)
			message = nonEmpty(event.Error.Message, message)
		}
		return nil, converter.fail(&UpstreamResponseError{Code: code, Message: nonEmpty(message, "upstream error")})
	case "response.failed":
		upstream := event.Error
		if event.Response != nil {
			if event.Response.Error != nil {
				upstream = event.Response.Error
			}
		}
		return nil, converter.fail(responseError(upstream))
	case "response.created":
		return converter.ensureMessageStart(), nil
	case "response.output_item.added":
		return converter.handleItemAdded(event)
	case "response.output_text.delta":
		return converter.handleTextDelta(event)
	case "response.output_text.done":
		return converter.handleTextDone(event)
	case "response.function_call_arguments.delta":
		return converter.handleToolDelta(event, false)
	case "response.custom_tool_call_input.delta":
		return converter.handleToolDelta(event, true)
	case "response.function_call_arguments.done":
		return converter.handleToolDone(event, false)
	case "response.custom_tool_call_input.done":
		return converter.handleToolDone(event, true)
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		return converter.handleReasoningDelta(event)
	case "response.reasoning_summary_text.done", "response.reasoning_text.done":
		return converter.handleReasoningDone(event)
	case "response.output_item.done":
		return converter.handleItemDone(event)
	case "response.completed", "response.done", "response.incomplete":
		return converter.handleTerminal(event)
	default:
		return nil, nil
	}
}

// Finalize verifies that the upstream supplied a real terminal event. It never
// fabricates message_delta/message_stop on EOF.
func (converter *ResponsesStreamConverter) Finalize() error {
	if converter.terminal {
		return converter.terminalErr
	}
	return ErrMissingTerminalEvent
}

// Started reports whether message_start has already been emitted.
func (converter *ResponsesStreamConverter) Started() bool {
	return converter.messageStarted
}

func (converter *ResponsesStreamConverter) captureMetadata(response *ResponsesResponse) {
	if response == nil {
		return
	}
	if response.ID != "" {
		converter.responseID = response.ID
	}
	if converter.model == "" && response.Model != "" {
		converter.model = response.Model
	}
}

func (converter *ResponsesStreamConverter) ensureMessageStart() []AnthropicStreamEvent {
	if converter.messageStarted {
		return nil
	}
	converter.messageStarted = true
	return []AnthropicStreamEvent{{
		Type: "message_start",
		Message: &AnthropicResponse{
			ID:         converter.responseID,
			Type:       "message",
			Role:       "assistant",
			Content:    []AnthropicContentBlock{},
			Model:      converter.model,
			StopReason: nil,
			Usage:      AnthropicUsage{},
		},
	}}
}

func (converter *ResponsesStreamConverter) handleItemAdded(event ResponsesStreamEvent) ([]AnthropicStreamEvent, error) {
	if event.Item == nil {
		return nil, nil
	}
	events := converter.ensureMessageStart()
	switch event.Item.Type {
	case "function_call", "custom_tool_call":
		events = append(events, converter.closeActive()...)
		converter.hasTool = true
		converter.sawContent = true
		converter.active = &streamBlock{
			index:       converter.nextBlockIndex,
			outputIndex: event.OutputIndex,
			kind:        "tool_use",
			callID:      event.Item.CallID,
			toolName:    event.Item.Name,
			custom:      event.Item.Type == "custom_tool_call",
		}
		index := converter.active.index
		events = append(events, AnthropicStreamEvent{
			Type:  "content_block_start",
			Index: &index,
			ContentBlock: &AnthropicContentBlock{
				Type:  "tool_use",
				ID:    event.Item.CallID,
				Name:  event.Item.Name,
				Input: json.RawMessage(`{}`),
			},
		})
	case "reasoning":
		events = append(events, converter.closeActive()...)
		converter.sawContent = true
		converter.active = &streamBlock{
			index:       converter.nextBlockIndex,
			outputIndex: event.OutputIndex,
			kind:        "thinking",
			signature:   event.Item.EncryptedContent,
		}
		index := converter.active.index
		events = append(events, AnthropicStreamEvent{
			Type:         "content_block_start",
			Index:        &index,
			ContentBlock: &AnthropicContentBlock{Type: "thinking", Thinking: ""},
		})
	}
	return events, nil
}

func (converter *ResponsesStreamConverter) handleTextDelta(event ResponsesStreamEvent) ([]AnthropicStreamEvent, error) {
	if event.Delta == "" {
		return nil, nil
	}
	events := converter.ensureMessageStart()
	if converter.active == nil || converter.active.kind != "text" || converter.active.outputIndex != event.OutputIndex {
		events = append(events, converter.closeActive()...)
		converter.active = &streamBlock{
			index:       converter.nextBlockIndex,
			outputIndex: event.OutputIndex,
			kind:        "text",
		}
		index := converter.active.index
		events = append(events, AnthropicStreamEvent{
			Type:         "content_block_start",
			Index:        &index,
			ContentBlock: &AnthropicContentBlock{Type: "text", Text: ""},
		})
	}
	converter.active.hadDelta = true
	converter.sawContent = true
	index := converter.active.index
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_delta",
		Index: &index,
		Delta: &AnthropicDelta{Type: "text_delta", Text: event.Delta},
	})
	return events, nil
}

func (converter *ResponsesStreamConverter) handleTextDone(event ResponsesStreamEvent) ([]AnthropicStreamEvent, error) {
	var events []AnthropicStreamEvent
	if converter.active == nil && event.Text != "" {
		var err error
		events, err = converter.handleTextDelta(ResponsesStreamEvent{
			Type:        "response.output_text.delta",
			OutputIndex: event.OutputIndex,
			Delta:       event.Text,
		})
		if err != nil {
			return nil, err
		}
	} else if converter.active != nil && converter.active.kind == "text" && !converter.active.hadDelta && event.Text != "" {
		index := converter.active.index
		converter.active.hadDelta = true
		events = append(events, AnthropicStreamEvent{
			Type:  "content_block_delta",
			Index: &index,
			Delta: &AnthropicDelta{Type: "text_delta", Text: event.Text},
		})
	}
	if converter.active != nil && converter.active.kind == "text" {
		events = append(events, converter.closeActive()...)
	}
	return events, nil
}

func (converter *ResponsesStreamConverter) handleToolDelta(event ResponsesStreamEvent, custom bool) ([]AnthropicStreamEvent, error) {
	if event.Delta == "" {
		return nil, nil
	}
	if converter.active == nil || converter.active.kind != "tool_use" || converter.active.outputIndex != event.OutputIndex || converter.active.custom != custom {
		return nil, converter.fail(errors.New("Responses tool delta does not match the active output item"))
	}
	converter.active.buffer.WriteString(event.Delta)
	if custom {
		return nil, nil
	}
	converter.active.hadDelta = true
	index := converter.active.index
	return []AnthropicStreamEvent{{
		Type:  "content_block_delta",
		Index: &index,
		Delta: &AnthropicDelta{Type: "input_json_delta", PartialJSON: event.Delta},
	}}, nil
}

func (converter *ResponsesStreamConverter) handleToolDone(event ResponsesStreamEvent, custom bool) ([]AnthropicStreamEvent, error) {
	if converter.active == nil || converter.active.kind != "tool_use" || converter.active.outputIndex != event.OutputIndex || converter.active.custom != custom {
		return nil, converter.fail(errors.New("Responses tool done event does not match the active output item"))
	}
	var events []AnthropicStreamEvent
	index := converter.active.index
	if custom {
		input := event.Input
		if input == "" {
			input = converter.active.buffer.String()
		}
		events = append(events, AnthropicStreamEvent{
			Type:  "content_block_delta",
			Index: &index,
			Delta: &AnthropicDelta{Type: "input_json_delta", PartialJSON: string(customToolInput(input))},
		})
	} else {
		arguments := event.Arguments
		if arguments == "" {
			arguments = converter.active.buffer.String()
		}
		if _, err := functionToolInput(arguments); err != nil {
			return nil, converter.fail(fmt.Errorf("complete function call %q: %w", converter.active.toolName, err))
		}
		if !converter.active.hadDelta {
			if arguments == "" {
				arguments = "{}"
			}
			events = append(events, AnthropicStreamEvent{
				Type:  "content_block_delta",
				Index: &index,
				Delta: &AnthropicDelta{Type: "input_json_delta", PartialJSON: arguments},
			})
		}
	}
	events = append(events, converter.closeActive()...)
	return events, nil
}

func (converter *ResponsesStreamConverter) handleReasoningDelta(event ResponsesStreamEvent) ([]AnthropicStreamEvent, error) {
	if event.Delta == "" {
		return nil, nil
	}
	events := converter.ensureMessageStart()
	if converter.active == nil || converter.active.kind != "thinking" || converter.active.outputIndex != event.OutputIndex {
		events = append(events, converter.closeActive()...)
		converter.active = &streamBlock{
			index:       converter.nextBlockIndex,
			outputIndex: event.OutputIndex,
			kind:        "thinking",
		}
		index := converter.active.index
		events = append(events, AnthropicStreamEvent{
			Type:         "content_block_start",
			Index:        &index,
			ContentBlock: &AnthropicContentBlock{Type: "thinking", Thinking: ""},
		})
	}
	converter.active.hadDelta = true
	converter.sawContent = true
	index := converter.active.index
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_delta",
		Index: &index,
		Delta: &AnthropicDelta{Type: "thinking_delta", Thinking: event.Delta},
	})
	return events, nil
}

func (converter *ResponsesStreamConverter) handleReasoningDone(event ResponsesStreamEvent) ([]AnthropicStreamEvent, error) {
	if converter.active != nil && converter.active.kind == "thinking" && !converter.active.hadDelta && event.Text != "" {
		index := converter.active.index
		converter.active.hadDelta = true
		return []AnthropicStreamEvent{{
			Type:  "content_block_delta",
			Index: &index,
			Delta: &AnthropicDelta{Type: "thinking_delta", Thinking: event.Text},
		}}, nil
	}
	return nil, nil
}

func (converter *ResponsesStreamConverter) handleItemDone(event ResponsesStreamEvent) ([]AnthropicStreamEvent, error) {
	if event.Item == nil || converter.active == nil {
		return nil, nil
	}
	if converter.active.outputIndex != event.OutputIndex {
		return nil, converter.fail(errors.New("Responses output_item.done does not match the active output item"))
	}
	switch event.Item.Type {
	case "reasoning":
		if event.Item.EncryptedContent != "" {
			converter.active.signature = event.Item.EncryptedContent
		}
		return converter.closeActive(), nil
	case "function_call":
		return converter.handleToolDone(ResponsesStreamEvent{
			OutputIndex: event.OutputIndex,
			Arguments:   event.Item.Arguments,
		}, false)
	case "custom_tool_call":
		return converter.handleToolDone(ResponsesStreamEvent{
			OutputIndex: event.OutputIndex,
			Input:       event.Item.Input,
		}, true)
	case "message":
		if converter.active.kind == "text" {
			return converter.closeActive(), nil
		}
	}
	return nil, nil
}

func (converter *ResponsesStreamConverter) handleTerminal(event ResponsesStreamEvent) ([]AnthropicStreamEvent, error) {
	if event.Response != nil && (event.Response.Status == "failed" || event.Response.Error != nil) {
		return nil, converter.fail(responseError(event.Response.Error))
	}

	events := converter.ensureMessageStart()
	if !converter.sawContent && event.Response != nil && len(event.Response.Output) > 0 {
		full, hasTool, err := streamFullOutput(event.Response.Output, converter.nextBlockIndex)
		if err != nil {
			return nil, converter.fail(err)
		}
		events = append(events, full...)
		converter.hasTool = converter.hasTool || hasTool
		for _, item := range full {
			if item.Type == "content_block_stop" {
				converter.nextBlockIndex++
			}
		}
	} else {
		events = append(events, converter.closeActive()...)
	}

	status := "completed"
	var details *ResponsesIncompleteDetails
	var usage *ResponsesUsage
	if event.Type == "response.incomplete" {
		status = "incomplete"
	}
	if event.Response != nil {
		if event.Response.Status != "" {
			status = event.Response.Status
		}
		details = event.Response.IncompleteDetails
		usage = event.Response.Usage
	}
	if usage == nil {
		usage = event.Usage
	}
	anthropicTokens := anthropicUsage(usage)
	stopReason := stopReasonFor(status, details, converter.hasTool)
	events = append(events,
		AnthropicStreamEvent{
			Type:  "message_delta",
			Delta: &AnthropicDelta{StopReason: stopReason},
			Usage: &anthropicTokens,
		},
		AnthropicStreamEvent{Type: "message_stop"},
	)
	converter.terminal = true
	return events, nil
}

func (converter *ResponsesStreamConverter) closeActive() []AnthropicStreamEvent {
	if converter.active == nil {
		return nil
	}
	block := converter.active
	converter.active = nil
	converter.nextBlockIndex++
	var events []AnthropicStreamEvent
	if block.kind == "thinking" && strings.TrimSpace(block.signature) != "" {
		index := block.index
		events = append(events, AnthropicStreamEvent{
			Type:  "content_block_delta",
			Index: &index,
			Delta: &AnthropicDelta{Type: "signature_delta", Signature: block.signature},
		})
	}
	index := block.index
	events = append(events, AnthropicStreamEvent{Type: "content_block_stop", Index: &index})
	return events
}

func (converter *ResponsesStreamConverter) fail(err error) error {
	converter.terminal = true
	converter.terminalErr = err
	return err
}

func streamFullOutput(output []ResponsesOutput, startIndex int) ([]AnthropicStreamEvent, bool, error) {
	blocks, hasTool, err := responseOutputToBlocks(output)
	if err != nil {
		return nil, false, err
	}
	var events []AnthropicStreamEvent
	for offset, block := range blocks {
		index := startIndex + offset
		start := block
		switch start.Type {
		case "text":
			start.Text = ""
		case "thinking":
			start.Thinking = ""
			start.Signature = ""
		case "tool_use":
			start.Input = json.RawMessage(`{}`)
		}
		events = append(events, AnthropicStreamEvent{Type: "content_block_start", Index: &index, ContentBlock: &start})
		switch block.Type {
		case "text":
			if block.Text != "" {
				events = append(events, AnthropicStreamEvent{Type: "content_block_delta", Index: &index, Delta: &AnthropicDelta{Type: "text_delta", Text: block.Text}})
			}
		case "thinking":
			if block.Thinking != "" {
				events = append(events, AnthropicStreamEvent{Type: "content_block_delta", Index: &index, Delta: &AnthropicDelta{Type: "thinking_delta", Thinking: block.Thinking}})
			}
			if strings.TrimSpace(block.Signature) != "" {
				events = append(events, AnthropicStreamEvent{Type: "content_block_delta", Index: &index, Delta: &AnthropicDelta{Type: "signature_delta", Signature: block.Signature}})
			}
		case "tool_use":
			events = append(events, AnthropicStreamEvent{Type: "content_block_delta", Index: &index, Delta: &AnthropicDelta{Type: "input_json_delta", PartialJSON: string(block.Input)}})
		}
		events = append(events, AnthropicStreamEvent{Type: "content_block_stop", Index: &index})
	}
	return events, hasTool, nil
}

func nonEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
