package storage

import (
	"context"
	"testing"

	"github.com/krun-dev/krun/internal/domain"
)

func TestPostgresR2BindingsAndRuntimeAuthorization(t *testing.T) {
	s := postgresTestStore(t)
	ctx := context.Background()
	if err := s.Update(ctx, PartitionIAM, func(st *domain.State) error {
		st.Organizations["org-r2"] = domain.Organization{ID: "org-r2", Name: "Storage test", Revision: 1}
		st.Resources["svc-r2"] = domain.Resource{ID: "svc-r2", Organization: "org-r2", Kind: "worker", Name: "Storage Worker", Revision: 1}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	at := now().UTC()
	ref := domain.WorkerR2Binding{BucketID: "bucket-r2", BucketName: "images", StoreID: "primary"}
	if err := s.Update(ctx, PartitionWorkers, func(st *domain.State) error {
		st.Workers.Buckets["bucket-r2"] = domain.WorkerBucket{ID: "bucket-r2", Organization: "org-r2", Name: "images", Store: "primary", Enabled: true, Revision: 1, Created: at}
		st.Workers.Workers["svc-r2"] = domain.Worker{Worker: "svc-r2", Organization: "org-r2", Environments: map[string]domain.WorkerEnvironment{"production": {Name: "production", Enabled: true, Version: "version-r2", R2Bindings: map[string]domain.WorkerR2Binding{"IMAGES": ref}}}}
		st.Workers.Placements[domain.WorkerPlacementKey("svc-r2", "default")] = domain.WorkerPlacement{Worker: "svc-r2", Organization: "org-r2", Pool: "default", Role: "primary", State: "active", Revision: 1, Created: at, Updated: at}
		st.Workers.Artifacts["artifact-r2"] = domain.WorkerArtifact{ID: "artifact-r2", Organization: "org-r2", State: "finalized", Created: at}
		st.Workers.Versions["version-r2"] = domain.WorkerVersion{CreatedBy: "author-id", CreatedByName: "Version Author", ID: "version-r2", Organization: "org-r2", Worker: "svc-r2", Environment: "production", Artifact: "artifact-r2", Created: at, R2Bindings: map[string]domain.WorkerR2Binding{"IMAGES": ref}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Open(ctx, PartitionWorkers)
	if err != nil {
		t.Fatal(err)
	}
	if version := loaded.Workers.Versions["version-r2"]; version.CreatedBy != "author-id" || version.CreatedByName != "Version Author" {
		t.Fatalf("version author did not reload: %+v", version)
	}
	if loaded.Workers.Buckets["bucket-r2"].Store != "primary" || loaded.Workers.Versions["version-r2"].R2Bindings["IMAGES"] != ref || loaded.Workers.Workers["svc-r2"].Environments["production"].R2Bindings["IMAGES"] != ref {
		t.Fatal("bucket or binding rows did not reload")
	}
	if err := s.Update(ctx, PartitionWorkers, func(*domain.State) error { return nil }); err != nil {
		t.Fatal("unchanged bucket rejected:", err)
	}
	if _, err := s.RuntimeBucket(ctx, "version-r2", "IMAGES", "default"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RuntimeBucket(ctx, "version-r2", "IMAGES", "other-pool"); err == nil {
		t.Fatal("cross-pool credentials allowed")
	}
	if _, err := s.RuntimeBucket(ctx, "version-r2", "FORGED", "default"); err == nil {
		t.Fatal("forged binding allowed")
	}
	projection, err := s.WorkerConfigState(ctx, []string{"svc-r2"})
	if err != nil {
		t.Fatal(err)
	}
	if version := projection.Workers.Versions["version-r2"]; version.CreatedBy != "author-id" || version.CreatedByName != "Version Author" {
		t.Fatalf("config projection lost author: %+v", version)
	}
	if projection.Workers.Versions["version-r2"].R2Bindings["IMAGES"] != ref {
		t.Fatal("configuration projection lost R2 binding")
	}
	if err := s.Update(ctx, PartitionWorkers, func(st *domain.State) error {
		b := st.Workers.Buckets["bucket-r2"]
		b.Enabled = false
		b.Revision++
		st.Workers.Buckets[b.ID] = b
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RuntimeBucket(ctx, "version-r2", "IMAGES", "default"); err == nil {
		t.Fatal("disabled bucket authorized")
	}
}
