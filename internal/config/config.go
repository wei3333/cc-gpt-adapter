// SPDX-License-Identifier: LGPL-3.0-only

// Package config defines the adapter's local runtime configuration.
package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const (
	// DefaultListenAddress limits the future HTTP server to the loopback interface.
	DefaultListenAddress = "127.0.0.1:8787"
	// DefaultUpstreamModel is the initial Codex model selected by the design.
	DefaultUpstreamModel = "gpt-5.6-sol"
	// ConfigDirEnvironment overrides the per-user configuration directory.
	ConfigDirEnvironment = "CC_GPT_ADAPTER_CONFIG_DIR"
	CredentialsFilename  = "credentials.json"
)

// Config contains the minimal runtime settings planned for the adapter.
type Config struct {
	ListenAddress string
	UpstreamModel string
}

// CredentialsPath returns the single-account credential file location.
func CredentialsPath() (string, error) {
	if override := strings.TrimSpace(os.Getenv(ConfigDirEnvironment)); override != "" {
		return filepath.Join(override, CredentialsFilename), nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(base) == "" {
		return "", errors.New("user configuration directory is empty")
	}
	return filepath.Join(base, "cc-gpt-adapter", CredentialsFilename), nil
}

// Default returns the phase-0 configuration defaults.
func Default() Config {
	return Config{
		ListenAddress: DefaultListenAddress,
		UpstreamModel: DefaultUpstreamModel,
	}
}
