// SPDX-License-Identifier: LGPL-3.0-only

package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/cc-gpt-adapter/internal/auth"
	"github.com/Wei-Shaw/cc-gpt-adapter/internal/protocol"
	"github.com/Wei-Shaw/cc-gpt-adapter/internal/upstream/codex"
)

const (
	DefaultMaxRequestBytes = 32 << 20
	DefaultSSEIdleTimeout  = 90 * time.Second
	maxUpstreamErrorBytes  = 512 << 10
)

type TokenProvider interface {
	GetValidToken(context.Context) (auth.Access, error)
	ForceRefresh(context.Context, string) (auth.Access, error)
}

type Upstream interface {
	Do(context.Context, []byte, codex.Credentials, codex.Session) (*http.Response, error)
}

type Config struct {
	LocalSecret      string
	Tokens           TokenProvider
	Upstream         Upstream
	MaxRequestBytes  int64
	SSEIdleTimeout   time.Duration
	MaxSSEEventBytes int
}

type Handler struct {
	localSecret      string
	tokens           TokenProvider
	upstream         Upstream
	maxRequestBytes  int64
	sseIdleTimeout   time.Duration
	maxSSEEventBytes int
	routes           *http.ServeMux
}

func New(config Config) (*Handler, error) {
	if strings.TrimSpace(config.LocalSecret) == "" {
		return nil, errors.New("create server: local secret is required")
	}
	if config.Tokens == nil || config.Upstream == nil {
		return nil, errors.New("create server: token provider and upstream are required")
	}
	if config.MaxRequestBytes <= 0 {
		config.MaxRequestBytes = DefaultMaxRequestBytes
	}
	if config.SSEIdleTimeout <= 0 {
		config.SSEIdleTimeout = DefaultSSEIdleTimeout
	}
	if config.MaxSSEEventBytes <= 0 {
		config.MaxSSEEventBytes = protocol.DefaultMaxSSEEventBytes
	}
	handler := &Handler{
		localSecret: config.LocalSecret, tokens: config.Tokens, upstream: config.Upstream,
		maxRequestBytes: config.MaxRequestBytes, sseIdleTimeout: config.SSEIdleTimeout,
		maxSSEEventBytes: config.MaxSSEEventBytes,
		routes:           http.NewServeMux(),
	}
	handler.routes.HandleFunc("GET /healthz", handler.health)
	handler.routes.HandleFunc("POST /v1/messages", handler.requireAuthentication(handler.messages))
	handler.routes.HandleFunc("POST /v1/messages/count_tokens", handler.requireAuthentication(handler.countTokens))
	return handler, nil
}

func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	handler.routes.ServeHTTP(writer, request)
}

func (handler *Handler) health(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
}

func (handler *Handler) requireAuthentication(next http.HandlerFunc) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if !handler.authenticated(request) {
			writeError(writer, http.StatusUnauthorized, "authentication_error", "Invalid local API key")
			return
		}
		next(writer, request)
	}
}

func (handler *Handler) authenticated(request *http.Request) bool {
	candidates := []string{strings.TrimSpace(request.Header.Get("x-api-key"))}
	authorization := strings.Fields(request.Header.Get("Authorization"))
	if len(authorization) == 2 && strings.EqualFold(authorization[0], "Bearer") {
		candidates = append(candidates, authorization[1])
	}
	for _, candidate := range candidates {
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(handler.localSecret)) == 1 {
			return true
		}
	}
	return false
}

func (handler *Handler) messages(writer http.ResponseWriter, request *http.Request) {
	anthropicRequest, err := handler.decodeRequest(writer, request)
	if err != nil {
		return
	}
	if err := validateMessagesRequest(anthropicRequest); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	responsesRequest, err := protocol.AnthropicToResponses(anthropicRequest)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request_error", publicMessage(err, "Invalid Messages request"))
		return
	}
	responsesBody, err := json.Marshal(responsesRequest)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "api_error", "Failed to build upstream request")
		return
	}
	session := codex.ResolveSession(request.Header, anthropicRequest)
	upstreamContext, cancelUpstream := context.WithCancel(request.Context())
	defer cancelUpstream()
	response, err := handler.callUpstream(upstreamContext, responsesBody, session)
	if err != nil {
		if request.Context().Err() == nil {
			writeError(writer, http.StatusBadGateway, "api_error", "Upstream request failed")
		}
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		handler.writeUpstreamHTTPError(writer, response)
		return
	}

	if anthropicRequest.Stream {
		handler.streamResponse(writer, request, response, cancelUpstream, anthropicRequest.Model)
		return
	}
	handler.nonStreamResponse(writer, request, response, cancelUpstream, anthropicRequest.Model)
}

func (handler *Handler) callUpstream(ctx context.Context, body []byte, session codex.Session) (*http.Response, error) {
	access, err := handler.tokens.GetValidToken(ctx)
	if err != nil {
		return nil, err
	}
	response, err := handler.upstream.Do(ctx, body, upstreamCredentials(access), session)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusUnauthorized {
		return response, nil
	}
	_ = response.Body.Close()
	refreshed, err := handler.tokens.ForceRefresh(ctx, access.AccessToken)
	if err != nil {
		return nil, err
	}
	return handler.upstream.Do(ctx, body, upstreamCredentials(refreshed), session)
}

func upstreamCredentials(access auth.Access) codex.Credentials {
	return codex.Credentials{AccessToken: access.AccessToken, AccountID: access.ChatGPTAccountID}
}

func (handler *Handler) decodeRequest(writer http.ResponseWriter, request *http.Request) (*protocol.AnthropicRequest, error) {
	if request.ContentLength > handler.maxRequestBytes {
		writeError(writer, http.StatusRequestEntityTooLarge, "invalid_request_error", "Request body is too large")
		return nil, errors.New("request body too large")
	}
	request.Body = http.MaxBytesReader(writer, request.Body, handler.maxRequestBytes)
	decoder := json.NewDecoder(request.Body)
	var decoded protocol.AnthropicRequest
	if err := decoder.Decode(&decoded); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeError(writer, http.StatusRequestEntityTooLarge, "invalid_request_error", "Request body is too large")
		} else if errors.Is(err, io.EOF) {
			writeError(writer, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		} else {
			writeError(writer, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		}
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeError(writer, http.StatusBadRequest, "invalid_request_error", "Request body must contain one JSON object")
		return nil, errors.New("multiple JSON values")
	}
	return &decoded, nil
}

func validateMessagesRequest(request *protocol.AnthropicRequest) error {
	if strings.TrimSpace(request.Model) == "" {
		return errors.New("model is required")
	}
	if len(request.Messages) == 0 {
		return errors.New("messages must contain at least one message")
	}
	if request.MaxTokens <= 0 {
		return errors.New("max_tokens must be positive")
	}
	return nil
}

func (handler *Handler) writeUpstreamHTTPError(writer http.ResponseWriter, response *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxUpstreamErrorBytes))
	switch response.StatusCode {
	case http.StatusTooManyRequests:
		writeError(writer, http.StatusTooManyRequests, "rate_limit_error", "Upstream rate limit exceeded")
	case http.StatusUnauthorized:
		writeError(writer, http.StatusBadGateway, "api_error", "Upstream authentication failed after token refresh")
	case http.StatusBadRequest:
		writeError(writer, http.StatusBadGateway, "api_error", "Upstream rejected the translated request")
	default:
		writeError(writer, http.StatusBadGateway, "api_error", "Upstream request failed")
	}
}

func writeError(writer http.ResponseWriter, status int, errorType, message string) {
	writeJSON(writer, status, protocol.NewAnthropicErrorEvent(errorType, message))
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func publicMessage(err error, fallback string) string {
	if err == nil {
		return fallback
	}
	message := strings.Map(func(character rune) rune {
		if character < 0x20 && character != '\t' {
			return -1
		}
		return character
	}, err.Error())
	message = strings.TrimSpace(message)
	if message == "" {
		return fallback
	}
	if len(message) > 512 {
		message = message[:512]
	}
	return message
}
