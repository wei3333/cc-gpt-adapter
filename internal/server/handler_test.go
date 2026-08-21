// SPDX-License-Identifier: LGPL-3.0-only

package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/cc-gpt-adapter/internal/auth"
	"github.com/Wei-Shaw/cc-gpt-adapter/internal/protocol"
	"github.com/Wei-Shaw/cc-gpt-adapter/internal/upstream/codex"
)

const testLocalSecret = "local-secret-for-tests"

type fakeTokens struct {
	mu       sync.Mutex
	access   auth.Access
	refresh  auth.Access
	getErr   error
	forceErr error
	forces   int
}

func (tokens *fakeTokens) GetValidToken(context.Context) (auth.Access, error) {
	tokens.mu.Lock()
	defer tokens.mu.Unlock()
	return tokens.access, tokens.getErr
}

func (tokens *fakeTokens) ForceRefresh(_ context.Context, rejected string) (auth.Access, error) {
	tokens.mu.Lock()
	defer tokens.mu.Unlock()
	tokens.forces++
	if tokens.forceErr != nil {
		return auth.Access{}, tokens.forceErr
	}
	if tokens.access.AccessToken == rejected {
		tokens.access = tokens.refresh
	}
	return tokens.access, nil
}

func (tokens *fakeTokens) ForceCount() int {
	tokens.mu.Lock()
	defer tokens.mu.Unlock()
	return tokens.forces
}

type unusedUpstream struct{}

func (unusedUpstream) Do(context.Context, []byte, codex.Credentials, codex.Session) (*http.Response, error) {
	return nil, errors.New("unexpected upstream call")
}

func TestHealthAuthenticationAndRequestLimits(t *testing.T) {
	t.Parallel()
	handler := newTestHandler(t, Config{
		LocalSecret:     testLocalSecret,
		Tokens:          &fakeTokens{},
		Upstream:        unusedUpstream{},
		MaxRequestBytes: 64,
	})

	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK || health.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("health response = %d, headers=%v", health.Code, health.Header())
	}

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`)))
	assertErrorResponse(t, unauthorized, http.StatusUnauthorized, "authentication_error")

	oversized := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(strings.Repeat("x", 65)))
	request.Header.Set("x-api-key", testLocalSecret)
	handler.ServeHTTP(oversized, request)
	assertErrorResponse(t, oversized, http.StatusRequestEntityTooLarge, "invalid_request_error")

	invalid := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"m"} {}`))
	request.Header.Set("Authorization", "Bearer "+testLocalSecret)
	handler.ServeHTTP(invalid, request)
	assertErrorResponse(t, invalid, http.StatusBadRequest, "invalid_request_error")
}

func TestMessagesStreamingEndToEnd(t *testing.T) {
	t.Parallel()
	var capturedBody map[string]any
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer access-token" {
			t.Errorf("upstream Authorization = %q", request.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(request.Body).Decode(&capturedBody); err != nil {
			t.Error(err)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, responsesTextSSE("Hello from Codex"))
	}))
	defer upstreamServer.Close()

	tokens := &fakeTokens{access: auth.Access{AccessToken: "access-token", ChatGPTAccountID: "acct-test"}}
	handler := integrationHandler(t, upstreamServer, tokens, 0)
	adapter := httptest.NewServer(handler)
	defer adapter.Close()

	body := `{"model":"claude-sonnet-test","max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"hello"}]}`
	response := postMessages(t, adapter.Client(), adapter.URL+"/v1/messages", body, "Authorization", "Bearer "+testLocalSecret)
	defer response.Body.Close()
	result, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(result)
	if response.StatusCode != http.StatusOK || !strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("response = %d %s", response.StatusCode, response.Header.Get("Content-Type"))
	}
	for _, expected := range []string{
		"event: message_start", `"model":"claude-sonnet-test"`,
		"event: content_block_delta", "Hello from Codex", "event: message_stop",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("stream missing %q:\n%s", expected, text)
		}
	}
	if capturedBody["model"] != codex.Model || capturedBody["stream"] != true || capturedBody["store"] != false {
		t.Fatalf("upstream request = %#v", capturedBody)
	}
	if _, exists := capturedBody["max_output_tokens"]; exists {
		t.Fatal("unsupported max_output_tokens reached upstream")
	}
}

func TestMessagesNonStreamMultiTurnToolsAndTurnState(t *testing.T) {
	t.Parallel()
	var requestCount atomic.Int32
	var secondBody map[string]any
	var secondTurnState string
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		call := requestCount.Add(1)
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		if call == 1 {
			writer.Header().Set(codex.TurnStateHeader, "opaque-turn-one")
			_, _ = io.WriteString(writer, responsesToolsSSE())
			return
		}
		secondBody = body
		secondTurnState = request.Header.Get(codex.TurnStateHeader)
		_, _ = io.WriteString(writer, responsesTextSSE("tools complete"))
	}))
	defer upstreamServer.Close()

	handler := integrationHandler(t, upstreamServer, &fakeTokens{access: auth.Access{AccessToken: "access", ChatGPTAccountID: "acct"}}, 0)
	adapter := httptest.NewServer(handler)
	defer adapter.Close()
	firstBody := `{"model":"claude-tool-model","max_tokens":1024,"messages":[{"role":"user","content":"use tools"}]}`
	first := postMessagesWithSession(t, adapter.Client(), adapter.URL+"/v1/messages", firstBody, "session-tools")
	firstPayload, _ := io.ReadAll(first.Body)
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first response = %d %s", first.StatusCode, firstPayload)
	}
	var firstResponse protocol.AnthropicResponse
	if err := json.Unmarshal(firstPayload, &firstResponse); err != nil {
		t.Fatal(err)
	}
	if len(firstResponse.Content) != 2 || firstResponse.Content[0].Type != "tool_use" || firstResponse.Content[1].Type != "tool_use" ||
		firstResponse.StopReason == nil || *firstResponse.StopReason != "tool_use" {
		t.Fatalf("first response = %#v", firstResponse)
	}

	secondRequest := `{
      "model":"claude-tool-model","max_tokens":1024,"messages":[
        {"role":"user","content":"use tools"},
        {"role":"assistant","content":[
          {"type":"tool_use","id":"toolu_one","name":"first","input":{"x":1}},
          {"type":"tool_use","id":"toolu_two","name":"second","input":{"y":2}}
        ]},
        {"role":"user","content":[
          {"type":"tool_result","tool_use_id":"toolu_one","content":"one"},
          {"type":"tool_result","tool_use_id":"toolu_two","content":"two"}
        ]}
      ]
    }`
	second := postMessagesWithSession(t, adapter.Client(), adapter.URL+"/v1/messages", secondRequest, "session-tools")
	secondPayload, _ := io.ReadAll(second.Body)
	second.Body.Close()
	if second.StatusCode != http.StatusOK || !strings.Contains(string(secondPayload), "tools complete") {
		t.Fatalf("second response = %d %s", second.StatusCode, secondPayload)
	}
	if secondTurnState != "opaque-turn-one" {
		t.Fatalf("second turn state = %q", secondTurnState)
	}
	encodedSecond, _ := json.Marshal(secondBody)
	for _, id := range []string{"toolu_one", "toolu_two"} {
		if strings.Count(string(encodedSecond), id) != 2 {
			t.Fatalf("call/result ID %q not paired in %s", id, encodedSecond)
		}
	}
}

func TestMessagesRetriesOneUnauthorizedWithForcedRefresh(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Header.Get("Authorization") == "Bearer rejected-token" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(writer, responsesTextSSE("refreshed"))
	}))
	defer upstreamServer.Close()
	tokens := &fakeTokens{
		access:  auth.Access{AccessToken: "rejected-token", ChatGPTAccountID: "acct"},
		refresh: auth.Access{AccessToken: "fresh-token", ChatGPTAccountID: "acct"},
	}
	adapter := httptest.NewServer(integrationHandler(t, upstreamServer, tokens, 0))
	defer adapter.Close()
	response := postMessages(t, adapter.Client(), adapter.URL+"/v1/messages",
		`{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`,
		"x-api-key", testLocalSecret)
	payload, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(payload), "refreshed") {
		t.Fatalf("response = %d %s", response.StatusCode, payload)
	}
	if calls.Load() != 2 || tokens.ForceCount() != 1 {
		t.Fatalf("upstream calls/refreshes = %d/%d", calls.Load(), tokens.ForceCount())
	}
}

func TestMessagesRetriesUnauthorizedOnlyOnce(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(writer, `{"secret":"must not escape"}`)
	}))
	defer upstreamServer.Close()
	tokens := &fakeTokens{
		access:  auth.Access{AccessToken: "rejected-token", ChatGPTAccountID: "acct"},
		refresh: auth.Access{AccessToken: "fresh-token", ChatGPTAccountID: "acct"},
	}
	adapter := httptest.NewServer(integrationHandler(t, upstreamServer, tokens, 0))
	defer adapter.Close()
	response := postMessages(t, adapter.Client(), adapter.URL+"/v1/messages",
		`{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`,
		"x-api-key", testLocalSecret)
	payload, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusBadGateway || !strings.Contains(string(payload), "after token refresh") {
		t.Fatalf("response = %d %s", response.StatusCode, payload)
	}
	if strings.Contains(string(payload), "must not escape") {
		t.Fatalf("upstream body leaked: %s", payload)
	}
	if calls.Load() != 2 || tokens.ForceCount() != 1 {
		t.Fatalf("upstream calls/refreshes = %d/%d", calls.Load(), tokens.ForceCount())
	}
}

func TestMessagesStreamingFailedEventIsAnthropicError(t *testing.T) {
	t.Parallel()
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_fail\",\"model\":\"gpt\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
		_, _ = io.WriteString(writer, "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_fail\",\"status\":\"failed\",\"error\":{\"code\":\"policy_error\",\"message\":\"request rejected\"}}}\n\n")
	}))
	defer upstreamServer.Close()
	adapter := httptest.NewServer(integrationHandler(t, upstreamServer, &fakeTokens{access: auth.Access{AccessToken: "a", ChatGPTAccountID: "acct"}}, 0))
	defer adapter.Close()
	response := postMessages(t, adapter.Client(), adapter.URL+"/v1/messages",
		`{"model":"m","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
		"x-api-key", testLocalSecret)
	payload, _ := io.ReadAll(response.Body)
	response.Body.Close()
	text := string(payload)
	if !strings.Contains(text, "event: message_start") || !strings.Contains(text, "event: error") ||
		!strings.Contains(text, "request rejected") || strings.Contains(text, "event: message_stop") {
		t.Fatalf("failed stream = %s", text)
	}
}

func TestMessagesCancellationPropagatesUpstream(t *testing.T) {
	t.Parallel()
	upstreamStarted := make(chan struct{})
	upstreamCanceled := make(chan struct{})
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		close(upstreamStarted)
		<-request.Context().Done()
		close(upstreamCanceled)
	}))
	defer upstreamServer.Close()
	handler := integrationHandler(t, upstreamServer, &fakeTokens{access: auth.Access{AccessToken: "a", ChatGPTAccountID: "acct"}}, 0)
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"m","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)).WithContext(ctx)
	request.Header.Set("x-api-key", testLocalSecret)
	result := make(chan struct{})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), request)
		close(result)
	}()
	select {
	case <-upstreamStarted:
	case <-time.After(time.Second):
		t.Fatal("upstream request did not start")
	}
	cancel()
	select {
	case <-upstreamCanceled:
	case <-time.After(time.Second):
		t.Fatal("client cancellation did not reach upstream")
	}
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("adapter handler did not stop after client cancellation")
	}
}

func TestMessagesSSEIdleTimeout(t *testing.T) {
	t.Parallel()
	upstreamCanceled := make(chan struct{})
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
		close(upstreamCanceled)
	}))
	defer upstreamServer.Close()
	adapter := httptest.NewServer(integrationHandler(t, upstreamServer, &fakeTokens{access: auth.Access{AccessToken: "a", ChatGPTAccountID: "acct"}}, 30*time.Millisecond))
	defer adapter.Close()
	response := postMessages(t, adapter.Client(), adapter.URL+"/v1/messages",
		`{"model":"m","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
		"x-api-key", testLocalSecret)
	payload, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusBadGateway || !strings.Contains(string(payload), "timed out") {
		t.Fatalf("idle response = %d %s", response.StatusCode, payload)
	}
	select {
	case <-upstreamCanceled:
	case <-time.After(time.Second):
		t.Fatal("idle timeout did not cancel upstream")
	}
}

func TestCountTokensIsLocalAndDeterministic(t *testing.T) {
	t.Parallel()
	handler := newTestHandler(t, Config{LocalSecret: testLocalSecret, Tokens: &fakeTokens{}, Upstream: unusedUpstream{}})
	body := `{"model":"claude-test","system":"你是 coding assistant","messages":[{"role":"user","content":"hello 世界"}],"tools":[{"name":"lookup","input_schema":{"type":"object","properties":{"q":{"type":"string"}}}}]}`
	first := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(body))
	request.Header.Set("x-api-key", testLocalSecret)
	handler.ServeHTTP(first, request)
	second := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+testLocalSecret)
	handler.ServeHTTP(second, request)
	if first.Code != http.StatusOK || first.Body.String() != second.Body.String() {
		t.Fatalf("count responses = %d %q / %d %q", first.Code, first.Body.String(), second.Code, second.Body.String())
	}
	var count struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &count); err != nil || count.InputTokens <= 0 {
		t.Fatalf("count response = %#v, %v", count, err)
	}
}

func integrationHandler(t *testing.T, upstreamServer *httptest.Server, tokens TokenProvider, idle time.Duration) *Handler {
	t.Helper()
	upstream, err := codex.NewClient(upstreamServer.URL+"/backend-api/codex/responses", upstreamServer.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return newTestHandler(t, Config{
		LocalSecret: testLocalSecret, Tokens: tokens, Upstream: upstream,
		SSEIdleTimeout: idle,
	})
}

func newTestHandler(t *testing.T, config Config) *Handler {
	t.Helper()
	handler, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func postMessages(t *testing.T, client *http.Client, target, body, header, value string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(header, value)
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("POST %s: %v", target, err)
	}
	return response
}

func postMessagesWithSession(t *testing.T, client *http.Client, target, body, session string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("x-api-key", testLocalSecret)
	request.Header.Set(codex.ClaudeCodeSessionHeader, session)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func assertErrorResponse(t *testing.T, recorder *httptest.ResponseRecorder, status int, errorType string) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, status, recorder.Body.String())
	}
	var event protocol.AnthropicStreamEvent
	if err := json.Unmarshal(recorder.Body.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "error" || event.Error == nil || event.Error.Type != errorType {
		t.Fatalf("error response = %#v", event)
	}
}

func responsesTextSSE(text string) string {
	quoted, _ := json.Marshal(text)
	return "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_text\",\"model\":\"gpt\",\"status\":\"in_progress\",\"output\":[]}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":" + string(quoted) + "}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_text\",\"model\":\"gpt\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":5,\"output_tokens\":3,\"total_tokens\":8}}}\n\n"
}

func responsesToolsSSE() string {
	return "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_tools\",\"model\":\"gpt\",\"status\":\"completed\",\"output\":[{\"type\":\"function_call\",\"call_id\":\"toolu_one\",\"name\":\"first\",\"arguments\":\"{\\\"x\\\":1}\"},{\"type\":\"function_call\",\"call_id\":\"toolu_two\",\"name\":\"second\",\"arguments\":\"{\\\"y\\\":2}\"}],\"usage\":{\"input_tokens\":5,\"output_tokens\":3}}}\n\n"
}
