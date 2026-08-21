// SPDX-License-Identifier: LGPL-3.0-only

// Command cc-gpt-adapter is the local Claude Code to Codex adapter.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Wei-Shaw/cc-gpt-adapter/internal/auth"
	"github.com/Wei-Shaw/cc-gpt-adapter/internal/config"
	adapterserver "github.com/Wei-Shaw/cc-gpt-adapter/internal/server"
	"github.com/Wei-Shaw/cc-gpt-adapter/internal/upstream/codex"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "cc-gpt-adapter:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string, output io.Writer) error {
	if len(arguments) != 1 {
		return errors.New("usage: cc-gpt-adapter <login|serve|status|logout>")
	}
	credentialPath, err := config.CredentialsPath()
	if err != nil {
		return fmt.Errorf("resolve credential path: %w", err)
	}
	store, err := auth.NewStore(credentialPath)
	if err != nil {
		return err
	}

	switch arguments[0] {
	case "login":
		oauthClient, err := auth.NewOAuthClient(auth.DefaultOAuthConfig(), nil)
		if err != nil {
			return err
		}
		credentials, err := auth.Login(ctx, auth.LoginOptions{
			OAuth: oauthClient, Store: store, Output: output,
		})
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(output, "Login complete for account %s.\n", credentials.ChatGPTAccountID)
		return nil
	case "status":
		credentials, err := store.Load()
		if errors.Is(err, auth.ErrNotLoggedIn) {
			_, _ = fmt.Fprintln(output, "Not logged in.")
			return nil
		}
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(output, "Logged in as %s. Token expires at %s.\n",
			credentials.ChatGPTAccountID, credentials.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z"))
		return nil
	case "logout":
		if err := store.Delete(); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(output, "Logged out; local credentials removed.")
		return nil
	case "serve":
		credentials, err := store.Load()
		if err != nil {
			return fmt.Errorf("load credentials for serve: %w", err)
		}
		oauthClient, err := auth.NewOAuthClient(auth.DefaultOAuthConfig(), nil)
		if err != nil {
			return err
		}
		tokenManager, err := auth.NewTokenManager(store, oauthClient)
		if err != nil {
			return err
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.ResponseHeaderTimeout = 30 * time.Second
		transport.TLSHandshakeTimeout = 10 * time.Second
		upstreamClient, err := codex.NewClient(codex.DefaultEndpoint, &http.Client{Transport: transport}, nil)
		if err != nil {
			return err
		}
		handler, err := adapterserver.New(adapterserver.Config{
			LocalSecret: credentials.LocalSecret,
			Tokens:      tokenManager,
			Upstream:    upstreamClient,
		})
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(output, "Listening on http://%s\n", config.DefaultListenAddress)
		return adapterserver.Serve(ctx, config.DefaultListenAddress, handler)
	default:
		return fmt.Errorf("unknown command %q; usage: cc-gpt-adapter <login|serve|status|logout>", arguments[0])
	}
}
