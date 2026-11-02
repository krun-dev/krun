package storage

import (
	"context"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/domain"
)

type State = domain.State
type User = domain.User
type Audit = domain.Audit

func memoryStore() *Store { return NewMemory(MemoryOptions{}) }
func TestMemoryReadSnapshotDiscardsMutations(t *testing.T) {
	store := memoryStore()
	store.memory.Users["stable"] = User{ID: "stable", Status: "active"}
	if err := store.Mutate(context.Background(), PartitionIAM, 0, func(st *State) error {
		delete(st.Users, "stable")
		st.Users["temporary"] = User{ID: "temporary", Status: "active"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.memory.Users["stable"]; !ok {
		t.Fatal("read-only snapshot mutation changed stored state")
	}
	if _, ok := store.memory.Users["temporary"]; ok {
		t.Fatal("read-only snapshot inserted a stored user")
	}
}

func TestMemoryAPIKeyLookupReadsOnlyPresentedHash(t *testing.T) {
	store := memoryStore()
	store.memory.KeyHashes["key-a"] = digest("krun_key_presented")
	id, ok, err := store.APIKeyIDByHash(context.Background(), digest("krun_key_presented"))
	if err != nil || !ok || id != "key-a" {
		t.Fatalf("lookup=(%q,%v,%v), want key-a", id, ok, err)
	}
	if _, ok, err = store.APIKeyIDByHash(context.Background(), digest("krun_key_unknown")); err != nil || ok {
		t.Fatalf("unknown lookup=(%v,%v), want missing", ok, err)
	}
}

func TestAuditRetentionBoundary(t *testing.T) {
	at := now()
	cutoff := at.Add(-AuditRetention)
	kept := PruneMemoryAudits([]Audit{{ID: "expired", Time: cutoff.Add(-time.Nanosecond)}, {ID: "boundary", Time: cutoff}, {ID: "recent", Time: at}}, at)
	if len(kept) != 2 || kept[0].ID != "boundary" || kept[1].ID != "recent" {
		t.Fatal("wrong retention boundary")
	}
}

func TestAuditRetentionDuringIdleTime(t *testing.T) {
	store := memoryStore()
	store.memoryAudits = []Audit{{ID: "expired", Time: now().Add(-8 * 24 * time.Hour)}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); store.RunRetention(ctx, time.Millisecond, nil) }()
	defer func() { cancel(); <-done }()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("idle retention did not delete expired data")
		case <-ticker.C:
			store.mu.Lock()
			count := len(store.memoryAudits)
			store.mu.Unlock()
			if count == 0 {
				return
			}
		}
	}
}

func TestMemoryRateLimitCounterStopsGrowing(t *testing.T) {
	store := memoryStore()
	for i := 0; i < 40; i++ {
		exceeded, err := store.RateLimitExceeded(context.Background(), "scope", "key", time.Minute, 30)
		if err != nil || exceeded != (i >= 30) {
			t.Fatalf("request=%d exceeded=%v err=%v", i, exceeded, err)
		}
	}
	for _, count := range store.memoryRateLimits {
		if count > 31 {
			t.Fatalf("counter grew after rejection: %d", count)
		}
	}
}
