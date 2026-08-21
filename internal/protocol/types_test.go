// SPDX-License-Identifier: LGPL-3.0-only

package protocol

import (
	"encoding/json"
	"testing"
)

func TestUsageTypesPreserveCacheFields(t *testing.T) {
	t.Parallel()

	anthropicWire := []byte(`{
		"input_tokens":100,
		"output_tokens":20,
		"cache_creation_input_tokens":7,
		"cache_read_input_tokens":80
	}`)
	var anthropic AnthropicUsage
	if err := json.Unmarshal(anthropicWire, &anthropic); err != nil {
		t.Fatal(err)
	}
	if anthropic.InputTokens != 100 || anthropic.OutputTokens != 20 ||
		anthropic.CacheCreationInputTokens != 7 || anthropic.CacheReadInputTokens != 80 {
		t.Fatalf("Anthropic usage = %#v", anthropic)
	}

	responsesWire := []byte(`{
		"input_tokens":100,
		"output_tokens":20,
		"total_tokens":120,
		"input_tokens_details":{"cached_tokens":80}
	}`)
	var responses ResponsesUsage
	if err := json.Unmarshal(responsesWire, &responses); err != nil {
		t.Fatal(err)
	}
	if responses.InputTokensDetails == nil || responses.InputTokensDetails.CachedTokens != 80 {
		t.Fatalf("Responses usage = %#v", responses)
	}
}
