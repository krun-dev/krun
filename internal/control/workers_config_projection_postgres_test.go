package control

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestPostgresConfigRevisionsStoreNormalizedEntitiesAndOps(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	first := workerConfigProjection{
		Routes: []workerConfigRoute{{Host: "old.workers.test", Path: "/", Enabled: true, Release: "rel-old"}},
		Releases: map[string]WorkerRelease{
			"rel-old": {ID: "rel-old", Service: "svc", Organization: "org", Environment: "production", Artifact: "art-old", Pool: "default"},
		},
	}
	firstDigest, firstPayload, err := workerConfigDigest(first)
	if err != nil {
		t.Fatal(err)
	}
	firstDelta := diffWorkerConfig(nil, first)
	firstOps, _ := json.Marshal(firstDelta)
	if revision, _, appended, err := store.AppendConfigRevisionCAS(ctx, 0, firstDigest, firstPayload, firstOps, 10); err != nil || !appended || revision != 1 {
		t.Fatalf("first append revision=%d appended=%v err=%v", revision, appended, err)
	}
	second := workerConfigProjection{
		Routes: []workerConfigRoute{{Host: "new.workers.test", Path: "/", Enabled: true, Release: "rel-new"}},
		Releases: map[string]WorkerRelease{
			"rel-new": {ID: "rel-new", Service: "svc", Organization: "org", Environment: "production", Artifact: "art-new", Pool: "default"},
		},
	}
	secondDigest, secondPayload, err := workerConfigDigest(second)
	if err != nil {
		t.Fatal(err)
	}
	secondDelta := diffWorkerConfig(&first, second)
	secondOps, _ := json.Marshal(secondDelta)
	if revision, _, appended, err := store.AppendConfigRevisionCAS(ctx, 1, secondDigest, secondPayload, secondOps, 10); err != nil || !appended || revision != 2 {
		t.Fatalf("second append revision=%d appended=%v err=%v", revision, appended, err)
	}

	var legacyColumns int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema=current_schema() AND table_name='worker_config_revisions' AND column_name IN ('payload','ops')`).Scan(&legacyColumns); err != nil {
		t.Fatal(err)
	}
	if legacyColumns != 0 {
		t.Fatalf("revision table still has %d document columns", legacyColumns)
	}
	var opColumns int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema=current_schema() AND table_name='worker_config_ops'
		AND column_name IN ('op_type','host','release_id','payload')`).Scan(&opColumns); err != nil {
		t.Fatal(err)
	}
	if opColumns != 4 {
		t.Fatalf("worker_config_ops has %d normalized columns, want 4", opColumns)
	}
	var routes, releases, revisions, ops, envelopedOps int
	if err := store.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM worker_config_routes),
		(SELECT count(*) FROM worker_config_releases),
		(SELECT count(*) FROM worker_config_revisions),
		(SELECT count(*) FROM worker_config_ops),
		(SELECT count(*) FROM worker_config_ops WHERE payload ? 'type' OR payload ? 'route')`).Scan(&routes, &releases, &revisions, &ops, &envelopedOps); err != nil {
		t.Fatal(err)
	}
	if routes != 1 || releases != 1 || revisions != 2 || ops != len(firstDelta)+len(secondDelta) || envelopedOps != 0 {
		t.Fatalf("routes=%d releases=%d revisions=%d ops=%d envelopedOps=%d", routes, releases, revisions, ops, envelopedOps)
	}
	latest, ok, err := store.LatestConfigRevision(ctx)
	if err != nil || !ok || latest.Revision != 2 {
		t.Fatalf("latest=%+v ok=%v err=%v", latest, ok, err)
	}
	decoded, err := decodePublishedWorkerConfig(latest.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.Routes) != 1 || decoded.Routes[0].Host != "new.workers.test" || decoded.Releases["rel-new"].Artifact != "art-new" {
		t.Fatalf("reconstructed head=%+v", decoded)
	}
	items, _, err := store.ConfigRevisionsSince(ctx, 1, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	var reconstructedOps []workerConfigOp
	if err := json.Unmarshal(items[0].Ops, &reconstructedOps); err != nil || len(reconstructedOps) != len(secondDelta) {
		t.Fatalf("ops=%+v err=%v", reconstructedOps, err)
	}
}

func TestPostgresWorkerConfigProjectionUsesCompactReadModel(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	if err := store.Update(ctx, PartitionIAM, func(st *State) error {
		st.Organizations["org"] = Organization{ID: "org", Name: "Org", Revision: 1}
		st.Resources["svc"] = Resource{ID: "svc", Kind: "service", Organization: "org", Name: "Service", Revision: 1}
		st.Revision++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Artifacts["art"] = WorkerArtifact{ID: "art", Organization: "org", State: "finalized"}
		st.Workers.Releases["rel"] = WorkerRelease{ID: "rel", Service: "svc", Organization: "org", Environment: "production", Artifact: "art", Pool: "default", Variables: map[string]string{"A": "B"}}
		st.Workers.Services["svc"] = WorkerService{Service: "svc", Organization: "org", Domains: []string{"internal"}, Environments: map[string]WorkerEnvironment{
			"production": {Name: "production", Enabled: true, Release: "rel", ConfigRevision: 1, Variables: map[string]string{}},
		}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// A generic Workers load cannot decode this operation. The edge read model
	// succeeds because it never reads operation/build/history tables.
	if _, err := store.pool.Exec(ctx, `INSERT INTO krun_worker_operations
		(operation_id, kind, org_id, service_id, env_name, state, steps, created_at)
		VALUES ('broken-op','publish','org','svc','production','failed','{}'::jsonb,now())`); err != nil {
		t.Fatal(err)
	}
	projection, generation, err := store.WorkerConfigProjection(ctx, []WorkerDomain{{ID: "internal", Domain: "workers.test"}})
	if err != nil {
		t.Fatal(err)
	}
	if generation < 1 || len(projection.Routes) != 1 || projection.Routes[0].Release != "rel" || projection.Releases["rel"].Artifact != "art" {
		t.Fatalf("generation=%d projection=%+v", generation, projection)
	}
	routing, routingGeneration, err := store.WorkerRoutingTopology(ctx)
	if err != nil || routingGeneration != generation || !routing["svc"].Environments["production"].Enabled {
		t.Fatalf("routing generation=%d topology=%+v err=%v", routingGeneration, routing, err)
	}
	// Make the specialized projection unreadable without advancing the durable
	// generation. A clean controller cycle must only read the meta row; if it
	// rebuilds the projection this malformed map produces a JSON decode error.
	if _, err := store.pool.Exec(ctx, `UPDATE krun_worker_releases SET variables='[]'::jsonb WHERE release_id='rel'`); err != nil {
		t.Fatal(err)
	}
	baseline := projection
	controller := &workersController{
		server:                   &Server{Store: store},
		configLoaded:             true,
		configLast:               &baseline,
		configRevision:           41,
		configTopologyLoaded:     true,
		configTopologyGeneration: generation,
	}
	if revision, err := controller.publishConfig(ctx); err != nil || revision != 41 {
		t.Fatalf("generation no-op rebuilt or published config: revision=%d err=%v", revision, err)
	}
	var revisions int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM worker_config_revisions`).Scan(&revisions); err != nil || revisions != 0 {
		t.Fatalf("generation no-op wrote %d revisions: %v", revisions, err)
	}
}

func TestPostgresNormalizedConfigMigrationSeedsLegacyHead(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `
		DROP TABLE worker_config_ops;
		DROP TABLE worker_config_routes;
		DROP TABLE worker_config_releases;
		DROP TABLE worker_config_revisions;
		CREATE TABLE worker_config_revisions (
			config_revision bigint PRIMARY KEY,
			digest text NOT NULL,
			payload jsonb NOT NULL,
			ops jsonb NOT NULL,
			created_at timestamptz NOT NULL DEFAULT now()
		);
		INSERT INTO worker_config_revisions (config_revision,digest,payload,ops) VALUES
		(7,'legacy',
		 '{"routes":[{"host":"legacy.workers.test","enabled":true,"release":"rel-legacy"}],"releases":{"rel-legacy":{"id":"rel-legacy","artifact_id":"art-legacy","pool":"default"}}}',
		 '[{"type":"route_upsert","host":"legacy.workers.test","route":{"host":"legacy.workers.test","enabled":true,"release":"rel-legacy"}}]')`); err != nil {
		t.Fatal(err)
	}
	if err = migrateNormalizedWorkerConfig(ctx, tx); err != nil {
		t.Fatal(err)
	}
	var routes, releases, ops, legacyColumns int
	if err = tx.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM worker_config_routes),
		(SELECT count(*) FROM worker_config_releases),
		(SELECT count(*) FROM worker_config_ops),
		(SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema()
		 AND table_name='worker_config_revisions' AND column_name IN ('payload','ops'))`).Scan(&routes, &releases, &ops, &legacyColumns); err != nil {
		t.Fatal(err)
	}
	if routes != 1 || releases != 1 || ops != 1 || legacyColumns != 0 {
		t.Fatalf("routes=%d releases=%d ops=%d legacyColumns=%d", routes, releases, ops, legacyColumns)
	}
}

func TestPostgresConfigCorruptionFailsClosed(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	projection := workerConfigProjection{
		Routes:   []workerConfigRoute{{Host: "old.workers.test", Path: "/", Enabled: true}},
		Releases: map[string]WorkerRelease{},
	}
	digest, payload, err := workerConfigDigest(projection)
	if err != nil {
		t.Fatal(err)
	}
	if revision, _, appended, err := store.AppendConfigRevisionCAS(ctx, 0, digest, payload, []byte(`[]`), 10); err != nil || !appended || revision != 1 {
		t.Fatalf("seed revision=%d appended=%v err=%v", revision, appended, err)
	}
	// Simulate partial corruption of the normalized published head. Treating the
	// now-valid empty JSON projection as the previous state would omit the
	// route_delete op and leave old.workers.test alive on runtimes.
	if _, err := store.pool.Exec(ctx, `DELETE FROM worker_config_routes WHERE host='old.workers.test'`); err != nil {
		t.Fatal(err)
	}
	controller := &workersController{server: &Server{Store: store}}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := controller.publishConfig(ctx); err == nil {
			t.Fatalf("attempt %d published from a corrupt normalized head", attempt+1)
		}
		if controller.configLoaded || controller.configLast != nil {
			t.Fatalf("attempt %d retained corrupt baseline: loaded=%v projection=%+v", attempt+1, controller.configLoaded, controller.configLast)
		}
	}
	var revisions int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM worker_config_revisions`).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if revisions != 1 {
		t.Fatalf("corruption created %d revisions, want original head only", revisions)
	}
}

func TestPostgresTopologyGenerationCoversEveryProjectionInput(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	created := now()
	if err := store.Update(ctx, PartitionIAM, func(st *State) error {
		st.Organizations["org"] = Organization{ID: "org", Name: "Org", Revision: 1}
		st.Resources["svc"] = Resource{ID: "svc", Kind: "service", Organization: "org", Name: "Service", Revision: 1}
		st.Revision++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Artifacts["art"] = WorkerArtifact{ID: "art", Organization: "org", State: "finalized"}
		st.Workers.Releases["rel-1"] = WorkerRelease{ID: "rel-1", Service: "svc", Organization: "org", Environment: "production", Artifact: "art", Pool: "default", Variables: map[string]string{}, Created: created}
		st.Workers.Services["svc"] = WorkerService{Service: "svc", Organization: "org", Domains: []string{"internal"}, Environments: map[string]WorkerEnvironment{
			"production": {Name: "production", Enabled: true, Release: "rel-1", ConfigRevision: 1, Variables: map[string]string{}},
		}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	generation, err := store.WorkerTopologyGeneration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertAdvanced := func(action string, mutate func() error) {
		t.Helper()
		before := generation
		if err := mutate(); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		generation, err = store.WorkerTopologyGeneration(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if generation != before+1 {
			t.Fatalf("%s generation=%d want %d", action, generation, before+1)
		}
	}
	assertAdvanced("active release descriptor", func() error {
		return store.Update(ctx, PartitionWorkers, func(st *State) error {
			release := st.Workers.Releases["rel-1"]
			release.Variables = map[string]string{"VERSION": "2"}
			st.Workers.Releases[release.ID] = release
			return nil
		})
	})
	assertAdvanced("environment enabled", func() error {
		return store.Update(ctx, PartitionWorkers, func(st *State) error {
			service := st.Workers.Services["svc"]
			environment := service.Environments["production"]
			environment.Enabled = false
			environment.ConfigRevision++
			service.Environments[environment.Name] = environment
			st.Workers.Services[service.Service] = service
			return nil
		})
	})
	assertAdvanced("environment re-enabled", func() error {
		return store.Update(ctx, PartitionWorkers, func(st *State) error {
			service := st.Workers.Services["svc"]
			environment := service.Environments["production"]
			environment.Enabled = true
			environment.ConfigRevision++
			service.Environments[environment.Name] = environment
			st.Workers.Services[service.Service] = service
			return nil
		})
	})
	beforeUnreferenced, err := store.Open(ctx, PartitionWorkers)
	if err != nil {
		t.Fatal(err)
	}
	afterUnreferenced, err := cloneState(beforeUnreferenced)
	if err != nil {
		t.Fatal(err)
	}
	afterUnreferenced.Workers.Releases["rel-2"] = WorkerRelease{ID: "rel-2", Service: "svc", Organization: "org", Environment: "production", Artifact: "art", Pool: "default", Variables: map[string]string{}, Created: created}
	afterUnreferenced.Workers.Operations["op"] = WorkerOperation{ID: "op", Kind: "publish", Service: "svc", Organization: "org", Environment: "production", State: "validating", Release: "rel-2", ExpectedRevision: 0, Steps: []WorkerStep{{Name: "artifact"}, {Name: "candidate"}, {Name: "commit"}, {Name: "synchronize"}}}
	if workerEdgeTopologyChanged(beforeUnreferenced, afterUnreferenced) {
		t.Fatal("unreferenced release was classified as edge topology")
	}
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Releases["rel-2"] = WorkerRelease{ID: "rel-2", Service: "svc", Organization: "org", Environment: "production", Artifact: "art", Pool: "default", Variables: map[string]string{}, Created: created}
		st.Workers.Operations["op"] = WorkerOperation{ID: "op", Kind: "publish", Service: "svc", Organization: "org", Environment: "production", State: "validating", Release: "rel-2", ExpectedRevision: 0, Steps: []WorkerStep{{Name: "artifact"}, {Name: "candidate"}, {Name: "commit"}, {Name: "synchronize"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Adding an unreferenced release and operation does not affect edge config.
	if unchanged, err := store.WorkerTopologyGeneration(ctx); err != nil || unchanged != generation {
		t.Fatalf("unreferenced work generation=%d want %d err=%v", unchanged, generation, err)
	}
	assertAdvanced("operation commit", func() error {
		won, err := store.commitOperation(ctx, WorkerOperation{ID: "op", Service: "svc", Environment: "production", Release: "rel-2", ExpectedRevision: 0})
		if err == nil && !won {
			return errors.New("operation did not win environment CAS")
		}
		return err
	})
	assertAdvanced("service resource deletion", func() error {
		return store.Update(ctx, PartitionIAM, func(st *State) error {
			delete(st.Resources, "svc")
			st.Revision++
			return nil
		})
	})
}
