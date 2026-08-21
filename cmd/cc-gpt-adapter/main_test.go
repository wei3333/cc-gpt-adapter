// SPDX-License-Identifier: LGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/cc-gpt-adapter/internal/auth"
	"github.com/Wei-Shaw/cc-gpt-adapter/internal/config"
)

func TestStatusAndLogoutCommands(t *testing.T) {
	directory := t.TempDir()
	t.Setenv(config.ConfigDirEnvironment, directory)
	var output bytes.Buffer
	if err := run(context.Background(), []string{"status"}, &output); err != nil {
		t.Fatalf("status before login: %v", err)
	}
	if output.String() != "Not logged in.\n" {
		t.Fatalf("status output = %q", output.String())
	}

	store, err := auth.NewStore(filepath.Join(directory, config.CredentialsFilename))
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Date(2026, 8, 21, 12, 30, 0, 0, time.UTC)
	if err := store.Save(auth.Credentials{
		Version: auth.CredentialsVersion, AccessToken: "access", RefreshToken: "refresh",
		ExpiresAt: expires, ChatGPTAccountID: "acct-cli", LocalSecret: "local",
	}); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run(context.Background(), []string{"status"}, &output); err != nil {
		t.Fatalf("status after login: %v", err)
	}
	if !strings.Contains(output.String(), "acct-cli") || strings.Contains(output.String(), "access") || strings.Contains(output.String(), "refresh") {
		t.Fatalf("unsafe status output = %q", output.String())
	}

	output.Reset()
	if err := run(context.Background(), []string{"logout"}, &output); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, err := os.Stat(store.Path()); !os.IsNotExist(err) {
		t.Fatalf("credentials still exist after logout: %v", err)
	}
	if !strings.Contains(output.String(), "credentials removed") {
		t.Fatalf("logout output = %q", output.String())
	}
}

func TestCommandUsage(t *testing.T) {
	t.Setenv(config.ConfigDirEnvironment, t.TempDir())
	for _, arguments := range [][]string{nil, {"unknown"}, {"status", "extra"}} {
		if err := run(context.Background(), arguments, &bytes.Buffer{}); err == nil {
			t.Fatalf("run(%v) unexpectedly succeeded", arguments)
		}
	}
}
