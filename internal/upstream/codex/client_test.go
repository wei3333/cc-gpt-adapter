// SPDX-License-Identifier: LGPL-3.0-only

package codex

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type capturedRequest struct {
	Header http.Header
	Body   string
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestClientRequestSnapshotAndTurnStateIsolation(t *testing.T) {
	t.Parallel()
	captured := make(chan capturedRequest, 3)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		captured <- capturedRequest{Header: request.Header.Clone(), Body: string(body)}
		writer.Header().Set(TurnStateHeader, "state-for-"+request.Header.Get("session_id"))
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	store := NewTurnStateStore(0)
	client, err := NewClient(server.URL+"/backend-api/codex/responses", server.Client(), store)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	credentials := Credentials{AccessToken: "test-access-token", AccountID: "acct_test"}
	body := []byte(`{"model":"ignored","input":"hello","reasoning":{"effort":"medium"}}`)
	sessionA := newSession("header", "claude-a")
	sessionB := newSession("header", "claude-b")

	first := doTestRequest(t, client, body, credentials, sessionA)
	second := doTestRequest(t, client, body, credentials, sessionA)
	third := doTestRequest(t, client, body, credentials, sessionB)

	firstCapture := <-captured
	secondCapture := <-captured
	thirdCapture := <-captured
	assertIdentityHeaders(t, firstCapture.Header)
	if firstCapture.Header.Get("Authorization") != "Bearer test-access-token" {
		t.Fatalf("Authorization = %q", firstCapture.Header.Get("Authorization"))
	}
	if firstCapture.Header.Get(AccountIDHeader) != "acct_test" {
		t.Fatalf("%s = %q", AccountIDHeader, firstCapture.Header.Get(AccountIDHeader))
	}
	if firstCapture.Header.Get("session_id") != sessionA.ID {
		t.Fatalf("session_id = %q, want %q", firstCapture.Header.Get("session_id"), sessionA.ID)
	}
	if got := firstCapture.Header.Get("conversation_id"); got != "" {
		t.Fatalf("conversation_id unexpectedly sent: %q", got)
	}
	if got := firstCapture.Header.Get(TurnStateHeader); got != "" {
		t.Fatalf("first request unexpectedly carried turn-state: %q", got)
	}
	wantState := "state-for-" + sessionA.ID
	if got := secondCapture.Header.Get(TurnStateHeader); got != wantState {
		t.Fatalf("second request turn-state = %q, want %q", got, wantState)
	}
	if got := thirdCapture.Header.Get(TurnStateHeader); got != "" {
		t.Fatalf("different session inherited turn-state: %q", got)
	}
	if thirdCapture.Header.Get("session_id") != sessionB.ID {
		t.Fatalf("third session_id = %q, want %q", thirdCapture.Header.Get("session_id"), sessionB.ID)
	}
	if firstCapture.Body != secondCapture.Body || firstCapture.Body != thirdCapture.Body {
		t.Fatal("identical Responses bodies transformed differently")
	}
	for _, required := range []string{`"model":"gpt-5.6-sol"`, `"store":false`, `"stream":true`} {
		if !strings.Contains(firstCapture.Body, required) {
			t.Fatalf("transformed body %s does not contain %s", firstCapture.Body, required)
		}
	}

	first.Body.Close()
	second.Body.Close()
	third.Body.Close()
}

func TestClientOnlySuccessfulHTTPResponsesUpdateTurnState(t *testing.T) {
	t.Parallel()

	statuses := []int{
		http.StatusUnauthorized,
		http.StatusBadRequest,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusOK,
	}
	requestNumber := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		status := statuses[requestNumber]
		requestNumber++
		header := make(http.Header)
		header.Set(TurnStateHeader, http.StatusText(status))
		return &http.Response{
			StatusCode: status,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    request,
		}, nil
	})}

	store := NewTurnStateStore(0)
	session := newSession("header", "status-gated-state")
	store.Put(session.Key, "known-good")
	client, err := NewClient("https://example.test/responses", httpClient, store)
	if err != nil {
		t.Fatal(err)
	}
	credentials := Credentials{AccessToken: "token", AccountID: "account"}
	body := []byte(`{"input":"hello"}`)

	for _, status := range statuses {
		response := doTestRequest(t, client, body, credentials, session)
		if response.StatusCode != status {
			t.Fatalf("StatusCode = %d, want %d", response.StatusCode, status)
		}
		_ = response.Body.Close()
		got, ok := store.Get(session.Key)
		if !ok {
			t.Fatalf("turn-state missing after HTTP %d", status)
		}
		want := "known-good"
		if status >= http.StatusOK && status < http.StatusMultipleChoices {
			want = http.StatusText(status)
		}
		if got != want {
			t.Fatalf("turn-state after HTTP %d = %q, want %q", status, got, want)
		}
	}
}

func doTestRequest(t *testing.T, client *Client, body []byte, credentials Credentials, session Session) *http.Response {
	t.Helper()
	response, err := client.Do(context.Background(), body, credentials, session)
	if err != nil {
		t.Fatalf("Client.Do() error = %v", err)
	}
	return response
}

func assertIdentityHeaders(t *testing.T, headers http.Header) {
	t.Helper()
	if headers.Get("User-Agent") != UserAgent {
		t.Fatalf("User-Agent = %q, want %q", headers.Get("User-Agent"), UserAgent)
	}
	if headers.Get("originator") != Originator {
		t.Fatalf("originator = %q, want %q", headers.Get("originator"), Originator)
	}
	if headers.Get("version") != ClientVersion {
		t.Fatalf("version = %q, want %q", headers.Get("version"), ClientVersion)
	}
	if headers.Get("OpenAI-Beta") != OpenAIBeta {
		t.Fatalf("OpenAI-Beta = %q, want %q", headers.Get("OpenAI-Beta"), OpenAIBeta)
	}
	wantPrefix := Originator + "/" + ClientVersion + " "
	if !strings.HasPrefix(headers.Get("User-Agent"), wantPrefix) {
		t.Fatalf("identity is incoherent: UA %q, originator %q, version %q", headers.Get("User-Agent"), headers.Get("originator"), headers.Get("version"))
	}
}

func TestClientValidation(t *testing.T) {
	t.Parallel()
	for _, endpoint := range []string{"", "/relative", "file:///tmp/socket", "https://user:pass@example.test"} {
		if _, err := NewClient(endpoint, nil, nil); err == nil {
			t.Fatalf("NewClient(%q) unexpectedly succeeded", endpoint)
		}
	}
	client, err := NewClient("https://example.test/responses", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	session := newSession("header", "session")
	for _, credentials := range []Credentials{
		{},
		{AccessToken: "token"},
		{AccessToken: "bad\r\ntoken", AccountID: "acct"},
	} {
		if _, err := client.NewRequest(context.Background(), []byte(`{}`), credentials, session); err == nil {
			t.Fatalf("NewRequest(%#v) unexpectedly succeeded", credentials)
		}
	}
}
