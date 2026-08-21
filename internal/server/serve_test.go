// SPDX-License-Identifier: LGPL-3.0-only

package server

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestNewHTTPServerRequiresIPv4Loopback(t *testing.T) {
	t.Parallel()
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	for _, address := range []string{"localhost:8787", "0.0.0.0:8787", "[::1]:8787", ":8787"} {
		if _, err := NewHTTPServer(address, handler); err == nil {
			t.Fatalf("NewHTTPServer(%q) unexpectedly succeeded", address)
		}
	}
	server, err := NewHTTPServer("127.0.0.1:0", handler)
	if err != nil {
		t.Fatal(err)
	}
	if server.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout = %v; SSE requires it to remain disabled", server.WriteTimeout)
	}
}

func TestServeStopsWhenContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := make(chan error, 1)
	go func() {
		result <- Serve(ctx, "127.0.0.1:0", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not stop after cancellation")
	}
}
