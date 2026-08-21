// SPDX-License-Identifier: LGPL-3.0-only

package protocol

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestSSEDecoderMultilineDataCRLFAndKeepalive(t *testing.T) {
	t.Parallel()

	wire := ": keepalive\r\n\r\n" +
		"id: event-7\r\n" +
		"event: response.output_text.delta\r\n" +
		"data: {\"type\":\"response.output_text.delta\",\r\n" +
		"data: \"output_index\":0,\"delta\":\"hello\"}\r\n\r\n"
	decoder := NewSSEDecoder(strings.NewReader(wire), 0)
	frame, err := decoder.Next()
	if err != nil {
		t.Fatal(err)
	}
	if frame.Event != "response.output_text.delta" || frame.ID != "event-7" {
		t.Fatalf("frame metadata = %#v", frame)
	}
	if !strings.Contains(frame.Data, "\n") {
		t.Fatalf("multiline data was not joined with newline: %q", frame.Data)
	}
	event, err := DecodeResponsesEvent(frame)
	if err != nil {
		t.Fatal(err)
	}
	if event.Type != "response.output_text.delta" || event.OutputIndex != 0 || event.Delta != "hello" {
		t.Fatalf("event = %#v", event)
	}
	if _, err := decoder.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("final error = %v, want EOF", err)
	}
}

func TestSSEDecoderSupportsLargeEventBeyondScannerLimit(t *testing.T) {
	t.Parallel()

	text := strings.Repeat("x", 128<<10)
	wire := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"" + text + "\"}\n\n"
	frame, err := NewSSEDecoder(strings.NewReader(wire), 0).Next()
	if err != nil {
		t.Fatal(err)
	}
	event, err := DecodeResponsesEvent(frame)
	if err != nil {
		t.Fatal(err)
	}
	if event.Delta != text {
		t.Fatalf("delta length = %d, want %d", len(event.Delta), len(text))
	}
}

func TestSSEDecoderRejectsConfiguredOversizeEvent(t *testing.T) {
	t.Parallel()

	wire := "data: " + strings.Repeat("x", 128) + "\n\n"
	_, err := NewSSEDecoder(strings.NewReader(wire), 64).Next()
	if !errors.Is(err, ErrSSEEventTooLarge) {
		t.Fatalf("error = %v, want ErrSSEEventTooLarge", err)
	}
}

func TestSSEDecoderDispatchesFinalUnterminatedFrame(t *testing.T) {
	t.Parallel()

	decoder := NewSSEDecoder(strings.NewReader("event: response.completed\ndata: {\"type\":\"response.completed\"}"), 0)
	frame, err := decoder.Next()
	if err != nil {
		t.Fatal(err)
	}
	if frame.Event != "response.completed" {
		t.Fatalf("Event = %q", frame.Event)
	}
	if _, err := decoder.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("final error = %v, want EOF", err)
	}
}

func TestDecodeResponsesEventFallbackAndDone(t *testing.T) {
	t.Parallel()

	event, err := DecodeResponsesEvent(SSEFrame{
		Event: "response.completed",
		Data:  `{"response":{"status":"completed"}}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if event.Type != "response.completed" {
		t.Fatalf("Type = %q", event.Type)
	}
	if _, err := DecodeResponsesEvent(SSEFrame{Data: "[DONE]"}); !errors.Is(err, ErrResponsesDone) {
		t.Fatalf("[DONE] error = %v", err)
	}
	if _, err := DecodeResponsesEvent(SSEFrame{Data: "{"}); err == nil {
		t.Fatal("malformed JSON unexpectedly decoded")
	}
}

func TestEncodeAnthropicSSERequiredEmptyFields(t *testing.T) {
	t.Parallel()

	index := 0
	wire, err := EncodeAnthropicSSE(AnthropicStreamEvent{
		Type:  "content_block_start",
		Index: &index,
		ContentBlock: &AnthropicContentBlock{
			Type: "thinking",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(wire)
	if !strings.HasPrefix(encoded, "event: content_block_start\ndata: ") || !strings.HasSuffix(encoded, "\n\n") {
		t.Fatalf("invalid SSE framing: %q", encoded)
	}
	if !strings.Contains(encoded, `"thinking":""`) {
		t.Fatalf("required empty thinking field missing: %s", encoded)
	}
	if _, err := EncodeAnthropicSSE(AnthropicStreamEvent{Type: "bad\nevent"}); err == nil {
		t.Fatal("newline in event type unexpectedly accepted")
	}
}

func TestResponsesSSEToAnthropicEndToEnd(t *testing.T) {
	t.Parallel()

	wire := "event: response.created\n" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_e2e\",\"model\":\"gpt\"}}\n\n" +
		"event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"hello\"}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n"

	decoder := NewSSEDecoder(strings.NewReader(wire), 0)
	converter := NewResponsesStreamConverter("client-model")
	var output []AnthropicStreamEvent
	for {
		frame, err := decoder.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		event, err := DecodeResponsesEvent(frame)
		if err != nil {
			t.Fatal(err)
		}
		converted, err := converter.Process(event)
		if err != nil {
			t.Fatal(err)
		}
		output = append(output, converted...)
	}
	if err := converter.Finalize(); err != nil {
		t.Fatal(err)
	}
	assertEventTypes(t, output, []string{
		"message_start", "content_block_start", "content_block_delta",
		"content_block_stop", "message_delta", "message_stop",
	})
	assertStrictAnthropicOrder(t, output)
}
