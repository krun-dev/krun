package control

import (
	"testing"

	"github.com/krun-dev/krun/internal/storage"
)

func TestPostgresRuntimeEpochRotationRejectsOldACKAndCheckpoint(t *testing.T) {
	store := postgresTestStore(t)
	ctx := t.Context()
	pool := postgresTestPool(store)
	appendTestConfigDelta(t, store, 0, workerConfigOp{Type: "route_delete", Pool: "default", Host: "w.workers.test"})
	previous, err := store.ConfigPosition(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO worker_config_checkpoints(pool,shard,revision,published_revision)
			VALUES('default','ab',1,1) ON CONFLICT(pool,shard) DO UPDATE SET published_revision=1;
		INSERT INTO krun_workers(worker_id,org_id) VALUES('worker','org');
		INSERT INTO krun_worker_placements(worker_id,pool_id,org_id,network_profile,role,state)
			VALUES('worker','default','org','default','primary','active');
		INSERT INTO krun_worker_environments(worker_id,env_name,enabled,version_id,deployment_revision)
			VALUES('worker','production',true,'release',1);
		INSERT INTO krun_worker_operations(operation_id,kind,org_id,worker_id,env_name,state,version_id,deployment_revision,edge_revision,edge_pool)
			VALUES('op','publish','org','worker','production','committed','release',1,1,'default');
	`); err != nil {
		t.Fatal(err)
	}
	ack := WorkerAck{Pod: "runtime", UID: "uid-1", Pool: "default", Epoch: previous.Epoch, Revision: 1}
	if err := store.RecordAck(ctx, ack); err != nil {
		t.Fatal(err)
	}
	epoch, err := storage.RotateConfigEpoch(ctx, pool.Config().ConnString())
	if err != nil || epoch == "" || epoch == previous.Epoch {
		t.Fatalf("rotate epoch: %q %v", epoch, err)
	}
	if err := store.RecordAck(ctx, ack); err == nil || err.Error() != "CONFIG_EPOCH_CHANGED" {
		t.Fatalf("old ACK: %v", err)
	}
	if err := store.MarkConfigCheckpointPublished(ctx, "default", "ab", previous); err != nil {
		t.Fatal(err)
	}
	var checkpoint, edge int64
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT published_revision FROM worker_config_checkpoints WHERE pool='default' AND shard='ab'),
		(SELECT edge_revision FROM krun_worker_operations WHERE operation_id='op')`).Scan(&checkpoint, &edge); err != nil {
		t.Fatal(err)
	}
	if checkpoint != 0 || edge != 0 {
		t.Fatalf("previous epoch retained publication state: checkpoint=%d edge=%d", checkpoint, edge)
	}
	if acks, err := store.ReadAcks(ctx); err != nil || len(acks) != 0 {
		t.Fatalf("previous epoch retained ACKs: %v %v", acks, err)
	}

	ack.Epoch = epoch
	ack.Revision = 0
	if err := store.RecordAck(ctx, ack); err != nil {
		t.Fatal(err)
	}
	acks, err := store.ReadAcks(ctx)
	if err != nil || acks["uid-1"].Epoch != epoch || acks["uid-1"].Revision != 0 {
		t.Fatalf("new ACK inherited the previous revision: %v %v", acks, err)
	}

	// Model a restored deployment that re-pinned to the same numeric revision.
	if _, err := pool.Exec(ctx, `UPDATE krun_worker_operations SET edge_revision=1,edge_pool='default' WHERE operation_id='op'`); err != nil {
		t.Fatal(err)
	}
	if err := store.ConfirmOperation(ctx, "op", "default", previous); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := pool.QueryRow(ctx, `SELECT state FROM krun_worker_operations WHERE operation_id='op'`).Scan(&state); err != nil || state != "committed" {
		t.Fatalf("previous epoch confirmed a new operation: %s %v", state, err)
	}
	current, err := store.ConfigPosition(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ConfirmOperation(ctx, "op", "default", current); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT state FROM krun_worker_operations WHERE operation_id='op'`).Scan(&state); err != nil || state != "available" {
		t.Fatalf("new epoch could not confirm the operation: %s %v", state, err)
	}
}
