// SPDX-License-Identifier: LGPL-3.0-only

package protocol

import "encoding/json"

// AnthropicRequest is the supported subset of a POST /v1/messages body.
// Sampling and stop fields are decoded so callers can accept Claude Code's
// wire format, but the Codex adapter deliberately does not forward them.
type AnthropicRequest struct {
	Model         string                 `json:"model"`
	MaxTokens     int                    `json:"max_tokens"`
	System        json.RawMessage        `json:"system,omitempty"`
	Messages      []AnthropicMessage     `json:"messages"`
	Tools         []AnthropicTool        `json:"tools,omitempty"`
	Stream        bool                   `json:"stream,omitempty"`
	Temperature   *float64               `json:"temperature,omitempty"`
	TopP          *float64               `json:"top_p,omitempty"`
	StopSequences []string               `json:"stop_sequences,omitempty"`
	Thinking      *AnthropicThinking     `json:"thinking,omitempty"`
	ToolChoice    json.RawMessage        `json:"tool_choice,omitempty"`
	Metadata      json.RawMessage        `json:"metadata,omitempty"`
	OutputConfig  *AnthropicOutputConfig `json:"output_config,omitempty"`
}

// AnthropicOutputConfig controls model effort.
type AnthropicOutputConfig struct {
	Effort string `json:"effort,omitempty"`
}

// AnthropicThinking describes Claude's extended-thinking request shape.
// Codex effort is selected through OutputConfig; this value is retained for
// wire compatibility and future response handling.
type AnthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

// AnthropicMessage is one user or assistant turn.
type AnthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// AnthropicContentBlock is the supported union of Claude Code content blocks.
type AnthropicContentBlock struct {
	Type         string                 `json:"type"`
	CacheControl *AnthropicCacheControl `json:"cache_control,omitempty"`

	Text      string `json:"text,omitempty"`
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`

	Source *AnthropicImageSource `json:"source,omitempty"`

	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// MarshalJSON keeps the required empty text/thinking field on block-start
// events. omitempty alone would produce a wire shape rejected by strict
// Anthropic clients.
func (block AnthropicContentBlock) MarshalJSON() ([]byte, error) {
	type alias AnthropicContentBlock
	switch block.Type {
	case "text":
		return json.Marshal(struct {
			Text string `json:"text"`
			alias
		}{Text: block.Text, alias: alias(block)})
	case "thinking":
		return json.Marshal(struct {
			Thinking string `json:"thinking"`
			alias
		}{Thinking: block.Thinking, alias: alias(block)})
	default:
		return json.Marshal(alias(block))
	}
}

// AnthropicImageSource contains an inline base64 image.
type AnthropicImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// AnthropicTool is a client tool definition.
type AnthropicTool struct {
	Type         string                 `json:"type,omitempty"`
	Name         string                 `json:"name"`
	Description  string                 `json:"description,omitempty"`
	InputSchema  json.RawMessage        `json:"input_schema,omitempty"`
	CacheControl *AnthropicCacheControl `json:"cache_control,omitempty"`
}

// AnthropicCacheControl retains Claude cache markers even though the Codex
// request bridge does not currently translate them into request fields.
type AnthropicCacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

// AnthropicUsage is the minimal usage shape needed for cache accounting in
// the response phases.
type AnthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// AnthropicResponse is a non-streaming Messages response or the message
// carried by a streaming message_start event.
type AnthropicResponse struct {
	ID           string                  `json:"id"`
	Type         string                  `json:"type"`
	Role         string                  `json:"role"`
	Content      []AnthropicContentBlock `json:"content"`
	Model        string                  `json:"model"`
	StopReason   *string                 `json:"stop_reason"`
	StopSequence *string                 `json:"stop_sequence,omitempty"`
	Usage        AnthropicUsage          `json:"usage"`
}

// AnthropicStreamEvent is one event in an Anthropic Messages SSE stream.
type AnthropicStreamEvent struct {
	Type string `json:"type"`

	Message *AnthropicResponse `json:"message,omitempty"`

	Index        *int                   `json:"index,omitempty"`
	ContentBlock *AnthropicContentBlock `json:"content_block,omitempty"`
	Delta        *AnthropicDelta        `json:"delta,omitempty"`
	Usage        *AnthropicUsage        `json:"usage,omitempty"`
	Error        *AnthropicError        `json:"error,omitempty"`
}

// AnthropicDelta contains a content fragment or final message metadata.
type AnthropicDelta struct {
	Type         string  `json:"type,omitempty"`
	Text         string  `json:"text,omitempty"`
	PartialJSON  string  `json:"partial_json,omitempty"`
	Thinking     string  `json:"thinking,omitempty"`
	Signature    string  `json:"signature,omitempty"`
	StopReason   string  `json:"stop_reason,omitempty"`
	StopSequence *string `json:"stop_sequence,omitempty"`
}

// AnthropicError is the error object used by Anthropic JSON and SSE errors.
type AnthropicError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// ResponsesRequest is the subset emitted by the request translator.
type ResponsesRequest struct {
	Model             string               `json:"model"`
	Instructions      string               `json:"instructions,omitempty"`
	Input             []ResponsesInputItem `json:"input"`
	MaxOutputTokens   *int                 `json:"max_output_tokens,omitempty"`
	Stream            bool                 `json:"stream,omitempty"`
	Tools             []ResponsesTool      `json:"tools,omitempty"`
	Include           []string             `json:"include,omitempty"`
	Store             *bool                `json:"store,omitempty"`
	ParallelToolCalls *bool                `json:"parallel_tool_calls,omitempty"`
	Reasoning         *ResponsesReasoning  `json:"reasoning,omitempty"`
	Text              *ResponsesText       `json:"text,omitempty"`
	ToolChoice        json.RawMessage      `json:"tool_choice,omitempty"`
}

// ResponsesReasoning selects the Codex reasoning effort and summary mode.
type ResponsesReasoning struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary,omitempty"`
}

// ResponsesText contains text-output preferences used by the Codex bridge.
type ResponsesText struct {
	Verbosity string `json:"verbosity,omitempty"`
}

// ResponsesInputItem is one message, reasoning item, function call, or
// function-call result in a Responses request.
type ResponsesInputItem struct {
	Type    string                 `json:"type,omitempty"`
	Role    string                 `json:"role,omitempty"`
	Content []ResponsesContentPart `json:"content,omitempty"`

	EncryptedContent string `json:"encrypted_content,omitempty"`

	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Output    string `json:"output,omitempty"`
}

// ResponsesContentPart is text or an inline image in a Responses message.
type ResponsesContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

// ResponsesTool is a Responses function-tool definition.
type ResponsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

// ResponsesUsage is the basic Responses usage shape needed by phase 2.
type ResponsesUsage struct {
	InputTokens              int                          `json:"input_tokens"`
	OutputTokens             int                          `json:"output_tokens"`
	TotalTokens              int                          `json:"total_tokens"`
	CacheCreationInputTokens int                          `json:"cache_creation_input_tokens,omitempty"`
	InputTokensDetails       *ResponsesInputTokensDetails `json:"input_tokens_details,omitempty"`
}

// ResponsesInputTokensDetails reports the cached portion of input tokens.
type ResponsesInputTokensDetails struct {
	CachedTokens        int `json:"cached_tokens"`
	CacheCreationTokens int `json:"cache_creation_tokens,omitempty"`
	CacheWriteTokens    int `json:"cache_write_tokens,omitempty"`
}

// ResponsesResponse is the terminal Responses object.
type ResponsesResponse struct {
	ID                string                      `json:"id"`
	Object            string                      `json:"object,omitempty"`
	Model             string                      `json:"model"`
	Status            string                      `json:"status"`
	Output            []ResponsesOutput           `json:"output"`
	Usage             *ResponsesUsage             `json:"usage,omitempty"`
	IncompleteDetails *ResponsesIncompleteDetails `json:"incomplete_details,omitempty"`
	Error             *ResponsesError             `json:"error,omitempty"`
}

// ResponsesIncompleteDetails explains an incomplete terminal response.
type ResponsesIncompleteDetails struct {
	Reason string `json:"reason"`
}

// ResponsesError is an upstream Responses error.
type ResponsesError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ResponsesOutput is one item in a terminal response or output-item event.
type ResponsesOutput struct {
	Type   string `json:"type"`
	ID     string `json:"id,omitempty"`
	Role   string `json:"role,omitempty"`
	Status string `json:"status,omitempty"`

	Content []ResponsesContentPart `json:"content,omitempty"`

	EncryptedContent string             `json:"encrypted_content,omitempty"`
	Summary          []ResponsesSummary `json:"summary,omitempty"`

	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Input     string `json:"input,omitempty"`
}

// ResponsesSummary is visible text attached to a reasoning output item.
type ResponsesSummary struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ResponsesStreamEvent is the supported union of Responses SSE payloads.
type ResponsesStreamEvent struct {
	Type string `json:"type"`

	Response *ResponsesResponse `json:"response,omitempty"`
	Usage    *ResponsesUsage    `json:"usage,omitempty"`
	Item     *ResponsesOutput   `json:"item,omitempty"`

	OutputIndex  int    `json:"output_index,omitempty"`
	ContentIndex int    `json:"content_index,omitempty"`
	Delta        string `json:"delta,omitempty"`
	Text         string `json:"text,omitempty"`
	ItemID       string `json:"item_id,omitempty"`

	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Input     string `json:"input,omitempty"`

	SummaryIndex int             `json:"summary_index,omitempty"`
	Code         string          `json:"code,omitempty"`
	Message      string          `json:"message,omitempty"`
	Param        string          `json:"param,omitempty"`
	Error        *ResponsesError `json:"error,omitempty"`

	SequenceNumber int `json:"sequence_number,omitempty"`
}
