package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/krun-dev/krun/internal/domain"
)

func TestBuildLogPublicationRejectsStaleAndTerminalOwners(t *testing.T) {
	testBuildLogPublication(t, postgresTestStore(t))
}

func testBuildLogPublication(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	if err := s.Update(ctx, PartitionWorkers, func(st *domain.State) error {
		st.Workers.Workers["worker"] = domain.Worker{
			Worker: "worker", Organization: "org",
			Environments: map[string]domain.WorkerEnvironment{},
		}
		st.Workers.Builds["b"] = domain.WorkerBuild{
			ID: "b", Organization: "org", Worker: "worker", Environment: "production",
			State: "queued", Created: now(),
		}
		st.Workers.BuildPrivate["b"] = domain.WorkerBuildPrivate{
			Actor: domain.WorkerActor{User: "actor", AuthVersion: 1},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if claimed, err := s.ClaimBuild(ctx, "b", "new"); err != nil || !claimed {
		t.Fatalf("claim build: %v %v", claimed, err)
	}
	if err := s.SetClaimedBuildLogKey(ctx, "b", "old", "stale"); !errors.Is(err, ErrBuildClaimLost) {
		t.Fatal(err)
	}
	if err := s.SetClaimedBuildLogKey(ctx, "b", "new", "live"); err != nil {
		t.Fatal(err)
	}
	pending, err := s.PendingWork(context.WithValue(ctx, WorkerQueueReplicaKey{}, "new"), 100)
	if err != nil || pending.BuildPrivate["b"].LogKey != "live" {
		t.Fatalf("queue lost log object key: %+v %v", pending.BuildPrivate["b"], err)
	}
	build := pending.Builds["b"]
	build.Entrypoint = "index.ts"
	private := pending.BuildPrivate["b"]
	if err := s.StoreClaimedRepositoryBuild(ctx, "b", "new", build, private); err != nil {
		t.Fatal("save prepared build metadata:", err)
	}
	if err := s.FinishClaimedBuild(ctx, "b", "new", "failed", "test", "", "final"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetClaimedBuildLogKey(ctx, "b", "new", "late"); !errors.Is(err, ErrBuildClaimLost) {
		t.Fatal(err)
	}
	p, _, _ := s.WorkerBuildPrivate(ctx, "b")
	if p.LogKey != "final" {
		t.Fatalf("unexpected private log: %+v", p)
	}
}
