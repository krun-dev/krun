package storage

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
)

// Deployment intent is immutable. The executor alone sets committed_at after
// winning the environment CAS; URLs are never published for failed candidates.
func writeDeployments(ctx context.Context, tx pgx.Tx, before, after *domain.WorkerState) error {
	for id, d := range after.Deployments {
		if old, ok := before.Deployments[id]; ok {
			if !sameDeployment(old, d) {
				return errors.New("deployment intent is immutable")
			}
			continue
		}
		var locator string
		if err := tx.QueryRow(ctx, `SELECT locator FROM krun_resources WHERE resource_id=$1 AND kind='worker'`, d.Worker).Scan(&locator); err != nil {
			return err
		}
		label := "d-" + securetoken.Digest(id)[:20] + "-" + locator
		if len(label) <= 63 {
			if err := claimWorkerAddress(ctx, tx, label, "deployment", id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE krun_worker_address_claims SET expires_at=$2 WHERE label=$1 AND owner_kind='deployment' AND owner_id=$3`, label, d.Expires, id); err != nil {
				return err
			}
		}
		tag, err := tx.Exec(ctx, `INSERT INTO krun_worker_deployments(deployment_id,org_id,worker_id,env_name,version_id,operation_id,created_at,expires_at,candidate_version_id,candidate_weight,message)
   SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11 WHERE EXISTS(SELECT 1 FROM krun_worker_versions WHERE version_id=$5 AND org_id=$2 AND worker_id=$3 AND env_name=$4)
   AND ($9='' OR EXISTS(SELECT 1 FROM krun_worker_versions WHERE version_id=$9 AND org_id=$2 AND worker_id=$3 AND env_name=$4))
   ON CONFLICT(deployment_id) DO NOTHING`, id, d.Organization, d.Worker, d.Environment, d.Version, d.Operation, d.Created, d.Expires, d.Candidate, d.CandidateWeight, d.Message)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return domain.Conflict("REVISION_CONFLICT", "Deployment changed")
		}
	}
	return nil
}
func readWorkerDeployments(results pgx.BatchResults, st *domain.State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	return readDeploymentRows(rows, st.Workers)
}
func readDeploymentRows(rows pgx.Rows, state *domain.WorkerState) error {
	for rows.Next() {
		var d domain.WorkerDeployment
		if err := rows.Scan(&d.ID, &d.Organization, &d.Worker, &d.Environment, &d.Version, &d.Operation, &d.Created, &d.Expires, &d.Committed, &d.Candidate, &d.CandidateWeight, &d.Message); err != nil {
			return err
		}
		state.Deployments[d.ID] = d
	}
	return rows.Err()
}

func sameDeployment(a, b domain.WorkerDeployment) bool {
	if (a.Committed == nil) != (b.Committed == nil) {
		return false
	}
	if a.Committed != nil && !a.Committed.Equal(*b.Committed) {
		return false
	}
	return a.WorkerGradualRelease == b.WorkerGradualRelease && a.Message == b.Message && a.ID == b.ID && a.Organization == b.Organization && a.Worker == b.Worker && a.Environment == b.Environment && a.Version == b.Version && a.Operation == b.Operation && a.Created.Equal(b.Created) && a.Expires.Equal(b.Expires)
}
