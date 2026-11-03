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
	version := WorkerVersion{ID: "version", Organization: "tenant", Service: "site-000002", Environment: "production", Artifact: artifact.ID, Created: now(), Variables: map[string]string{}}
	op := WorkerOperation{ID: "op-deploy", Kind: "publish", Organization: version.Organization, Service: version.Service, Environment: version.Environment, Version: version.ID, State: "queued", Created: now(), Steps: []domain.WorkerStep{{Name: "artifact"}, {Name: "candidate"}, {Name: "commit"}, {Name: "synchronize"}}}
	if err := store.Update(ctx, PartitionWorkers, func(st *State) error {
		st.Workers.Artifacts[artifact.ID] = artifact
		st.Workers.Versions[version.ID] = version
		st.Workers.Operations[op.ID] = op
		workerapp.RememberDeployment(st, op)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	won, err := store.CommitOperation(ctx, op)
	if err != nil || !won {
		t.Fatalf("commit: %t %v", won, err)
	}
	if err := store.Update(ctx, PartitionWorkers, func(*State) error { return nil }); err != nil {
		t.Fatalf("unchanged deployment lost instant equality: %v", err)
	}
	config := workerProjectionConfig([]appconfig.WorkerDomain{{ID: "internal", Domain: "workers.test"}})
	projection, err := workerapp.LoadWorkerConfigProjection(ctx, store, []string{version.Service}, config)
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
