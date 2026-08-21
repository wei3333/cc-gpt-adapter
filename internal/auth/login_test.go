// SPDX-License-Identifier: LGPL-3.0-only

package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoginCompleteMockFlowAndPreserveLocalSecret(t *testing.T) {
	t.Parallel()
	listener := newCallbackListener(t)
	redirectURI := "http://" + listener.Addr().String() + "/auth/callback"
	var exchanges atomic.Int32
	var authorizationChallenge string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		exchanges.Add(1)
		if err := request.ParseForm(); err != nil {
			t.Error(err)
		}
		verifier := request.Form.Get("code_verifier")
		digest := sha256Sum(verifier)
		if digest != authorizationChallenge {
			t.Errorf("PKCE challenge mismatch: %q != %q", digest, authorizationChallenge)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(writer, `{"access_token":"login-access","refresh_token":"login-refresh","id_token":%q,"expires_in":3600}`, testIDToken("acct-login"))
	}))
	defer server.Close()

	oauthClient := newTestOAuthClient(t, server, redirectURI)
	store := newTestStore(t)
	existing := testCredentials(time.Now().Add(time.Hour))
	existing.LocalSecret = "preserve-this-local-secret"
	if err := store.Save(existing); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	opener := func(_ context.Context, authorizationURL string) error {
		parsed, err := url.Parse(authorizationURL)
		if err != nil {
			return err
		}
		authorizationChallenge = parsed.Query().Get("code_challenge")
		go func() {
			response, requestErr := http.Get(redirectURI + "?code=login-code&state=" + url.QueryEscape(parsed.Query().Get("state")))
			if requestErr == nil {
				response.Body.Close()
			}
		}()
		return errors.New("browser unavailable")
	}
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	credentials, err := Login(context.Background(), LoginOptions{
		OAuth: oauthClient, Store: store, Listener: listener, OpenURL: opener,
		Output: &output, Timeout: time.Second, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if credentials.ChatGPTAccountID != "acct-login" || credentials.LocalSecret != existing.LocalSecret {
		t.Fatalf("credentials = %#v", credentials)
	}
	if credentials.ExpiresAt != now.Add(time.Hour) {
		t.Fatalf("expires_at = %v", credentials.ExpiresAt)
	}
	if !strings.Contains(output.String(), "Open this URL") || !strings.Contains(output.String(), "/authorize?") {
		t.Fatalf("browser fallback output = %q", output.String())
	}
	if exchanges.Load() != 1 {
		t.Fatalf("exchange count = %d", exchanges.Load())
	}
	stored, err := store.Load()
	if err != nil || stored != credentials {
		t.Fatalf("stored credentials = %#v, %v", stored, err)
	}
}

func TestLoginStateMismatchNeverExchangesCode(t *testing.T) {
	t.Parallel()
	listener := newCallbackListener(t)
	redirectURI := "http://" + listener.Addr().String() + "/auth/callback"
	var exchanges atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		exchanges.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	opener := func(_ context.Context, _ string) error {
		go func() {
			response, requestErr := http.Get(redirectURI + "?code=stolen&state=wrong")
			if requestErr == nil {
				response.Body.Close()
			}
		}()
		return nil
	}
	_, err := Login(context.Background(), LoginOptions{
		OAuth: newTestOAuthClient(t, server, redirectURI), Store: newTestStore(t),
		Listener: listener, OpenURL: opener, Timeout: time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "state mismatch") {
		t.Fatalf("Login() error = %v", err)
	}
	if exchanges.Load() != 0 {
		t.Fatalf("exchange count = %d, want 0", exchanges.Load())
	}
}

func TestLoginTimeout(t *testing.T) {
	t.Parallel()
	listener := newCallbackListener(t)
	redirectURI := "http://" + listener.Addr().String() + "/auth/callback"
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	_, err := Login(context.Background(), LoginOptions{
		OAuth: newTestOAuthClient(t, server, redirectURI), Store: newTestStore(t), Listener: listener,
		OpenURL: func(context.Context, string) error { return nil }, Timeout: 20 * time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("Login() error = %v", err)
	}
}

func sha256Sum(value string) string {
	digest := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}
