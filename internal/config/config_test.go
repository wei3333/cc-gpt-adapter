// SPDX-License-Identifier: LGPL-3.0-only

package config

import (
	"path/filepath"
	"testing"
)

func TestDefault(t *testing.T) {
	t.Parallel()

	got := Default()
	if got.ListenAddress != "127.0.0.1:8787" {
		t.Fatalf("ListenAddress = %q, want loopback default", got.ListenAddress)
	}
	if got.UpstreamModel != "gpt-5.6-sol" {
		t.Fatalf("UpstreamModel = %q, want gpt-5.6-sol", got.UpstreamModel)
	}
}

func TestCredentialsPathOverride(t *testing.T) {
	directory := t.TempDir()
	t.Setenv(ConfigDirEnvironment, directory)
	got, err := CredentialsPath()
	if err != nil {
		t.Fatalf("CredentialsPath() error = %v", err)
	}
	if want := filepath.Join(directory, CredentialsFilename); got != want {
		t.Fatalf("CredentialsPath() = %q, want %q", got, want)
	}
}
