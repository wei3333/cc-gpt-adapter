// SPDX-License-Identifier: LGPL-3.0-only

package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

const DefaultListenAddress = "127.0.0.1:8787"

func NewHTTPServer(address string, handler http.Handler) (*http.Server, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		address = DefaultListenAddress
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" || port == "" {
		return nil, errors.New("create HTTP server: listen address must be 127.0.0.1:<port>")
	}
	if handler == nil {
		return nil, errors.New("create HTTP server: handler is required")
	}
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}, nil
}

// Serve runs until the context is canceled and then performs a bounded
// graceful shutdown. WriteTimeout remains disabled for long SSE responses.
func Serve(ctx context.Context, address string, handler http.Handler) error {
	if ctx == nil {
		ctx = context.Background()
	}
	server, err := NewHTTPServer(address, handler)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", server.Addr, err)
	}
	shutdownSignal, stopShutdown := context.WithCancel(ctx)
	defer stopShutdown()
	shutdownComplete := make(chan struct{})
	go func() {
		defer close(shutdownComplete)
		<-shutdownSignal.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
	}()
	err = server.Serve(listener)
	stopShutdown()
	<-shutdownComplete
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("serve on %s: %w", server.Addr, err)
}
