// SPDX-License-Identifier: LGPL-3.0-only

// Package codex contains the compatibility boundary for the private ChatGPT
// Codex subscription backend. It deliberately has no OAuth implementation;
// callers provide credentials at request time.
package codex

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	// Model is the only upstream model exposed by this adapter.
	Model = "gpt-5.6-sol"

	defaultInstructions = "You are a helpful coding assistant."
	callIDMaxLength     = 64
	callIDPrefix        = "fc_"
	reasoningInclude    = "reasoning.encrypted_content"
)

var unsupportedFields = [...]string{
	"user",
	"metadata",
	"prompt_cache_key",
	"prompt_cache_retention",
	"safety_identifier",
	"stream_options",
	"max_output_tokens",
	"max_completion_tokens",
	"temperature",
	"top_p",
	"frequency_penalty",
	"presence_penalty",
}

// Transform normalizes a Responses request for the ChatGPT Codex OAuth
// endpoint. Unknown fields are retained unless the endpoint is known not to
// support them. JSON numbers are preserved with json.Number.
func Transform(body []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()

	var request map[string]any
	if err := decoder.Decode(&request); err != nil {
		return nil, fmt.Errorf("transform Codex request: decode JSON: %w", err)
	}
	if request == nil {
		return nil, errors.New("transform Codex request: body must be a JSON object")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}

	request["model"] = Model
	request["stream"] = true
	request["store"] = false
	for _, field := range unsupportedFields {
		delete(request, field)
	}

	normalizeInstructions(request)
	ensureReasoningInclude(request)
	normalizeLegacyFunctions(request)
	normalizeTools(request)
	normalizeToolChoice(request)
	normalizeInput(request)

	transformed, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("transform Codex request: encode JSON: %w", err)
	}
	return transformed, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return errors.New("transform Codex request: multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("transform Codex request: decode trailing JSON: %w", err)
	}
	return nil
}

func normalizeInstructions(request map[string]any) {
	instructions, ok := request["instructions"].(string)
	if !ok || strings.TrimSpace(instructions) == "" {
		request["instructions"] = defaultInstructions
	}
}

func ensureReasoningInclude(request map[string]any) {
	if reasoning, ok := request["reasoning"].(map[string]any); !ok || len(reasoning) == 0 {
		return
	}

	include, ok := request["include"].([]any)
	if !ok {
		request["include"] = []any{reasoningInclude}
		return
	}
	for _, item := range include {
		if item == reasoningInclude {
			return
		}
	}
	request["include"] = append(include, reasoningInclude)
}

func normalizeLegacyFunctions(request map[string]any) {
	if rawFunctions, exists := request["functions"]; exists {
		if functions, ok := rawFunctions.([]any); ok {
			tools := make([]any, 0, len(functions))
			for _, function := range functions {
				tools = append(tools, map[string]any{"type": "function", "function": function})
			}
			request["tools"] = tools
		}
		delete(request, "functions")
	}

	if rawChoice, exists := request["function_call"]; exists {
		switch choice := rawChoice.(type) {
		case string:
			request["tool_choice"] = choice
		case map[string]any:
			if name := stringValue(choice["name"]); name != "" {
				request["tool_choice"] = map[string]any{"type": "function", "name": name}
			}
		}
		delete(request, "function_call")
	}
}

func normalizeTools(request map[string]any) {
	tools, ok := request["tools"].([]any)
	if !ok {
		return
	}

	normalized := make([]any, 0, len(tools))
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			normalized = append(normalized, rawTool)
			continue
		}
		if stringValue(tool["type"]) != "function" {
			normalized = append(normalized, tool)
			continue
		}

		if stringValue(tool["name"]) == "" {
			function, ok := tool["function"].(map[string]any)
			if !ok || stringValue(function["name"]) == "" {
				continue
			}
			for _, field := range []string{"name", "description", "parameters", "strict"} {
				if _, exists := tool[field]; !exists {
					if value, exists := function[field]; exists {
						tool[field] = value
					}
				}
			}
		}
		delete(tool, "function")
		normalized = append(normalized, tool)
	}
	request["tools"] = normalized
}

func normalizeToolChoice(request map[string]any) {
	choice, ok := request["tool_choice"].(map[string]any)
	if !ok {
		return
	}

	choiceType := stringValue(choice["type"])
	if choiceType == "" {
		return
	}
	if choiceType == "function" {
		name := stringValue(choice["name"])
		if name == "" {
			if function, ok := choice["function"].(map[string]any); ok {
				name = stringValue(function["name"])
			}
		}
		if name == "" || !toolsContainFunction(request["tools"], name) {
			request["tool_choice"] = "auto"
			return
		}
		request["tool_choice"] = map[string]any{"type": "function", "name": name}
		return
	}
	if !toolsContainType(request["tools"], choiceType) {
		request["tool_choice"] = "auto"
	}
}

func normalizeInput(request map[string]any) {
	switch input := request["input"].(type) {
	case string:
		if strings.TrimSpace(input) == "" {
			request["input"] = []any{}
		} else {
			request["input"] = []any{map[string]any{
				"type": "message", "role": "user", "content": input,
			}}
		}
	case []any:
		for index, rawItem := range input {
			item, ok := rawItem.(map[string]any)
			if !ok {
				continue
			}
			itemType := stringValue(item["type"])
			if itemType == "reasoning" {
				delete(item, "id")
				if summary, exists := item["summary"]; !exists || summary == nil {
					item["summary"] = []any{}
				}
			}
			if isToolCallItem(itemType) {
				callID := stringValue(item["call_id"])
				if callID == "" {
					callID = stringValue(item["id"])
				}
				if callID != "" {
					item["call_id"] = compactCallIDIfNeeded(callID)
				}
			} else {
				delete(item, "call_id")
			}
			if requiresToolName(itemType) && stringValue(item["name"]) == "" {
				name := stringValue(item["tool_name"])
				if name == "" {
					if function, ok := item["function"].(map[string]any); ok {
						name = stringValue(function["name"])
					}
				}
				if name == "" {
					name = "tool"
				}
				item["name"] = name
			}
			input[index] = item
		}
		request["input"] = input
	case nil:
		request["input"] = []any{}
	}
}

func compactCallIDIfNeeded(id string) string {
	if len(id) <= callIDMaxLength {
		return id
	}
	digest := sha256.Sum256([]byte("sub2api:codex-call-id:v1:" + id))
	encoded := hex.EncodeToString(digest[:])
	return callIDPrefix + encoded[:callIDMaxLength-len(callIDPrefix)]
}

func isToolCallItem(itemType string) bool {
	switch itemType {
	case "function_call", "tool_call", "local_shell_call", "tool_search_call",
		"custom_tool_call", "mcp_tool_call", "function_call_output",
		"mcp_tool_call_output", "custom_tool_call_output", "tool_search_output":
		return true
	default:
		return false
	}
}

func requiresToolName(itemType string) bool {
	return itemType == "function_call" || itemType == "custom_tool_call" || itemType == "mcp_tool_call"
}

func toolsContainFunction(rawTools any, name string) bool {
	tools, ok := rawTools.([]any)
	if !ok {
		return false
	}
	for _, rawTool := range tools {
		if tool, ok := rawTool.(map[string]any); ok &&
			stringValue(tool["type"]) == "function" && stringValue(tool["name"]) == name {
			return true
		}
	}
	return false
}

func toolsContainType(rawTools any, toolType string) bool {
	tools, ok := rawTools.([]any)
	if !ok {
		return false
	}
	for _, rawTool := range tools {
		if tool, ok := rawTool.(map[string]any); ok && stringValue(tool["type"]) == toolType {
			return true
		}
	}
	return false
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}
