package control

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	workerartifact "github.com/krun-dev/krun/internal/artifact"
	appconfig "github.com/krun-dev/krun/internal/config"
)

func TestPostgresWorkerCompatibilityMetadataRoundTrip(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	created := now()
	finished := created
	flags := []string{"enable_nodejs_fs_module", "nodejs_compat"}
	artifact := WorkerArtifact{
		ID: "art-compat", Organization: "org-compat", State: "finalized", Profile: workerartifact.WorkerProfile,
		ArchiveDigest: "archive", ManifestDigest: "manifest", ModuleDigest: "module",
		CompatibilityDate: "2026-09-28", CompatibilityFlags: append([]string(nil), flags...),
		Size: 123, ObjectKey: "v1/org-compat/archive", StorageLocation: "primary", Bucket: "artifacts", Created: created,
	}
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Builds["bld-compat"] = WorkerBuild{
			ID: "bld-compat", Organization: "org-compat", Worker: "svc-compat", Environment: appconfig.WorkerBaseline,
			State: "succeeded", Entrypoint: "index.ts", Profile: workerartifact.WorkerProfile,
			CompatibilityDate: "2026-09-28", CompatibilityFlags: append([]string(nil), flags...),
			Artifact: artifact.ID, Created: created, Finished: &finished,
		}
		st.Workers.Artifacts[artifact.ID] = artifact
		st.Workers.Versions["rel-compat"] = WorkerVersion{
			ID: "rel-compat", Organization: "org-compat", Worker: "svc-compat", Environment: appconfig.WorkerBaseline,
			Artifact: artifact.ID, Profile: artifact.Profile, ArchiveDigest: artifact.ArchiveDigest, ModuleDigest: artifact.ModuleDigest,
			CompatibilityDate: artifact.CompatibilityDate, CompatibilityFlags: append([]string(nil), flags...),
			Variables: map[string]any{"CONFIG": map[string]any{"values": []any{true, nil, json.Number("42")}}}, VersionMetadata: "CF_VERSION_METADATA", Tag: "fixture-tag", Message: "fixture message", Created: created,
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
	version := state.Workers.Versions["rel-compat"]
	if version.VersionMetadata != "CF_VERSION_METADATA" || version.Tag != "fixture-tag" || version.Message != "fixture message" || !reflect.DeepEqual(version.Variables["CONFIG"], map[string]any{"values": []any{true, nil, json.Number("42")}}) {
		t.Fatal("typed bindings and version annotations did not round-trip", version)
	}
	for name, got := range map[string][]string{
		"build": build.CompatibilityFlags, "artifact": storedArtifact.CompatibilityFlags, "version": version.CompatibilityFlags,
	} {
		if !reflect.DeepEqual(got, flags) {
			t.Fatalf("%s compatibility flags = %v, want %v", name, got, flags)
		}
	}
	if build.CompatibilityDate != artifact.CompatibilityDate || storedArtifact.Profile != workerartifact.WorkerProfile || version.CompatibilityDate != artifact.CompatibilityDate || version.Profile != workerartifact.WorkerProfile {
		t.Fatalf("compatibility metadata did not round-trip: build=%+v artifact=%+v version=%+v", build, storedArtifact, version)
	}

	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Versions["rel-mismatch"] = WorkerVersion{
			ID: "rel-mismatch", Organization: artifact.Organization, Worker: "svc-compat", Environment: appconfig.WorkerBaseline,
			Artifact: artifact.ID, Profile: artifact.Profile, ArchiveDigest: artifact.ArchiveDigest, ModuleDigest: artifact.ModuleDigest,
			CompatibilityDate: "2026-09-27", CompatibilityFlags: []string{"nodejs_compat"}, Variables: map[string]any{}, Created: created,
		}
		return nil
	}); err == nil {
		t.Fatal("version with artifact compatibility mismatch was persisted")
	}

	err = store.Update(ctx, PartitionWorkers, func(st *State) error {
		version := st.Workers.Versions["rel-compat"]
		version.CompatibilityDate = "2026-09-27"
		st.Workers.Versions[version.ID] = version
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("existing version descriptor was mutated: %v", err)
	}
}
