package control

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/minio/minio-go/v7"
)

func TestPostgresConfigDeltaStoresNormalizedEntitiesAndShards(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	seedPerformanceSites(t, store, 4)
	config := workerProjectionConfig([]WorkerDomain{{ID: "internal", Domain: "workers.test"}})
	controller := &workersController{server: &Server{Store: store}, config: config}
	objects := map[string][]byte{}
	controller.putObject = func(_ context.Context, _, key string, r io.Reader, _ int64, _ minio.PutObjectOptions) (minio.UploadInfo, error) {
		raw, err := io.ReadAll(r)
		objects[key] = raw
		return minio.UploadInfo{}, err
	}
	head, err := controller.publishConfig(ctx)
	if err != nil || head != 1 {
		t.Fatalf("head=%d err=%v", head, err)
	}
	var routes, ops int
	if err = postgresTestPool(store).QueryRow(ctx, `SELECT (SELECT count(*) FROM worker_config_routes),(SELECT count(*) FROM worker_config_ops)`).Scan(&routes, &ops); err != nil || routes != 4 || ops != 4 {
		t.Fatalf("routes=%d ops=%d err=%v", routes, ops, err)
	}
	var latest workerConfigLatest
	if err = json.Unmarshal(objects[workerPoolConfigLatestKey("default")], &latest); err != nil || !latest.LazyRoutes || latest.Snapshot != "" {
		t.Fatalf("compact head=%+v err=%v", latest, err)
	}
	var checkpoints int
	if err = postgresTestPool(store).QueryRow(ctx, `SELECT count(*) FROM worker_config_checkpoints WHERE revision=published_revision`).Scan(&checkpoints); err != nil || checkpoints < 1 {
		t.Fatalf("checkpoints=%d err=%v", checkpoints, err)
	}
	revisions, _, err := store.ConfigRevisionsSince(ctx, 0, 10)
	if err != nil || len(revisions) != 1 {
		t.Fatalf("feed=%+v err=%v", revisions, err)
	}
	var delta []workerConfigOp
	if err = json.Unmarshal(revisions[0].Ops, &delta); err != nil || len(delta) != 4 {
		t.Fatalf("delta=%+v err=%v", delta, err)
	}
	if again, err := controller.publishConfig(ctx); err != nil || again != head {
		t.Fatalf("idle publisher=%d err=%v", again, err)
	}
}

func TestPostgresWorkerConfigProjectionUsesCompactReadModel(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	seedPerformanceSites(t, store, 4)
	if _, err := postgresTestPool(store).Exec(ctx, `INSERT INTO krun_worker_operations(operation_id,kind,org_id,service_id,env_name,state,steps,created_at) VALUES('broken','publish','tenant','site-000002','production','failed','{}'::jsonb,now())`); err != nil {
		t.Fatal(err)
	}
	config := workerProjectionConfig([]WorkerDomain{{ID: "internal", Domain: "workers.test"}})
	projection, err := loadWorkerConfigProjection(ctx, store, []string{"site-000002"}, config)
	if err != nil || len(projection.Routes) != 1 || projection.Routes[0].Service != "site-000002" {
		t.Fatalf("projection=%+v err=%v", projection, err)
	}
	// Prove the idle publisher does not read unrelated service data.
	controller := &workersController{server: &Server{Store: store}, config: config}
	if _, err = controller.publishConfig(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = postgresTestPool(store).Exec(ctx, `UPDATE krun_worker_environments SET variables='[]'::jsonb`); err != nil {
		t.Fatal(err)
	}
	if _, err = controller.publishConfig(ctx); err != nil {
		t.Fatalf("idle publisher read unrelated state: %v", err)
	}
}

func TestPostgresDirtyQueueCoversProjectionChanges(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	seedPerformanceSites(t, store, 4)
	if _, err := postgresTestPool(store).Exec(ctx, `DELETE FROM worker_config_dirty`); err != nil {
		t.Fatal(err)
	}
	changes := []string{
		`UPDATE krun_worker_environments SET enabled=false WHERE service_id='site-000002'`,
		`UPDATE krun_worker_environments SET release_id='release-new' WHERE service_id='site-000002'`,
		`INSERT INTO krun_worker_service_domains(service_id,domain_id) VALUES('site-000002','internal')`,
		`UPDATE krun_worker_service_placements SET state='draining' WHERE service_id='site-000002'`,
		`DELETE FROM krun_resources WHERE resource_id='site-000002'`,
	}
	for _, query := range changes {
		if _, err := postgresTestPool(store).Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
		dirty, err := store.DirtyWorkerServices(ctx)
		if err != nil || len(dirty) != 1 || dirty[0].ID != "site-000002" {
			t.Fatalf("query=%s dirty=%+v err=%v", query, dirty, err)
		}
		if _, err = postgresTestPool(store).Exec(ctx, `DELETE FROM worker_config_dirty`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := postgresTestPool(store).Exec(ctx, `UPDATE krun_worker_environments SET variables='{"A":"B"}'::jsonb WHERE service_id='site-000004'`); err != nil {
		t.Fatal(err)
	}
	if dirty, err := store.DirtyWorkerServices(ctx); err != nil || len(dirty) != 0 {
		t.Fatalf("unpublished variables affected runtime config: %+v %v", dirty, err)
	}
}
