// SPDX-License-Identifier: LGPL-3.0-only

package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/Wei-Shaw/cc-gpt-adapter/internal/protocol"
)

var errSSEIdleTimeout = errors.New("upstream SSE idle timeout")

type frameResult struct {
	frame protocol.SSEFrame
	err   error
}

type activityReader struct {
	reader   io.Reader
	activity chan<- struct{}
}

func (reader activityReader) Read(buffer []byte) (int, error) {
	count, err := reader.reader.Read(buffer)
	if count > 0 {
		select {
		case reader.activity <- struct{}{}:
		default:
		}
	}
	return count, err
}

func decodeResponseFrames(ctx context.Context, body io.Reader, maxEventBytes int) (<-chan frameResult, <-chan struct{}) {
	frames := make(chan frameResult, 1)
	activity := make(chan struct{}, 1)
	decoder := protocol.NewSSEDecoder(activityReader{reader: body, activity: activity}, maxEventBytes)
	go func() {
		defer close(frames)
		for {
			frame, err := decoder.Next()
			result := frameResult{frame: frame, err: err}
			select {
			case frames <- result:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return frames, activity
}

func nextFrame(
	ctx context.Context,
	frames <-chan frameResult,
	activity <-chan struct{},
	idleTimeout time.Duration,
) (protocol.SSEFrame, error) {
	timer := time.NewTimer(idleTimeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return protocol.SSEFrame{}, ctx.Err()
		case <-activity:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(idleTimeout)
		case result, ok := <-frames:
			if !ok {
				return protocol.SSEFrame{}, io.EOF
			}
			return result.frame, result.err
		case <-timer.C:
			select {
			case <-activity:
				timer.Reset(idleTimeout)
				continue
			default:
				return protocol.SSEFrame{}, errSSEIdleTimeout
			}
		}
	}
}

func (handler *Handler) streamResponse(
	writer http.ResponseWriter,
	request *http.Request,
	response *http.Response,
	cancelUpstream context.CancelFunc,
	model string,
) {
	converter := protocol.NewResponsesStreamConverter(model)
	frames, activity := decodeResponseFrames(request.Context(), response.Body, handler.maxSSEEventBytes)
	started := false
	for {
		frame, err := nextFrame(request.Context(), frames, activity, handler.sseIdleTimeout)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || request.Context().Err() != nil {
				cancelUpstream()
				return
			}
			if errors.Is(err, io.EOF) || errors.Is(err, protocol.ErrResponsesDone) {
				err = converter.Finalize()
			}
			if err != nil {
				cancelUpstream()
				handler.writeProcessingError(writer, err, started)
			}
			return
		}
		event, err := protocol.DecodeResponsesEvent(frame)
		if err != nil {
			if errors.Is(err, protocol.ErrResponsesDone) {
				err = converter.Finalize()
			}
			if err != nil {
				cancelUpstream()
				handler.writeProcessingError(writer, err, started)
			}
			return
		}
		anthropicEvents, err := converter.Process(event)
		if err != nil {
			cancelUpstream()
			handler.writeProcessingError(writer, err, started)
			return
		}
		for _, anthropicEvent := range anthropicEvents {
			if !started {
				writer.Header().Set("Content-Type", "text/event-stream")
				writer.Header().Set("Cache-Control", "no-cache")
				writer.Header().Set("Connection", "keep-alive")
				writer.WriteHeader(http.StatusOK)
				started = true
			}
			encoded, encodeErr := protocol.EncodeAnthropicSSE(anthropicEvent)
			if encodeErr != nil {
				cancelUpstream()
				return
			}
			if _, writeErr := writer.Write(encoded); writeErr != nil {
				cancelUpstream()
				return
			}
			if flusher, ok := writer.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		if isTerminalResponsesEvent(event.Type) {
			return
		}
	}
}

func (handler *Handler) nonStreamResponse(
	writer http.ResponseWriter,
	request *http.Request,
	response *http.Response,
	cancelUpstream context.CancelFunc,
	model string,
) {
	accumulator := protocol.NewResponseAccumulator()
	frames, activity := decodeResponseFrames(request.Context(), response.Body, handler.maxSSEEventBytes)
	for {
		frame, err := nextFrame(request.Context(), frames, activity, handler.sseIdleTimeout)
		if err != nil {
			if request.Context().Err() != nil {
				cancelUpstream()
				return
			}
			if errors.Is(err, io.EOF) || errors.Is(err, protocol.ErrResponsesDone) {
				_, err = accumulator.Finalize()
			}
			if err != nil {
				cancelUpstream()
				handler.writeProcessingError(writer, err, false)
			}
			return
		}
		event, err := protocol.DecodeResponsesEvent(frame)
		if err != nil {
			if errors.Is(err, protocol.ErrResponsesDone) {
				_, err = accumulator.Finalize()
			}
			if err != nil {
				cancelUpstream()
				handler.writeProcessingError(writer, err, false)
			}
			return
		}
		if err := accumulator.Process(event); err != nil {
			cancelUpstream()
			handler.writeProcessingError(writer, err, false)
			return
		}
		if !isTerminalResponsesEvent(event.Type) {
			continue
		}
		terminal, err := accumulator.Finalize()
		if err != nil {
			handler.writeProcessingError(writer, err, false)
			return
		}
		anthropicResponse, err := protocol.ResponsesToAnthropic(terminal, model)
		if err != nil {
			handler.writeProcessingError(writer, err, false)
			return
		}
		writeJSON(writer, http.StatusOK, anthropicResponse)
		return
	}
}

func (handler *Handler) writeProcessingError(writer http.ResponseWriter, err error, started bool) {
	message := "Upstream response stream failed"
	if errors.Is(err, errSSEIdleTimeout) {
		message = "Upstream response timed out"
	} else {
		var upstreamError *protocol.UpstreamResponseError
		if errors.As(err, &upstreamError) {
			message = publicMessage(upstreamError, message)
		}
	}
	if !started {
		writeError(writer, http.StatusBadGateway, "api_error", message)
		return
	}
	event := protocol.NewAnthropicErrorEvent("api_error", message)
	encoded, encodeErr := protocol.EncodeAnthropicSSE(event)
	if encodeErr != nil {
		return
	}
	_, _ = writer.Write(encoded)
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

func isTerminalResponsesEvent(eventType string) bool {
	switch eventType {
	case "response.completed", "response.done", "response.incomplete", "response.failed", "error":
		return true
	default:
		return false
	}
}
