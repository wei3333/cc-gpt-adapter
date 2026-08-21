// SPDX-License-Identifier: LGPL-3.0-only

package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Wei-Shaw/cc-gpt-adapter/internal/protocol"
)

const estimatedImageTokens = 1024

func (handler *Handler) countTokens(writer http.ResponseWriter, request *http.Request) {
	anthropicRequest, err := handler.decodeRequest(writer, request)
	if err != nil {
		return
	}
	if strings.TrimSpace(anthropicRequest.Model) == "" {
		writeError(writer, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	if len(anthropicRequest.Messages) == 0 {
		writeError(writer, http.StatusBadRequest, "invalid_request_error", "messages must contain at least one message")
		return
	}
	count, err := EstimateInputTokens(anthropicRequest)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request_error", "Failed to estimate input tokens")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]int{"input_tokens": count})
}

// EstimateInputTokens provides a deterministic local approximation.
// It deliberately avoids counting base64 image bytes and encrypted reasoning.
func EstimateInputTokens(request *protocol.AnthropicRequest) (int, error) {
	responses, err := protocol.AnthropicToResponses(request)
	if err != nil {
		return 0, err
	}
	total := estimateTextTokens(responses.Instructions)
	for _, item := range responses.Input {
		total += 3
		total += estimateTextTokens(item.Type)
		total += estimateTextTokens(item.Role)
		total += estimateTextTokens(item.Name)
		total += estimateTextTokens(item.CallID)
		total += estimateTextTokens(item.Arguments)
		total += estimateTextTokens(item.Output)
		for _, part := range item.Content {
			total++
			switch part.Type {
			case "input_image":
				total += estimatedImageTokens
			default:
				total += estimateTextTokens(part.Text)
			}
		}
	}
	for _, tool := range responses.Tools {
		encoded, marshalErr := json.Marshal(tool)
		if marshalErr != nil {
			return 0, marshalErr
		}
		total += estimateTextTokens(compactJSON(encoded))
	}
	if len(bytes.TrimSpace(responses.ToolChoice)) > 0 {
		total += estimateTextTokens(compactJSON(responses.ToolChoice))
	}
	if total < 1 {
		return 1, nil
	}
	return total, nil
}

func estimateTextTokens(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	total := 0
	latinBytes := 0
	flushLatin := func() {
		if latinBytes > 0 {
			total += (latinBytes + 3) / 4
			latinBytes = 0
		}
	}
	for _, character := range text {
		if unicode.Is(unicode.Han, character) || unicode.Is(unicode.Hiragana, character) ||
			unicode.Is(unicode.Katakana, character) || unicode.Is(unicode.Hangul, character) {
			flushLatin()
			total++
			continue
		}
		latinBytes += utf8.RuneLen(character)
	}
	flushLatin()
	return total
}

func compactJSON(raw []byte) string {
	var compacted bytes.Buffer
	if json.Compact(&compacted, raw) != nil {
		return string(raw)
	}
	return compacted.String()
}
