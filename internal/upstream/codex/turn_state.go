// SPDX-License-Identifier: LGPL-3.0-only

package codex

import (
	"strings"
	"sync"
	"time"
)

const (
	TurnStateHeader     = "x-codex-turn-state"
	DefaultTurnStateTTL = 30 * time.Minute
)

type turnStateEntry struct {
	value     string
	expiresAt time.Time
}

// TurnStateStore keeps Codex's opaque continuation state isolated by the
// derived Claude session. It is intentionally process-local in phase 3.
type TurnStateStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	entries map[string]turnStateEntry
}

func NewTurnStateStore(ttl time.Duration) *TurnStateStore {
	if ttl <= 0 {
		ttl = DefaultTurnStateTTL
	}
	return &TurnStateStore{
		ttl: ttl, now: time.Now, entries: make(map[string]turnStateEntry),
	}
}

func (store *TurnStateStore) Get(sessionKey string) (string, bool) {
	if store == nil || strings.TrimSpace(sessionKey) == "" {
		return "", false
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, ok := store.entries[sessionKey]
	if !ok {
		return "", false
	}
	if !store.now().Before(entry.expiresAt) {
		delete(store.entries, sessionKey)
		return "", false
	}
	return entry.value, true
}

func (store *TurnStateStore) Put(sessionKey, state string) {
	if store == nil || strings.TrimSpace(sessionKey) == "" {
		return
	}
	state = strings.TrimSpace(state)
	store.mu.Lock()
	defer store.mu.Unlock()
	if state == "" {
		delete(store.entries, sessionKey)
		return
	}
	store.entries[sessionKey] = turnStateEntry{
		value: state, expiresAt: store.now().Add(store.ttl),
	}
}

func (store *TurnStateStore) Delete(sessionKey string) {
	if store == nil {
		return
	}
	store.mu.Lock()
	delete(store.entries, sessionKey)
	store.mu.Unlock()
}
