// SPDX-License-Identifier: LGPL-3.0-only

package protocol

import (
	"errors"
	"sort"
)

// ResponseAccumulator reconstructs a terminal ResponsesResponse from its SSE
// lifecycle. It is used when the Anthropic caller requested stream=false.
type ResponseAccumulator struct {
	response ResponsesResponse
	items    map[int]*ResponsesOutput
	terminal bool
	err      error
}

// NewResponseAccumulator returns an empty non-streaming accumulator.
func NewResponseAccumulator() *ResponseAccumulator {
	return &ResponseAccumulator{items: make(map[int]*ResponsesOutput)}
}

// Process adds one Responses event to the accumulated response.
func (accumulator *ResponseAccumulator) Process(event ResponsesStreamEvent) error {
	if accumulator.terminal {
		return accumulator.err
	}

	switch event.Type {
	case "error":
		code := event.Code
		message := event.Message
		if event.Error != nil {
			code = nonEmpty(event.Error.Code, code)
			message = nonEmpty(event.Error.Message, message)
		}
		return accumulator.fail(&UpstreamResponseError{Code: code, Message: nonEmpty(message, "upstream error")})
	case "response.failed":
		upstream := event.Error
		if event.Response != nil {
			if event.Response.Error != nil {
				upstream = event.Response.Error
			}
		}
		return accumulator.fail(responseError(upstream))
	case "response.created":
		accumulator.captureResponse(event.Response, false)
	case "response.output_item.added":
		if event.Item != nil {
			item := *event.Item
			accumulator.items[event.OutputIndex] = &item
		}
	case "response.output_text.delta":
		item := accumulator.item(event.OutputIndex, "message")
		part := ensureContentPart(item, event.ContentIndex, "output_text")
		part.Text += event.Delta
	case "response.output_text.done":
		item := accumulator.item(event.OutputIndex, "message")
		part := ensureContentPart(item, event.ContentIndex, "output_text")
		if event.Text != "" {
			part.Text = event.Text
		}
	case "response.function_call_arguments.delta":
		item := accumulator.item(event.OutputIndex, "function_call")
		item.Arguments += event.Delta
		captureCallMetadata(item, event)
	case "response.function_call_arguments.done":
		item := accumulator.item(event.OutputIndex, "function_call")
		if event.Arguments != "" {
			item.Arguments = event.Arguments
		}
		captureCallMetadata(item, event)
	case "response.custom_tool_call_input.delta":
		item := accumulator.item(event.OutputIndex, "custom_tool_call")
		item.Input += event.Delta
		captureCallMetadata(item, event)
	case "response.custom_tool_call_input.done":
		item := accumulator.item(event.OutputIndex, "custom_tool_call")
		if event.Input != "" {
			item.Input = event.Input
		}
		captureCallMetadata(item, event)
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		item := accumulator.item(event.OutputIndex, "reasoning")
		part := ensureSummaryPart(item, event.SummaryIndex)
		part.Text += event.Delta
	case "response.reasoning_summary_text.done", "response.reasoning_text.done":
		item := accumulator.item(event.OutputIndex, "reasoning")
		part := ensureSummaryPart(item, event.SummaryIndex)
		if event.Text != "" {
			part.Text = event.Text
		}
	case "response.output_item.done":
		if event.Item != nil {
			mergeOutput(accumulator.item(event.OutputIndex, event.Item.Type), event.Item)
		}
	case "response.completed", "response.done", "response.incomplete":
		if event.Response != nil && (event.Response.Status == "failed" || event.Response.Error != nil) {
			return accumulator.fail(responseError(event.Response.Error))
		}
		accumulator.captureResponse(event.Response, true)
		if accumulator.response.Status == "" {
			if event.Type == "response.incomplete" {
				accumulator.response.Status = "incomplete"
			} else {
				accumulator.response.Status = "completed"
			}
		}
		if accumulator.response.Usage == nil {
			accumulator.response.Usage = event.Usage
		}
		if len(accumulator.response.Output) == 0 {
			accumulator.response.Output = accumulator.sortedOutput()
		}
		accumulator.terminal = true
	}
	return nil
}

// Finalize returns the completed/incomplete response. EOF without a terminal
// event is an error and is never treated as a successful partial response.
func (accumulator *ResponseAccumulator) Finalize() (*ResponsesResponse, error) {
	if accumulator.err != nil {
		return nil, accumulator.err
	}
	if !accumulator.terminal {
		return nil, ErrMissingTerminalEvent
	}
	return &accumulator.response, nil
}

func (accumulator *ResponseAccumulator) item(index int, itemType string) *ResponsesOutput {
	item := accumulator.items[index]
	if item == nil {
		item = &ResponsesOutput{Type: itemType}
		accumulator.items[index] = item
	} else if item.Type == "" {
		item.Type = itemType
	}
	return item
}

func (accumulator *ResponseAccumulator) captureResponse(response *ResponsesResponse, terminal bool) {
	if response == nil {
		return
	}
	if response.ID != "" {
		accumulator.response.ID = response.ID
	}
	if response.Object != "" {
		accumulator.response.Object = response.Object
	}
	if response.Model != "" {
		accumulator.response.Model = response.Model
	}
	if response.Status != "" {
		accumulator.response.Status = response.Status
	}
	if response.Usage != nil {
		accumulator.response.Usage = response.Usage
	}
	if response.IncompleteDetails != nil {
		accumulator.response.IncompleteDetails = response.IncompleteDetails
	}
	if response.Error != nil {
		accumulator.response.Error = response.Error
	}
	if terminal && len(response.Output) > 0 {
		accumulator.response.Output = response.Output
	}
}

func (accumulator *ResponseAccumulator) sortedOutput() []ResponsesOutput {
	indices := make([]int, 0, len(accumulator.items))
	for index := range accumulator.items {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	output := make([]ResponsesOutput, 0, len(indices))
	for _, index := range indices {
		output = append(output, *accumulator.items[index])
	}
	return output
}

func (accumulator *ResponseAccumulator) fail(err error) error {
	if err == nil {
		err = errors.New("upstream Responses error")
	}
	accumulator.terminal = true
	accumulator.err = err
	return err
}

func ensureContentPart(item *ResponsesOutput, index int, partType string) *ResponsesContentPart {
	for len(item.Content) <= index {
		item.Content = append(item.Content, ResponsesContentPart{Type: partType})
	}
	if item.Content[index].Type == "" {
		item.Content[index].Type = partType
	}
	return &item.Content[index]
}

func ensureSummaryPart(item *ResponsesOutput, index int) *ResponsesSummary {
	for len(item.Summary) <= index {
		item.Summary = append(item.Summary, ResponsesSummary{Type: "summary_text"})
	}
	if item.Summary[index].Type == "" {
		item.Summary[index].Type = "summary_text"
	}
	return &item.Summary[index]
}

func captureCallMetadata(item *ResponsesOutput, event ResponsesStreamEvent) {
	if event.CallID != "" {
		item.CallID = event.CallID
	}
	if event.Name != "" {
		item.Name = event.Name
	}
}

func mergeOutput(current *ResponsesOutput, done *ResponsesOutput) {
	if done.Type != "" {
		current.Type = done.Type
	}
	if done.ID != "" {
		current.ID = done.ID
	}
	if done.Role != "" {
		current.Role = done.Role
	}
	if done.Status != "" {
		current.Status = done.Status
	}
	if len(done.Content) > 0 {
		current.Content = done.Content
	}
	if done.EncryptedContent != "" {
		current.EncryptedContent = done.EncryptedContent
	}
	if len(done.Summary) > 0 {
		current.Summary = done.Summary
	}
	if done.CallID != "" {
		current.CallID = done.CallID
	}
	if done.Name != "" {
		current.Name = done.Name
	}
	if done.Arguments != "" {
		current.Arguments = done.Arguments
	}
	if done.Input != "" {
		current.Input = done.Input
	}
}
