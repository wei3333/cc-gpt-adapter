// SPDX-License-Identifier: LGPL-3.0-only

package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	minimumMaxOutputTokens = 128
	billingHeaderPrefix    = "x-anthropic-billing-header: "
)

// AnthropicToResponses converts the supported Anthropic Messages request
// shape into a Responses request. Codex-specific normalization that requires
// upstream knowledge remains a phase-3 responsibility.
func AnthropicToResponses(req *AnthropicRequest) (*ResponsesRequest, error) {
	if req == nil {
		return nil, errors.New("convert Anthropic request: nil request")
	}

	input, err := convertInput(req.System, req.Messages)
	if err != nil {
		return nil, err
	}

	store := false
	parallelToolCalls := true
	out := &ResponsesRequest{
		Model:             req.Model,
		Input:             input,
		Stream:            req.Stream,
		Include:           []string{"reasoning.encrypted_content"},
		Store:             &store,
		ParallelToolCalls: &parallelToolCalls,
		Reasoning: &ResponsesReasoning{
			Effort:  "medium",
			Summary: "auto",
		},
		Text: &ResponsesText{Verbosity: "medium"},
	}

	// The generic bridge records Anthropic's limit here. Phase 3 removes this
	// field because the subscription Codex backend does not honor it.
	if req.MaxTokens > 0 {
		maxTokens := max(req.MaxTokens, minimumMaxOutputTokens)
		out.MaxOutputTokens = &maxTokens
	}

	if len(req.Tools) > 0 {
		out.Tools, err = convertTools(req.Tools)
		if err != nil {
			return nil, err
		}
	}

	if req.OutputConfig != nil && req.OutputConfig.Effort != "" {
		effort, err := convertEffort(req.OutputConfig.Effort)
		if err != nil {
			return nil, err
		}
		out.Reasoning.Effort = effort
	}

	if len(bytes.TrimSpace(req.ToolChoice)) > 0 {
		out.ToolChoice, err = convertToolChoice(req.ToolChoice)
		if err != nil {
			return nil, fmt.Errorf("convert tool_choice: %w", err)
		}
	}

	// temperature, top_p and stop_sequences are intentionally ignored. They
	// are not safe to emulate locally and are unsupported by the fixed Codex
	// reasoning model used by this adapter.
	return out, nil
}

func convertInput(system json.RawMessage, messages []AnthropicMessage) ([]ResponsesInputItem, error) {
	var out []ResponsesInputItem

	if len(bytes.TrimSpace(system)) > 0 {
		parts, err := convertSystem(system)
		if err != nil {
			return nil, fmt.Errorf("convert system: %w", err)
		}
		if len(parts) > 0 {
			out = append(out, ResponsesInputItem{
				Type:    "message",
				Role:    "developer",
				Content: parts,
			})
		}
	}

	for i, message := range messages {
		items, err := convertMessage(message)
		if err != nil {
			return nil, fmt.Errorf("convert message %d: %w", i, err)
		}
		out = append(out, items...)
	}

	return out, nil
}

func convertSystem(raw json.RawMessage) ([]ResponsesContentPart, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if text == "" || strings.HasPrefix(text, billingHeaderPrefix) {
			return nil, nil
		}
		return []ResponsesContentPart{{Type: "input_text", Text: text}}, nil
	}

	var blocks []AnthropicContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, errors.New("must be a string or content-block array")
	}

	parts := make([]ResponsesContentPart, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" && !strings.HasPrefix(block.Text, billingHeaderPrefix) {
			parts = append(parts, ResponsesContentPart{Type: "input_text", Text: block.Text})
		}
	}
	return parts, nil
}

func convertMessage(message AnthropicMessage) ([]ResponsesInputItem, error) {
	switch message.Role {
	case "user":
		return convertUserMessage(message.Content)
	case "assistant":
		return convertAssistantMessage(message.Content)
	default:
		return nil, fmt.Errorf("unsupported role %q", message.Role)
	}
}

func convertUserMessage(raw json.RawMessage) ([]ResponsesInputItem, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return []ResponsesInputItem{{
			Type:    "message",
			Role:    "user",
			Content: []ResponsesContentPart{{Type: "input_text", Text: text}},
		}}, nil
	}

	var blocks []AnthropicContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, errors.New("user content must be a string or content-block array")
	}

	var out []ResponsesInputItem
	var resultImages []ResponsesContentPart
	for _, block := range blocks {
		if block.Type != "tool_result" {
			continue
		}
		output, images := convertToolResult(block)
		out = append(out, ResponsesInputItem{
			Type:   "function_call_output",
			CallID: block.ToolUseID,
			Output: output,
		})
		resultImages = append(resultImages, images...)
	}

	var parts []ResponsesContentPart
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text != "" {
				parts = append(parts, ResponsesContentPart{Type: "input_text", Text: block.Text})
			}
		case "image":
			if imageURL := imageDataURI(block.Source); imageURL != "" {
				parts = append(parts, ResponsesContentPart{Type: "input_image", ImageURL: imageURL})
			}
		}
	}
	parts = append(parts, resultImages...)
	if len(parts) > 0 {
		out = append(out, ResponsesInputItem{Type: "message", Role: "user", Content: parts})
	}

	return out, nil
}

func convertAssistantMessage(raw json.RawMessage) ([]ResponsesInputItem, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return []ResponsesInputItem{{
			Type:    "message",
			Role:    "assistant",
			Content: []ResponsesContentPart{{Type: "output_text", Text: text}},
		}}, nil
	}

	var blocks []AnthropicContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, errors.New("assistant content must be a string or content-block array")
	}

	var out []ResponsesInputItem
	for _, block := range blocks {
		if block.Type == "thinking" && strings.TrimSpace(block.Signature) != "" {
			out = append(out, ResponsesInputItem{
				Type:             "reasoning",
				EncryptedContent: block.Signature,
			})
		}
	}

	var textParts []string
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			textParts = append(textParts, block.Text)
		}
	}
	if len(textParts) > 0 {
		out = append(out, ResponsesInputItem{
			Type: "message",
			Role: "assistant",
			Content: []ResponsesContentPart{{
				Type: "output_text",
				Text: strings.Join(textParts, "\n\n"),
			}},
		})
	}

	for _, block := range blocks {
		if block.Type != "tool_use" {
			continue
		}
		arguments := "{}"
		if len(bytes.TrimSpace(block.Input)) > 0 {
			arguments = string(block.Input)
		}
		out = append(out, ResponsesInputItem{
			Type:      "function_call",
			CallID:    block.ID,
			Name:      block.Name,
			Arguments: arguments,
		})
	}

	return out, nil
}

func convertToolResult(block AnthropicContentBlock) (string, []ResponsesContentPart) {
	if len(bytes.TrimSpace(block.Content)) == 0 {
		return formatToolResultOutput("(empty)", block.IsError), nil
	}

	var text string
	if err := json.Unmarshal(block.Content, &text); err == nil {
		if text == "" {
			text = "(empty)"
		}
		return formatToolResultOutput(text, block.IsError), nil
	}

	var blocks []AnthropicContentBlock
	if err := json.Unmarshal(block.Content, &blocks); err != nil {
		return formatToolResultOutput("(empty)", block.IsError), nil
	}

	var textParts []string
	var images []ResponsesContentPart
	for _, inner := range blocks {
		switch inner.Type {
		case "text":
			if inner.Text != "" {
				textParts = append(textParts, inner.Text)
			}
		case "image":
			if imageURL := imageDataURI(inner.Source); imageURL != "" {
				images = append(images, ResponsesContentPart{Type: "input_image", ImageURL: imageURL})
			}
		}
	}

	output := strings.Join(textParts, "\n\n")
	if output == "" {
		output = "(empty)"
	}
	return formatToolResultOutput(output, block.IsError), images
}

func formatToolResultOutput(output string, isError bool) string {
	if !isError {
		return output
	}
	if output == "(empty)" || strings.TrimSpace(output) == "" {
		output = "Tool execution failed without an error message."
	}
	return "[tool_error]\n" + output
}

func imageDataURI(source *AnthropicImageSource) string {
	if source == nil || source.Data == "" {
		return ""
	}
	mediaType := source.MediaType
	if mediaType == "" {
		mediaType = "image/png"
	}
	return "data:" + mediaType + ";base64," + source.Data
}

func convertTools(tools []AnthropicTool) ([]ResponsesTool, error) {
	out := make([]ResponsesTool, 0, len(tools))
	for i, tool := range tools {
		parameters, err := normalizeToolParameters(tool.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("convert tool %d (%q): %w", i, tool.Name, err)
		}
		strict := false
		out = append(out, ResponsesTool{
			Type:        "function",
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  parameters,
			Strict:      &strict,
		})
	}
	return out, nil
}

func normalizeToolParameters(schema json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(schema)) == 0 || bytes.Equal(bytes.TrimSpace(schema), []byte("null")) {
		return json.RawMessage(`{"type":"object","properties":{}}`), nil
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal(schema, &object); err != nil {
		return nil, errors.New("input_schema must be valid JSON")
	}
	if string(object["type"]) != `"object"` {
		return append(json.RawMessage(nil), schema...), nil
	}
	if _, exists := object["properties"]; exists {
		return append(json.RawMessage(nil), schema...), nil
	}
	object["properties"] = json.RawMessage(`{}`)
	return json.Marshal(object)
}

func convertEffort(effort string) (string, error) {
	switch effort {
	case "low", "medium", "high":
		return effort, nil
	case "max":
		return "xhigh", nil
	default:
		return "", fmt.Errorf("unsupported output_config.effort %q", effort)
	}
}

func convertToolChoice(raw json.RawMessage) (json.RawMessage, error) {
	var choice struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &choice); err != nil {
		return nil, errors.New("must be a JSON object")
	}

	switch choice.Type {
	case "auto":
		return json.Marshal("auto")
	case "any":
		return json.Marshal("required")
	case "none":
		return json.Marshal("none")
	case "tool":
		if choice.Name == "" {
			return nil, errors.New("tool choice requires a name")
		}
		return json.Marshal(struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}{Type: "function", Name: choice.Name})
	default:
		return nil, fmt.Errorf("unsupported type %q", choice.Type)
	}
}
