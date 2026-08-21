// SPDX-License-Identifier: LGPL-3.0-only

package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTokenManagerRefreshRotationAndConcurrency(t *testing.T) {
	t.Parallel()
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		refreshes.Add(1)
		if err := request.ParseForm(); err != nil {
			t.Error(err)
		}
		if request.Form.Get("refresh_token") != "refresh-old" {
			t.Errorf("refresh token = %q", request.Form.Get("refresh_token"))
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(writer, `{"access_token":"access-new","refresh_token":"refresh-new","id_token":%q,"expires_in":3600}`, testIDToken("acct-new"))
	}))
	defer server.Close()

	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	store := newTestStore(t)
	credentials := testCredentials(now.Add(time.Minute))
	if err := store.Save(credentials); err != nil {
		t.Fatal(err)
	}
	manager, err := NewTokenManager(store, newTestOAuthClient(t, server, "http://localhost/callback"))
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return now }

	const goroutines = 32
	var group sync.WaitGroup
	errorsChannel := make(chan error, goroutines)
	for range goroutines {
		group.Add(1)
		go func() {
			defer group.Done()
			access, err := manager.GetValidToken(context.Background())
			if err == nil && (access.AccessToken != "access-new" || access.ChatGPTAccountID != "acct-new") {
				err = fmt.Errorf("unexpected access: %#v", access)
			}
			errorsChannel <- err
		}()
	}
	group.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatal(err)
		}
	}
	if refreshes.Load() != 1 {
		t.Fatalf("refresh count = %d, want 1", refreshes.Load())
	}
	saved, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if saved.RefreshToken != "refresh-new" || saved.LocalSecret != credentials.LocalSecret {
		t.Fatalf("saved credentials = %#v", saved)
	}
}

func TestTokenManagerPreservesRefreshTokenWhenOmitted(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"access_token":"access-new","expires_in":3600}`))
	}))
	defer server.Close()
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	store := newTestStore(t)
	if err := store.Save(testCredentials(now.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	manager, _ := NewTokenManager(store, newTestOAuthClient(t, server, "http://localhost/callback"))
	manager.now = func() time.Time { return now }
	if _, err := manager.GetValidToken(context.Background()); err != nil {
		t.Fatalf("GetValidToken() error = %v", err)
	}
	saved, _ := store.Load()
	if saved.RefreshToken != "refresh-old" {
		t.Fatalf("refresh token = %q, want preserved old token", saved.RefreshToken)
	}
}

func TestTokenManagerForceRefreshDeduplicatesRejectedToken(t *testing.T) {
	t.Parallel()
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		refreshes.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"access_token":"access-after-401","expires_in":3600}`))
	}))
	defer server.Close()
	store := newTestStore(t)
	if err := store.Save(testCredentials(time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	manager, _ := NewTokenManager(store, newTestOAuthClient(t, server, "http://localhost/callback"))
	var group sync.WaitGroup
	for range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			if access, err := manager.ForceRefresh(context.Background(), "access-old"); err != nil || access.AccessToken != "access-after-401" {
				t.Errorf("ForceRefresh() = %#v, %v", access, err)
			}
		}()
	}
	group.Wait()
	if refreshes.Load() != 1 {
		t.Fatalf("force refresh count = %d, want 1", refreshes.Load())
	}
}

func TestTokenManagerReturnsTokenOutsideRefreshWindow(t *testing.T) {
	t.Parallel()
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		refreshes.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	store := newTestStore(t)
	if err := store.Save(testCredentials(now.Add(RefreshBeforeExpiry + time.Second))); err != nil {
		t.Fatal(err)
	}
	manager, _ := NewTokenManager(store, newTestOAuthClient(t, server, "http://localhost/callback"))
	manager.now = func() time.Time { return now }
	access, err := manager.GetValidToken(context.Background())
	if err != nil || access.AccessToken != "access-old" {
		t.Fatalf("GetValidToken() = %#v, %v", access, err)
	}
	if refreshes.Load() != 0 {
		t.Fatalf("refresh count = %d, want 0", refreshes.Load())
	}
	if _, err := manager.ForceRefresh(context.Background(), ""); err == nil {
		t.Fatal("ForceRefresh with empty rejected token unexpectedly succeeded")
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "private", "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}
