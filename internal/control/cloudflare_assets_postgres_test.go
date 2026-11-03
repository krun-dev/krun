package control

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/securetoken"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

func TestPostgresRuntimeDeltaPreservesJSONNullValues(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	pool := postgresTestPool(store)
	if _, err := pool.Exec(ctx, `INSERT INTO worker_config_revisions(config_revision,digest) VALUES(1,'fixture')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO worker_config_ops(config_revision,ordinal,pool,op_type,version_id,payload) VALUES(1,0,'default','version_put','fixture','{"id":"fixture","variables":{"EMPTY":null,"CONFIG":{"nullable":null,"array":[null,false,1]}}}'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	revisions, _, err := store.ConfigRevisionsSince(ctx, 0, 10)
	if err != nil || len(revisions) != 1 {
		t.Fatal(revisions, err)
	}
	var ops []struct {
		Version struct {
			Variables map[string]any `json:"variables"`
		} `json:"version"`
	}
	if err = json.Unmarshal(revisions[0].Ops, &ops); err != nil {
		t.Fatal(err)
	}
	vars := ops[0].Version.Variables
	if value, ok := vars["EMPTY"]; !ok || value != nil {
		t.Fatal("top-level null binding was removed", vars)
	}
	nested := vars["CONFIG"].(map[string]any)
	if value, ok := nested["nullable"]; !ok || value != nil {
		t.Fatal("nested null value was removed", nested)
	}
}

func TestPostgresAssetUploadCapabilityAndRetention(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	controller := &workerapp.Controller{Service: &workerapp.Service{Store: store}}
	raw := []byte("fixture")
	hash := workerapp.AssetUploadHash("fixture.txt", raw)
	out, err := controller.StartAssetUpload(ctx, workerapp.ServiceRef{Organization: "tenant", Service: "worker"}, map[string]workerapp.AssetUploadFile{"/fixture.txt": {Hash: hash, Size: len(raw)}, "/copy.txt": {Hash: hash, Size: len(raw)}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := store.WorkerAssetSession(ctx, "tenant", securetoken.Digest(out.Token), false)
	if err != nil || len(a.Files) != 2 {
		t.Fatal(a, err)
	}
	if err = store.MarkWorkerAssetUploaded(ctx, a.ID, hash, "text/plain"); err != nil {
		t.Fatal(err)
	}
	complete, err := controller.CompleteAssetUpload(ctx, "tenant", out.Token)
	if err != nil || complete == "" {
		t.Fatal("upload did not complete", err)
	}
	loaded, err := store.WorkerAssetSession(ctx, "tenant", securetoken.Digest(complete), true)
	if err != nil || !loaded.Files[0].Uploaded || !loaded.Files[1].Uploaded {
		t.Fatal(loaded, err)
	}
	pool := postgresTestPool(store)
	if _, err = pool.Exec(ctx, `UPDATE krun_worker_asset_sessions SET created_at=now()-interval '2 hours',expires_at=now()-interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.WorkerAssetSession(ctx, "tenant", securetoken.Digest(out.Token), false); err == nil {
		t.Fatal("expired session accepted")
	}
	if err = store.RetainExpired(ctx); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"krun_worker_asset_sessions", "krun_worker_asset_session_files"} {
		var n int
		if err = pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil || n != 0 {
			t.Fatal(table, n, err)
		}
	}
	// Staging objects retain their own cleanup inventory after session expiry.
	if _, err = pool.Exec(ctx, `UPDATE krun_worker_source_uploads SET created_at=now()-interval '25 hours'`); err != nil {
		t.Fatal(err)
	}
	garbage, err := store.GarbageWorkerSourceUploads(ctx, time.Now().Add(-24*time.Hour), 100)
	if err != nil || len(garbage) != 1 || garbage[0].Key != a.Files[0].ObjectKey {
		t.Fatal("staging GC inventory lost", garbage, err)
	}
}
