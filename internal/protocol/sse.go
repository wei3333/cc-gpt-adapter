// SPDX-License-Identifier: LGPL-3.0-only

package protocol

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const DefaultMaxSSEEventBytes = 16 << 20

var (
	// ErrSSEEventTooLarge is returned before an oversized frame is dispatched.
	ErrSSEEventTooLarge = errors.New("SSE event exceeds configured size limit")
	// ErrResponsesDone represents the non-JSON [DONE] sentinel. A successful
	// Responses stream must still have supplied a terminal response event.
	ErrResponsesDone = errors.New("Responses stream sent [DONE]")
)

// SSEFrame is one fully assembled Server-Sent Event. Multiple data fields are
// joined with newlines as required by the SSE format.
type SSEFrame struct {
	Event string
	Data  string
	ID    string
}

// SSEDecoder reads complete SSE frames without bufio.Scanner's 64 KiB token
// limit. It accepts LF and CRLF, comments, keepalives, and multi-line data.
type SSEDecoder struct {
	reader   *bufio.Reader
	maxBytes int
	first    bool
}

// NewSSEDecoder creates a frame decoder. A non-positive maxBytes selects the
// 16 MiB project default.
func NewSSEDecoder(reader io.Reader, maxBytes int) *SSEDecoder {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxSSEEventBytes
	}
	return &SSEDecoder{
		reader:   bufio.NewReader(reader),
		maxBytes: maxBytes,
		first:    true,
	}
}

// Next returns the next frame containing at least one data field. Empty
// comment/keepalive frames are consumed internally.
func (decoder *SSEDecoder) Next() (SSEFrame, error) {
	var frame SSEFrame
	var data []string
	var size int

	for {
		line, err := decoder.readLine()
		if decoder.first {
			line = bytes.TrimPrefix(line, []byte{0xef, 0xbb, 0xbf})
			decoder.first = false
		}
		if len(line) > 0 {
			size += len(line) + 1
			if size > decoder.maxBytes {
				return SSEFrame{}, ErrSSEEventTooLarge
			}
			parseSSELine(line, &frame, &data)
		}

		if err != nil && !errors.Is(err, io.EOF) {
			return SSEFrame{}, err
		}
		if len(line) == 0 || errors.Is(err, io.EOF) {
			if len(data) > 0 {
				if frame.Event == "" {
					frame.Event = "message"
				}
				frame.Data = strings.Join(data, "\n")
				return frame, nil
			}
			frame = SSEFrame{}
			data = nil
			size = 0
		}
		if errors.Is(err, io.EOF) {
			return SSEFrame{}, io.EOF
		}
	}
}

func (decoder *SSEDecoder) readLine() ([]byte, error) {
	var line []byte
	for {
		fragment, more, err := decoder.reader.ReadLine()
		line = append(line, fragment...)
		if err != nil {
			return line, err
		}
		if !more {
			return line, nil
		}
		if len(line) > decoder.maxBytes {
			return nil, ErrSSEEventTooLarge
		}
	}
}

func parseSSELine(line []byte, frame *SSEFrame, data *[]string) {
	if len(line) == 0 || line[0] == ':' {
		return
	}
	field, value, found := bytes.Cut(line, []byte{':'})
	if !found {
		value = nil
	} else if len(value) > 0 && value[0] == ' ' {
		value = value[1:]
	}

	switch string(field) {
	case "event":
		frame.Event = string(value)
	case "data":
		*data = append(*data, string(value))
	case "id":
		if !bytes.ContainsRune(value, '\x00') {
			frame.ID = string(value)
		}
	}
}

// DecodeResponsesEvent decodes a JSON Responses payload and uses the SSE
// event field as a type fallback for compatible upstreams that omit type.
func DecodeResponsesEvent(frame SSEFrame) (ResponsesStreamEvent, error) {
	if strings.TrimSpace(frame.Data) == "[DONE]" {
		return ResponsesStreamEvent{}, ErrResponsesDone
	}
	var event ResponsesStreamEvent
	if err := json.Unmarshal([]byte(frame.Data), &event); err != nil {
		return ResponsesStreamEvent{}, fmt.Errorf("decode Responses SSE event: %w", err)
	}
	if event.Type == "" && frame.Event != "" && frame.Event != "message" {
		event.Type = frame.Event
	}
	if event.Type == "" {
		return ResponsesStreamEvent{}, errors.New("decode Responses SSE event: missing type")
	}
	return event, nil
}

// EncodeAnthropicSSE serializes one Anthropic event as a complete SSE frame.
func EncodeAnthropicSSE(event AnthropicStreamEvent) ([]byte, error) {
	if event.Type == "" || strings.ContainsAny(event.Type, "\r\n") {
		return nil, errors.New("encode Anthropic SSE event: invalid type")
	}
	data, err := json.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("encode Anthropic SSE event: %w", err)
	}
	frame := make([]byte, 0, len(data)+len(event.Type)+16)
	frame = append(frame, "event: "...)
	frame = append(frame, event.Type...)
	frame = append(frame, '\n')
	frame = append(frame, "data: "...)
	frame = append(frame, data...)
	frame = append(frame, '\n', '\n')
	return frame, nil
}

// NewAnthropicErrorEvent maps an upstream/protocol error to Anthropic's SSE
// error shape. Server code can use the same object in a pre-stream JSON error.
func NewAnthropicErrorEvent(errorType, message string) AnthropicStreamEvent {
	if errorType == "" {
		errorType = "api_error"
	}
	return AnthropicStreamEvent{
		Type: "error",
		Error: &AnthropicError{
			Type:    errorType,
			Message: message,
		},
	}
}
