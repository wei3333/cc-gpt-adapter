// SPDX-License-Identifier: LGPL-3.0-only

package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestBeginAuthorizationUsesCodexPKCEParameters(t *testing.T) {
	t.Parallel()
	client, err := NewOAuthClient(DefaultOAuthConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	flow, err := client.BeginAuthorization()
	if err != nil {
		t.Fatalf("BeginAuthorization() error = %v", err)
	}
	parsed, err := url.Parse(flow.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	for key, want := range map[string]string{
		"response_type":              "code",
		"client_id":                  ClientID,
		"redirect_uri":               DefaultRedirectURI,
		"scope":                      DefaultScopes,
		"state":                      flow.State,
		"code_challenge":             flow.CodeChallenge,
		"code_challenge_method":      "S256",
		"id_token_add_organizations": "true",
		"codex_cli_simplified_flow":  "true",
	} {
		if got := query.Get(key); got != want {
			t.Fatalf("query %s = %q, want %q", key, got, want)
		}
	}
	if len(flow.State) != 64 || len(flow.CodeVerifier) != 128 {
		t.Fatalf("state/verifier lengths = %d/%d", len(flow.State), len(flow.CodeVerifier))
	}
	digest := sha256.Sum256([]byte(flow.CodeVerifier))
	if want := base64.RawURLEncoding.EncodeToString(digest[:]); flow.CodeChallenge != want {
		t.Fatalf("code challenge = %q, want %q", flow.CodeChallenge, want)
	}
}

func TestOAuthClientExchangeAndRefreshForms(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Header.Get("User-Agent") == "" || request.Header.Get("originator") == "" {
			t.Error("missing Codex auth identity")
		}
		if request.Header.Get("version") != "" {
			t.Errorf("auth request unexpectedly carried version %q", request.Header.Get("version"))
		}
		if err := request.ParseForm(); err != nil {
			t.Error(err)
		}
		writer.Header().Set("Content-Type", "application/json")
		switch request.Form.Get("grant_type") {
		case "authorization_code":
			if request.Form.Get("code") != "auth-code" || request.Form.Get("code_verifier") != "verifier" ||
				request.Form.Get("redirect_uri") != "http://localhost/callback" {
				t.Errorf("exchange form = %v", request.Form)
			}
			_, _ = fmt.Fprintf(writer, `{"access_token":"access-one","refresh_token":"refresh-one","id_token":%q,"expires_in":3600}`, testIDToken("acct-one"))
		case "refresh_token":
			if request.Form.Get("refresh_token") != "refresh-one" || request.Form.Get("scope") != RefreshScopes {
				t.Errorf("refresh form = %v", request.Form)
			}
			_, _ = writer.Write([]byte(`{"access_token":"access-two","expires_in":1800}`))
		default:
			t.Errorf("unexpected grant_type %q", request.Form.Get("grant_type"))
		}
	}))
	defer server.Close()

	client := newTestOAuthClient(t, server, "http://localhost/callback")
	exchanged, err := client.ExchangeCode(context.Background(), "auth-code", "verifier")
	if err != nil {
		t.Fatalf("ExchangeCode() error = %v", err)
	}
	if exchanged.AccessToken != "access-one" || exchanged.RefreshToken != "refresh-one" {
		t.Fatalf("exchange token = %#v", exchanged)
	}
	refreshed, err := client.Refresh(context.Background(), exchanged.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if refreshed.AccessToken != "access-two" || refreshed.RefreshToken != "" {
		t.Fatalf("refresh token = %#v", refreshed)
	}
	if requests.Load() != 2 {
		t.Fatalf("token request count = %d, want 2", requests.Load())
	}
}

func TestOAuthTokenErrorDoesNotLeakResponseSecrets(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"error":"invalid_grant","access_token":"do-not-leak","refresh_token":"also-secret"}`))
	}))
	defer server.Close()
	client := newTestOAuthClient(t, server, "http://localhost/callback")
	_, err := client.ExchangeCode(context.Background(), "code", "verifier")
	if err == nil {
		t.Fatal("ExchangeCode unexpectedly succeeded")
	}
	message := err.Error()
	if !strings.Contains(message, "invalid_grant") || strings.Contains(message, "do-not-leak") || strings.Contains(message, "also-secret") {
		t.Fatalf("unsafe OAuth error: %q", message)
	}
}

func TestAccountIDFromIDToken(t *testing.T) {
	t.Parallel()
	if got, err := AccountIDFromIDToken(testIDToken("acct-test")); err != nil || got != "acct-test" {
		t.Fatalf("AccountIDFromIDToken() = %q, %v", got, err)
	}
	for _, token := range []string{"", "one.two", testIDToken("")} {
		if _, err := AccountIDFromIDToken(token); err == nil {
			t.Fatalf("AccountIDFromIDToken(%q) unexpectedly succeeded", token)
		}
	}
}

func TestLocalSecretIsRandomAndURLSafe(t *testing.T) {
	t.Parallel()
	first, err := newLocalSecret()
	if err != nil {
		t.Fatal(err)
	}
	second, err := newLocalSecret()
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) != 43 || strings.ContainsAny(first, "+/=") {
		t.Fatalf("unexpected local secrets: %q, %q", first, second)
	}
}

func newTestOAuthClient(t *testing.T, server *httptest.Server, redirectURI string) *OAuthClient {
	t.Helper()
	client, err := NewOAuthClient(OAuthConfig{
		ClientID: "test-client", AuthorizeURL: server.URL + "/authorize",
		TokenURL: server.URL + "/token", RedirectURI: redirectURI,
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func testIDToken(accountID string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	claims, _ := json.Marshal(map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": accountID},
	})
	payload := base64.RawURLEncoding.EncodeToString(claims)
	return header + "." + payload + ".signature"
}
