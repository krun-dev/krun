package control

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestPostgresWorkerCompatibilityMetadataRoundTrip(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	created := now()
	finished := created
	flags := []string{"enable_nodejs_fs_module", "nodejs_compat"}
	artifact := WorkerArtifact{
		ID: "art-compat", Organization: "org-compat", State: "finalized", Profile: workerProfile,
		ArchiveDigest: "archive", ManifestDigest: "manifest", ModuleDigest: "module",
		CompatibilityDate: "2026-09-28", CompatibilityFlags: append([]string(nil), flags...),
		Size: 123, ObjectKey: "v1/org-compat/archive", StorageLocation: "primary", Bucket: "artifacts", Created: created,
	}
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Builds["bld-compat"] = WorkerBuild{
			ID: "bld-compat", Organization: "org-compat", Service: "svc-compat", Environment: workerBaseline,
			State: "succeeded", Entrypoint: "index.ts", Profile: workerProfile,
			CompatibilityDate: "2026-09-28", CompatibilityFlags: append([]string(nil), flags...),
			Artifact: artifact.ID, Created: created, Finished: &finished,
		}
		st.Workers.Artifacts[artifact.ID] = artifact
		st.Workers.Releases["rel-compat"] = WorkerRelease{
			ID: "rel-compat", Organization: "org-compat", Service: "svc-compat", Environment: workerBaseline,
			Artifact: artifact.ID, Profile: artifact.Profile, ArchiveDigest: artifact.ArchiveDigest, ModuleDigest: artifact.ModuleDigest,
			CompatibilityDate: artifact.CompatibilityDate, CompatibilityFlags: append([]string(nil), flags...),
			Variables: map[string]string{}, Created: created,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	state, err := store.Open(ctx, PartitionWorkers)
	if err != nil {
		t.Fatal(err)
	}
	build := state.Workers.Builds["bld-compat"]
	storedArtifact := state.Workers.Artifacts[artifact.ID]
	release := state.Workers.Releases["rel-compat"]
	for name, got := range map[string][]string{
		"build": build.CompatibilityFlags, "artifact": storedArtifact.CompatibilityFlags, "release": release.CompatibilityFlags,
	} {
		if !reflect.DeepEqual(got, flags) {
			t.Fatalf("%s compatibility flags = %v, want %v", name, got, flags)
		}
	}
	if build.CompatibilityDate != artifact.CompatibilityDate || storedArtifact.Profile != workerProfile || release.CompatibilityDate != artifact.CompatibilityDate || release.Profile != workerProfile {
		t.Fatalf("compatibility metadata did not round-trip: build=%+v artifact=%+v release=%+v", build, storedArtifact, release)
	}

	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Releases["rel-mismatch"] = WorkerRelease{
			ID: "rel-mismatch", Organization: artifact.Organization, Service: "svc-compat", Environment: workerBaseline,
			Artifact: artifact.ID, Profile: artifact.Profile, ArchiveDigest: artifact.ArchiveDigest, ModuleDigest: artifact.ModuleDigest,
			CompatibilityDate: "2026-09-27", CompatibilityFlags: []string{"nodejs_compat"}, Variables: map[string]string{}, Created: created,
		}
		return nil
	}); err == nil {
		t.Fatal("release with artifact compatibility mismatch was persisted")
	}

	err = store.Update(ctx, PartitionWorkers, func(st *State) error {
		release := st.Workers.Releases["rel-compat"]
		release.CompatibilityDate = "2026-09-27"
		st.Workers.Releases[release.ID] = release
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("existing release descriptor was mutated: %v", err)
	}
}
