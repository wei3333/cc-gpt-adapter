// SPDX-License-Identifier: LGPL-3.0-only

package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreAtomicRoundTripPermissionsAndDelete(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "private")
	store, err := NewStore(filepath.Join(directory, "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := testCredentials(time.Now().Add(time.Hour))
	if err := store.Save(want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	directoryInfo, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if directoryInfo.Mode().Perm() != 0o700 || fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("directory/file modes = %04o/%04o", directoryInfo.Mode().Perm(), fileInfo.Mode().Perm())
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Version != want.Version || got.AccessToken != want.AccessToken ||
		got.RefreshToken != want.RefreshToken || got.ChatGPTAccountID != want.ChatGPTAccountID ||
		got.LocalSecret != want.LocalSecret || !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "credentials.json" {
		t.Fatalf("unexpected files after atomic save: %#v", entries)
	}
	if err := store.Delete(); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := store.Load(); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("Load() after delete error = %v", err)
	}
	if err := store.Delete(); err != nil {
		t.Fatalf("idempotent Delete() error = %v", err)
	}
}

func TestStoreRejectsInsecureFileAndSymlink(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "credentials.json")
	store, _ := NewStore(path)
	body := `{"version":1,"access_token":"a","refresh_token":"r","expires_at":"2030-01-01T00:00:00Z","chatgpt_account_id":"acct","local_secret":"local"}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil || !strings.Contains(err.Error(), "insecure permissions") {
		t.Fatalf("Load() error = %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlink Load() error = %v", err)
	}
}

func TestStoreRejectsInvalidCredentials(t *testing.T) {
	t.Parallel()
	store, _ := NewStore(filepath.Join(t.TempDir(), "credentials.json"))
	for _, credentials := range []Credentials{{}, {Version: 99}} {
		if err := store.Save(credentials); err == nil {
			t.Fatalf("Save(%#v) unexpectedly succeeded", credentials)
		}
	}
}

func testCredentials(expiresAt time.Time) Credentials {
	return Credentials{
		Version: CredentialsVersion, AccessToken: "access-old", RefreshToken: "refresh-old",
		ExpiresAt: expiresAt, ChatGPTAccountID: "acct-old", LocalSecret: "local-secret",
	}
}
