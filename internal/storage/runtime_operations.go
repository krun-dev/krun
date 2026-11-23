package storage

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/krun-dev/krun/internal/domain"
)

type RuntimeOperationTarget struct {
	Epoch      string
	Generation int64
	Host       string
	Path       string
}

// PinOperationResourceGeneration records the exact deployment being observed.
// It never claims a global head and rejects a superseded deployment or pool.
func (s *Store) PinOperationResourceGeneration(ctx context.Context, id, pool string) (RuntimeOperationTarget, error) {
	var out RuntimeOperationTarget
	err := s.pool.QueryRow(ctx, `WITH pinned AS (
		UPDATE krun_worker_operations o SET resource_generation=COALESCE(g.generation,1),resource_pool=$2,resource_epoch=(SELECT value FROM krun_meta WHERE key='worker_config_epoch')
		FROM krun_worker_environments e LEFT JOIN worker_runtime_generations g ON g.kind='deployment' AND g.resource_id=e.worker_id||'/'||e.env_name
		WHERE o.operation_id=$1 AND o.state='committed' AND e.worker_id=o.worker_id AND e.env_name=o.env_name
		AND e.enabled AND ((o.kind<>'version-url' AND e.deployment_revision=o.deployment_revision AND e.version_id=o.version_id
		AND e.candidate_version_id=o.candidate_version_id AND e.candidate_weight=o.candidate_weight)
        OR (o.kind='version-url' AND EXISTS(SELECT 1 FROM krun_worker_versions v WHERE v.version_id=o.version_id AND v.worker_id=o.worker_id AND v.version_url)))
		AND `+activeOperationPool+` RETURNING o.worker_id,o.env_name,o.resource_generation,o.kind,o.version_id
	) SELECT m.value,p.resource_generation,b.host,b.path FROM pinned p
	JOIN LATERAL(SELECT host,path FROM worker_runtime_bindings b WHERE b.worker_id=p.worker_id AND b.env_name=p.env_name
		AND b.pool=$2 AND b.enabled AND (b.expires_at IS NULL OR b.expires_at>now()) AND ((p.kind='version-url' AND b.fixed_version_id=p.version_id) OR (p.kind<>'version-url' AND b.fixed_version_id='')) ORDER BY b.match_type,b.host,b.path LIMIT 1) b ON true
	JOIN krun_meta m ON m.key='worker_config_epoch'`, id, pool).Scan(&out.Epoch, &out.Generation, &out.Host, &out.Path)
	if errors.Is(err, pgx.ErrNoRows) {
		return RuntimeOperationTarget{}, nil
	}
	return out, err
}

// ConfirmResourceVersionURL confirms a serving preparation probe without
// changing the environment's traffic selection or deployment history.
func (s *Store) ConfirmResourceVersionURL(ctx context.Context, id, pool string, target domain.EdgeConfigCursor) error {
	_, err := s.pool.Exec(ctx, `UPDATE krun_worker_operations o SET state='available',error='',finished_at=now(),
  steps=(SELECT jsonb_agg(jsonb_set(step,'{state}','"done"')) FROM jsonb_array_elements(o.steps) step)
  WHERE o.operation_id=$1 AND o.kind='version-url' AND o.state='committed' AND o.resource_pool=$2 AND o.resource_generation=$3 AND o.resource_epoch=$4
  AND EXISTS(SELECT 1 FROM krun_meta WHERE key='worker_config_epoch' AND value=$4)
  AND EXISTS(SELECT 1 FROM krun_worker_environments e WHERE e.worker_id=o.worker_id AND e.env_name=o.env_name AND e.enabled)
  AND EXISTS(SELECT 1 FROM worker_runtime_bindings b WHERE b.worker_id=o.worker_id AND b.pool=$2 AND b.fixed_version_id=o.version_id AND b.enabled AND b.expires_at>now())
  AND `+activeOperationPool, id, pool, target.Revision, target.Epoch)
	return err
}

func (s *Store) NoteResourceOperation(ctx context.Context, id, message string) error {
	_, err := s.pool.Exec(ctx, `UPDATE krun_worker_operations SET error=$2 WHERE operation_id=$1 AND state='committed'`, id, message)
	return err
}
