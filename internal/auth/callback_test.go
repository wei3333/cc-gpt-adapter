// SPDX-License-Identifier: LGPL-3.0-only

package auth

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestWaitForCallbackSuccess(t *testing.T) {
	t.Parallel()
	listener := newCallbackListener(t)
	redirectURI := "http://" + listener.Addr().String() + "/auth/callback"
	result := make(chan struct {
		code string
		err  error
	}, 1)
	go func() {
		code, err := WaitForCallback(context.Background(), listener, redirectURI, "expected-state")
		result <- struct {
			code string
			err  error
		}{code, err}
	}()
	response, err := http.Get(redirectURI + "?code=auth-code&state=expected-state")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "Login complete") {
		t.Fatalf("callback response = %d %q", response.StatusCode, body)
	}
	got := <-result
	if got.err != nil || got.code != "auth-code" {
		t.Fatalf("WaitForCallback() = %q, %v", got.code, got.err)
	}
}

func TestWaitForCallbackRejectsStateMismatch(t *testing.T) {
	t.Parallel()
	listener := newCallbackListener(t)
	redirectURI := "http://" + listener.Addr().String() + "/auth/callback"
	result := make(chan error, 1)
	go func() {
		_, err := WaitForCallback(context.Background(), listener, redirectURI, "expected")
		result <- err
	}()
	response, err := http.Get(redirectURI + "?code=must-not-exchange&state=wrong")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.StatusCode)
	}
	if err := <-result; err == nil || !strings.Contains(err.Error(), "state mismatch") {
		t.Fatalf("WaitForCallback() error = %v", err)
	}
}

func TestWaitForCallbackTimeout(t *testing.T) {
	t.Parallel()
	listener := newCallbackListener(t)
	contextWithTimeout, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := WaitForCallback(contextWithTimeout, listener, "http://localhost/auth/callback", "state")
	if err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("WaitForCallback() error = %v", err)
	}
}

func TestValidateCallbackProviderErrorIsSanitized(t *testing.T) {
	t.Parallel()
	request := &http.Request{URL: &url.URL{RawQuery: "state=s&error=bad%0Asecret"}}
	result := validateCallback(request, "s")
	if result.err == nil || strings.Contains(result.err.Error(), "secret") {
		t.Fatalf("unsafe callback error = %v", result.err)
	}
}

func newCallbackListener(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return listener
}
