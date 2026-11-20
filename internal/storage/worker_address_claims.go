package storage

import (
	"context"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/krun-dev/krun/internal/domain"
)

type addressClaimTx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func claimWorkerAddress(ctx context.Context, tx addressClaimTx, label, kind, owner string) error {
	if len(label) == 0 || len(label) > 63 {
		return domain.Invalid("HOSTNAME_INVALID", "Worker address must fit in a DNS label")
	}
	tag, err := tx.Exec(ctx, `INSERT INTO krun_worker_address_claims(label,owner_kind,owner_id)
		VALUES($1,$2,$3) ON CONFLICT(label) DO UPDATE SET owner_kind=excluded.owner_kind,owner_id=excluded.owner_id,expires_at=NULL
		WHERE (krun_worker_address_claims.owner_kind=excluded.owner_kind AND krun_worker_address_claims.owner_id=excluded.owner_id)
		   OR krun_worker_address_claims.expires_at<=now()`, label, kind, owner)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		if kind == "worker" {
			return domain.Conflict("LOCATOR_CONFLICT", "Worker name is already in use")
		}
		return domain.Conflict("HOSTNAME_UNAVAILABLE", "Worker address is already in use")
	}
	return nil
}

// Hold removed addresses beyond cached route and retained deployment lifetimes.
func releaseWorkerAddress(ctx context.Context, tx addressClaimTx, kind, owner string) error {
	_, err := tx.Exec(ctx, `UPDATE krun_worker_address_claims SET expires_at=now()+interval '31 days'
		WHERE owner_kind=$1 AND owner_id=$2 AND expires_at IS NULL`, kind, owner)
	return err
}
