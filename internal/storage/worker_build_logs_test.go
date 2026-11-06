package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/krun-dev/krun/internal/domain"
)

func TestBuildLogPublicationRejectsStaleAndTerminalOwners(t *testing.T) {
	s := memoryStore()
	s.memory.Workers.Builds["b"] = domain.WorkerBuild{ID: "b", State: "building", ClaimedBy: "new"}
	s.memory.Workers.BuildPrivate["b"] = domain.WorkerBuildPrivate{}
	ctx := context.Background()
	if err := s.SetClaimedBuildLogKey(ctx, "b", "old", "stale"); !errors.Is(err, ErrBuildClaimLost) {
		t.Fatal(err)
	}
	if err := s.SetClaimedBuildLogKey(ctx, "b", "new", "live"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishClaimedBuild(ctx, "b", "new", "failed", "test", "", "final"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetClaimedBuildLogKey(ctx, "b", "new", "late"); !errors.Is(err, ErrBuildClaimLost) {
		t.Fatal(err)
	}
	p, _, _ := s.WorkerBuildPrivate(ctx, "b")
	if p.Log != "" || p.LogKey != "final" {
		t.Fatalf("unexpected private log: %+v", p)
	}
}
