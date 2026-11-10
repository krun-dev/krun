package storage

import (
	"context"
	"encoding/json"

	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/storage/models"
)

// StartUploadedBuild reserves bounded work for synchronous trusted packaging.
// An unrelated controller cannot claim the upload while its lease is fresh.
func (s *Store) StartUploadedBuild(ctx context.Context, b domain.WorkerBuild, actor domain.WorkerActor) error {
	tx, err := s.beginORMTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	result, err := tx.orm.NewUpdate().Model((*models.Worker)(nil)).Set("active_build_count = active_build_count + 1").Where("worker_id = ?", b.Worker).Where("org_id = ?", b.Organization).Where("active_build_count < ?", WorkerActiveBuildLimit).Exec(ctx)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return domain.Problem(429, "WORKER_BUILD_LIMIT", "Too many active uploads")
	}
	flags, err := json.Marshal(b.CompatibilityFlags)
	if err != nil {
		return err
	}
	row := models.Build{BuildID: b.ID, OrgID: b.Organization, WorkerID: b.Worker, EnvName: b.Environment, State: "building", Entrypoint: b.Entrypoint, Profile: b.Profile, CompatibilityDate: b.CompatibilityDate, CompatibilityFlags: flags, CreatedAt: b.Created, ClaimedBy: b.ClaimedBy, ClaimedAt: b.ClaimedAt}
	if _, err = tx.orm.NewInsert().Model(&row).Column("build_id", "org_id", "worker_id", "env_name", "state", "entrypoint", "profile", "compatibility_date", "compatibility_flags", "created_at", "claimed_by", "claimed_at").Returning("NULL").Exec(ctx); err != nil {
		return err
	}
	private := models.BuildPrivate{BuildID: b.ID, ActorUserID: actor.User, ActorKeyID: actor.Key, ActorAuthVersion: actor.AuthVersion}
	if _, err = tx.orm.NewInsert().Model(&private).Returning("NULL").Exec(ctx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
