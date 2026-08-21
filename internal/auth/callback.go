// SPDX-License-Identifier: LGPL-3.0-only

package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type callbackResult struct {
	code string
	err  error
}

// WaitForCallback serves exactly one OAuth callback attempt. A state mismatch
// consumes the flow, so the authorization code can never be replayed through
// the same listener.
func WaitForCallback(ctx context.Context, listener net.Listener, redirectURI, expectedState string) (string, error) {
	if listener == nil {
		return "", errors.New("wait for OAuth callback: nil listener")
	}
	defer listener.Close()
	redirect, err := url.Parse(redirectURI)
	if err != nil || redirect.Path == "" {
		return "", errors.New("wait for OAuth callback: redirect URI has no valid path")
	}
	if strings.TrimSpace(expectedState) == "" {
		return "", errors.New("wait for OAuth callback: expected state is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	results := make(chan callbackResult, 1)
	var consume sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc(redirect.Path, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", http.MethodGet)
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handled := false
		consume.Do(func() {
			handled = true
			result := validateCallback(request, expectedState)
			if result.err != nil {
				http.Error(writer, "OAuth login failed. You may close this window.", http.StatusBadRequest)
			} else {
				writer.Header().Set("Content-Type", "text/html; charset=utf-8")
				writer.WriteHeader(http.StatusOK)
				_, _ = writer.Write([]byte("<!doctype html><title>Login complete</title><p>Login complete. You may close this window.</p>"))
			}
			results <- result
		})
		if !handled {
			http.Error(writer, "OAuth callback already consumed", http.StatusGone)
		}
	})

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       5 * time.Second,
	}
	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- server.Serve(listener)
	}()

	var result callbackResult
	select {
	case result = <-results:
	case <-ctx.Done():
		result.err = fmt.Errorf("wait for OAuth callback: %w", ctx.Err())
	case serveErr := <-serveErrors:
		if errors.Is(serveErr, http.ErrServerClosed) {
			result.err = errors.New("wait for OAuth callback: server closed before callback")
		} else {
			result.err = fmt.Errorf("serve OAuth callback: %w", serveErr)
		}
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownContext)
	if result.err != nil {
		return "", result.err
	}
	return result.code, nil
}

func validateCallback(request *http.Request, expectedState string) callbackResult {
	query := request.URL.Query()
	actualState := query.Get("state")
	if subtle.ConstantTimeCompare([]byte(actualState), []byte(expectedState)) != 1 {
		return callbackResult{err: errors.New("OAuth callback state mismatch")}
	}
	if providerError := safeOAuthErrorCode(query.Get("error")); providerError != "" {
		return callbackResult{err: fmt.Errorf("OAuth provider returned %s", providerError)}
	}
	code := strings.TrimSpace(query.Get("code"))
	if code == "" {
		return callbackResult{err: errors.New("OAuth callback did not contain an authorization code")}
	}
	return callbackResult{code: code}
}

func safeOAuthErrorCode(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-') {
			return "oauth_error"
		}
	}
	return value
}
