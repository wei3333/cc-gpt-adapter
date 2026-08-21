// SPDX-License-Identifier: LGPL-3.0-only

package codex

import (
	"sync"
	"testing"
	"time"
)

func TestTurnStateStoreIsolationExpiryAndDelete(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	store := NewTurnStateStore(time.Minute)
	store.now = func() time.Time { return now }

	store.Put("session-a", " state-a ")
	store.Put("session-b", "state-b")
	if got, ok := store.Get("session-a"); !ok || got != "state-a" {
		t.Fatalf("session-a state = %q, %v", got, ok)
	}
	if got, ok := store.Get("session-b"); !ok || got != "state-b" {
		t.Fatalf("session-b state = %q, %v", got, ok)
	}

	now = now.Add(time.Minute)
	if _, ok := store.Get("session-a"); ok {
		t.Fatal("expired state was returned")
	}
	store.Put("session-b", "")
	if _, ok := store.Get("session-b"); ok {
		t.Fatal("empty state did not delete entry")
	}

	store.Put("session-c", "state-c")
	store.Delete("session-c")
	if _, ok := store.Get("session-c"); ok {
		t.Fatal("Delete did not remove entry")
	}
}

func TestTurnStateStoreConcurrentAccess(t *testing.T) {
	t.Parallel()
	store := NewTurnStateStore(time.Minute)
	var group sync.WaitGroup
	for index := range 32 {
		group.Add(1)
		go func() {
			defer group.Done()
			key := string(rune('a' + index%4))
			store.Put(key, "opaque")
			store.Get(key)
		}()
	}
	group.Wait()
}
