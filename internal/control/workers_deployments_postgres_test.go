package control

import (
	"context"
	"testing"

	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

func TestPostgresDeploymentCommitPublishesPinnedRouteAndProtectsVersion(t *testing.T) {
	store := postgresTestStore(t)
	ctx := context.Background()
	seedPerformanceSites(t, store, 2)
	artifact := WorkerArtifact{ID: "artifact", Organization: "tenant", State: "finalized", Created: now()}
	version := WorkerVersion{ID: "version", Organization: "tenant", Worker: "site-000002", Environment: "production", Artifact: artifact.ID, Created: now(), Variables: map[string]any{}}
	op := WorkerOperation{ID: "op-deploy", Kind: "publish", Organization: version.Organization, Worker: version.Worker, Environment: version.Environment, Version: version.ID, State: "queued", Created: now(), Steps: []domain.WorkerStep{{Name: "artifact"}, {Name: "candidate"}, {Name: "commit"}, {Name: "synchronize"}}}
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Artifacts[artifact.ID] = artifact
		st.Workers.Versions[version.ID] = version
		st.Workers.Operations[op.ID] = op
		workerapp.RememberDeployment(st, op)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	claim, owned, err := store.ClaimOperation(ctx, op.ID, "replica")
	if err != nil || !owned {
		t.Fatalf("claim: owned=%t err=%v", owned, err)
	}
	won, err := store.CommitOperation(ctx, claim)
	if err != nil || !won {
		t.Fatalf("commit: %t %v", won, err)
	}
	if err := store.Update(ctx, PartitionWorkers, func(*State) error { return nil }); err != nil {
		t.Fatalf("unchanged deployment lost instant equality: %v", err)
	}
	config := workerProjectionConfig([]appconfig.WorkerDomain{{ID: "internal", Domain: "workers.test"}})
	projection, err := workerapp.LoadWorkerConfigProjection(ctx, store, []string{version.Worker}, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Routes) != 2 {
		t.Fatalf("current plus pinned routes: %+v", projection.Routes)
	}
	if _, err = postgresTestPool(store).Exec(ctx, `UPDATE krun_worker_versions SET created_at=now()-'40 days'::interval WHERE version_id=$1`, version.ID); err != nil {
		t.Fatal(err)
	}
	if err = store.RetainExpired(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = postgresTestPool(store).QueryRow(ctx, `SELECT count(*) FROM krun_worker_versions WHERE version_id=$1`, version.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("retained deployment lost its version", err)
	}
	if _, err = postgresTestPool(store).Exec(ctx, `UPDATE krun_worker_deployments SET created_at=now()-'40 days'::interval,expires_at=now()-'1 day'::interval`); err != nil {
		t.Fatal(err)
	}
	if err = store.RetainExpired(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.View(ctx, PartitionWorkers, func(st *State) error {
		if len(st.Workers.Deployments) != 0 {
			t.Fatal("expired deployment retained")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_ = domain.WorkerDeployment{}

}

func TestPostgresGradualDeploymentQueueAndProjection(t *testing.T) {
	store := postgresTestStore(t)
	ctx := t.Context()
	seedPerformanceSites(t, store, 2)
	const worker = "site-000002"
	op := WorkerOperation{
		ID: "op-gradual", Kind: "publish", Organization: "tenant", Worker: worker,
		Environment: "production", Version: "stable", State: "queued", Created: now(),
		Steps:                []domain.WorkerStep{{Name: "artifact"}, {Name: "candidate"}, {Name: "commit"}, {Name: "synchronize"}},
		WorkerGradualRelease: domain.WorkerGradualRelease{Candidate: "candidate", CandidateWeight: 125},
	}
	err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		for _, id := range []string{"stable", "candidate"} {
			st.Workers.Artifacts[id] = WorkerArtifact{ID: id, Organization: "tenant", State: "finalized", Created: now()}
			st.Workers.Versions[id] = WorkerVersion{ID: id, Organization: "tenant", Worker: worker, Environment: "production", Artifact: id, Created: now(), Variables: map[string]any{}}
		}
		st.Workers.Operations[op.ID] = op
		workerapp.RememberDeployment(st, op, "Canary")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store.PendingWork(ctx, 10)
	if err != nil || pending.Versions["candidate"].ID != "candidate" || pending.Artifacts["candidate"].ID != "candidate" {
		t.Fatalf("candidate cannot be validated: %v, versions=%v", err, pending.Versions)
	}
	claim, owned, err := store.ClaimOperation(ctx, op.ID, "replica")
	if err != nil || !owned {
		t.Fatalf("claim = %v, %v", owned, err)
	}
	if committed, err := store.CommitOperation(ctx, claim); err != nil || !committed {
		t.Fatalf("commit = %v, %v", committed, err)
	}
	config := workerProjectionConfig([]appconfig.WorkerDomain{{ID: "internal", Domain: "workers.test"}})
	projection, err := workerapp.LoadWorkerConfigProjection(ctx, store, []string{worker}, config)
	if err != nil || len(projection.Routes) != 1 || projection.Routes[0].CandidateWeight != 125 {
		t.Fatalf("wrong split route: %+v %v", projection.Routes, err)
	}
	if _, ok := projection.Versions["candidate"]; !ok {
		t.Fatal("projection is missing the candidate descriptor")
	}
	ops := workerapp.DiffWorkerConfig(nil, projection)
	if _, ok, err := store.AppendConfigDelta(ctx, 0, nil, ops); err != nil || !ok {
		t.Fatalf("publish: %v %v", ok, err)
	}
	route := projection.Routes[0]
	loaded, _, err := store.RuntimeConfigRoute(ctx, route.Host, route.Pool)
	if err != nil || len(loaded.Routes) != 1 || loaded.Routes[0].Candidate != "candidate" ||
		loaded.Versions["candidate"].ID != "candidate" {
		t.Fatalf("cold route lost candidate: %+v %v", loaded, err)
	}
}
