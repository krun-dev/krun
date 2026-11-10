package storage

import (
	"testing"

	"github.com/krun-dev/krun/internal/domain"
)

func TestReserveBuildArtifactReusesExistingContentIdentity(t *testing.T) {
	t.Run("memory", func(t *testing.T) { testReserveBuildArtifact(t, memoryStore()) })
	t.Run("postgres", func(t *testing.T) { testReserveBuildArtifact(t, postgresTestStore(t)) })
}

func testReserveBuildArtifact(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	existing := domain.WorkerArtifact{
		ID: "art_existing", Organization: "org", State: "finalized",
		ObjectKey: "bundle-existing", ArchiveDigest: "existing", Created: now(),
	}
	fresh := domain.WorkerArtifact{
		ID: "art-fresh", Organization: "org", State: "finalized",
		ObjectKey: "bundle-fresh", ArchiveDigest: "fresh", Created: now(),
	}
	if err := s.Update(ctx, PartitionWorkers, func(st *domain.State) error {
		st.Workers.Workers["worker"] = domain.Worker{
			Worker: "worker", Organization: "org",
			Environments: map[string]domain.WorkerEnvironment{},
		}
		st.Workers.Artifacts[existing.ID] = existing
		for _, id := range []string{"reuse", "new"} {
			st.Workers.Builds[id] = domain.WorkerBuild{
				ID: id, Organization: "org", Worker: "worker", Environment: "production",
				State: "queued", Created: now(),
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	reused := existing
	reused.ID = "art-existing"
	for _, tc := range []struct {
		build, previousID, want string
		artifact                domain.WorkerArtifact
	}{
		{"reuse", existing.ID, existing.ID, reused},
		{"new", "art_fresh", fresh.ID, fresh},
	} {
		claimed, err := s.ClaimBuild(ctx, tc.build, "builder")
		if err != nil || !claimed {
			t.Fatalf("claim %s: %v (%v)", tc.build, claimed, err)
		}
		if err := s.ReserveBuildArtifact(ctx, tc.build, "builder", &tc.artifact, tc.previousID); err != nil {
			t.Fatal(err)
		}
		if tc.artifact.ID != tc.want {
			t.Fatalf("build %s reserved %q, want %q", tc.build, tc.artifact.ID, tc.want)
		}
	}
	if err := s.View(ctx, PartitionWorkers, func(st *domain.State) error {
		if len(st.Workers.Artifacts) != 2 || st.Workers.Builds["reuse"].Artifact != existing.ID || st.Workers.Builds["new"].Artifact != fresh.ID {
			t.Fatalf("unexpected artifact reservations: %+v", st.Workers)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
