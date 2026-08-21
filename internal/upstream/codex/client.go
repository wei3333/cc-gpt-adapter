// SPDX-License-Identifier: LGPL-3.0-only

package codex

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const (
	DefaultEndpoint = "https://chatgpt.com/backend-api/codex/responses"
	ClientVersion   = "0.146.0"
	Originator      = "codex-tui"
	UserAgent       = Originator + "/" + ClientVersion + " (Ubuntu 22.4.0; x86_64) xterm-256color"
	OpenAIBeta      = "responses=experimental"

	AccountIDHeader = "chatgpt-account-id"
)

// Credentials is the short-lived authentication material supplied by phase
// 4. The upstream package never persists it.
type Credentials struct {
	AccessToken string
	AccountID   string
}

// Client sends already translated Responses requests through the mandatory
// Codex transform. Endpoint and HTTPClient are injectable for hermetic tests.
type Client struct {
	endpoint   string
	httpClient *http.Client
	turnStates *TurnStateStore
}

func NewClient(endpoint string, httpClient *http.Client, turnStates *TurnStateStore) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("create Codex client: endpoint must be an absolute HTTP(S) URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("create Codex client: endpoint must use HTTP(S)")
	}
	if parsed.User != nil {
		return nil, errors.New("create Codex client: endpoint must not contain credentials")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if turnStates == nil {
		turnStates = NewTurnStateStore(0)
	}
	return &Client{endpoint: parsed.String(), httpClient: httpClient, turnStates: turnStates}, nil
}

// NewRequest constructs an upstream request without sending it.
func (client *Client) NewRequest(
	ctx context.Context,
	responsesBody []byte,
	credentials Credentials,
	session Session,
) (*http.Request, error) {
	if client == nil {
		return nil, errors.New("create Codex request: nil client")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if session.Key == "" || session.ID == "" {
		return nil, errors.New("create Codex request: unresolved session")
	}
	body, err := Transform(responsesBody)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create Codex request: %w", err)
	}
	if err := applyHeaders(request.Header, credentials); err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("session_id", session.ID)
	request.Header.Del("conversation_id")
	if state, ok := client.turnStates.Get(session.Key); ok {
		request.Header.Set(TurnStateHeader, state)
	}
	return request, nil
}

// Do sends one request and remembers a turn-state response header for the
// same Claude session. It does not read or close the response body.
func (client *Client) Do(
	ctx context.Context,
	responsesBody []byte,
	credentials Credentials,
	session Session,
) (*http.Response, error) {
	request, err := client.NewRequest(ctx, responsesBody, credentials, session)
	if err != nil {
		return nil, err
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("send Codex request: %w", err)
	}
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		if state := response.Header.Get(TurnStateHeader); strings.TrimSpace(state) != "" {
			client.turnStates.Put(session.Key, state)
		}
	}
	return response, nil
}

func applyHeaders(headers http.Header, credentials Credentials) error {
	token := strings.TrimSpace(credentials.AccessToken)
	accountID := strings.TrimSpace(credentials.AccountID)
	if token == "" {
		return errors.New("create Codex request: missing access token")
	}
	if accountID == "" {
		return errors.New("create Codex request: missing account ID")
	}
	if invalidHeaderValue(token) || invalidHeaderValue(accountID) {
		return errors.New("create Codex request: credentials contain invalid header characters")
	}

	headers.Set("User-Agent", UserAgent)
	headers.Set("originator", Originator)
	headers.Set("version", ClientVersion)
	headers.Set("OpenAI-Beta", OpenAIBeta)
	headers.Set(AccountIDHeader, accountID)
	headers.Set("Authorization", "Bearer "+token)
	return nil
}

func invalidHeaderValue(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}
