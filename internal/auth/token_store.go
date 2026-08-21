// SPDX-License-Identifier: LGPL-3.0-only

package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const CredentialsVersion = 1

var ErrNotLoggedIn = errors.New("not logged in")

type Credentials struct {
	Version          int       `json:"version"`
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token"`
	ExpiresAt        time.Time `json:"expires_at"`
	ChatGPTAccountID string    `json:"chatgpt_account_id"`
	LocalSecret      string    `json:"local_secret"`
}

type Store struct {
	path string
}

func NewStore(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("create credential store: path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("create credential store: %w", err)
	}
	return &Store{path: absolute}, nil
}

func (store *Store) Path() string {
	if store == nil {
		return ""
	}
	return store.path
}

func (store *Store) Load() (Credentials, error) {
	if store == nil {
		return Credentials{}, errors.New("load credentials: nil store")
	}
	info, err := os.Lstat(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return Credentials{}, ErrNotLoggedIn
	}
	if err != nil {
		return Credentials{}, fmt.Errorf("inspect credentials: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Credentials{}, errors.New("load credentials: file is not a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return Credentials{}, fmt.Errorf("load credentials: insecure permissions %04o, want 0600", info.Mode().Perm())
	}
	file, err := os.Open(store.path)
	if err != nil {
		return Credentials{}, fmt.Errorf("open credentials: %w", err)
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return Credentials{}, fmt.Errorf("read credentials: %w", err)
	}
	if len(body) > 1<<20 {
		return Credentials{}, errors.New("read credentials: file exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var credentials Credentials
	if err := decoder.Decode(&credentials); err != nil {
		return Credentials{}, fmt.Errorf("decode credentials: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Credentials{}, errors.New("decode credentials: multiple JSON values")
	}
	if err := validateCredentials(credentials); err != nil {
		return Credentials{}, err
	}
	return credentials, nil
}

func (store *Store) Save(credentials Credentials) error {
	if store == nil {
		return errors.New("save credentials: nil store")
	}
	if err := validateCredentials(credentials); err != nil {
		return err
	}
	directory := filepath.Dir(store.path)
	if err := ensurePrivateDirectory(directory); err != nil {
		return err
	}
	body, err := json.MarshalIndent(credentials, "", "  ")
	if err != nil {
		return fmt.Errorf("encode credentials: %w", err)
	}
	body = append(body, '\n')

	temporary, err := os.CreateTemp(directory, ".credentials-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary credentials: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	closeTemporary := func() {
		_ = temporary.Close()
	}
	if err := temporary.Chmod(0o600); err != nil {
		closeTemporary()
		return fmt.Errorf("protect temporary credentials: %w", err)
	}
	if _, err := temporary.Write(body); err != nil {
		closeTemporary()
		return fmt.Errorf("write temporary credentials: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		closeTemporary()
		return fmt.Errorf("sync temporary credentials: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary credentials: %w", err)
	}
	if err := os.Rename(temporaryPath, store.path); err != nil {
		return fmt.Errorf("replace credentials atomically: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		return fmt.Errorf("sync credential directory: %w", err)
	}
	return nil
}

func (store *Store) Delete() error {
	if store == nil {
		return errors.New("delete credentials: nil store")
	}
	err := os.Remove(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete credentials: %w", err)
	}
	if err := syncDirectory(filepath.Dir(store.path)); err != nil {
		return fmt.Errorf("sync credential directory: %w", err)
	}
	return nil
}

func validateCredentials(credentials Credentials) error {
	if credentials.Version != CredentialsVersion {
		return fmt.Errorf("validate credentials: unsupported version %d", credentials.Version)
	}
	if strings.TrimSpace(credentials.AccessToken) == "" || strings.TrimSpace(credentials.RefreshToken) == "" ||
		strings.TrimSpace(credentials.ChatGPTAccountID) == "" || strings.TrimSpace(credentials.LocalSecret) == "" ||
		credentials.ExpiresAt.IsZero() {
		return errors.New("validate credentials: required field is missing")
	}
	for _, value := range []string{
		credentials.AccessToken, credentials.RefreshToken, credentials.ChatGPTAccountID, credentials.LocalSecret,
	} {
		for _, character := range value {
			if character < 0x20 || character == 0x7f {
				return errors.New("validate credentials: field contains control characters")
			}
		}
	}
	return nil
}

func ensurePrivateDirectory(directory string) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create credential directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect credential directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("credential directory is not a regular directory")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("protect credential directory: %w", err)
	}
	return nil
}

func syncDirectory(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}
