package control

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const workerSourceGCLease = "10 minutes"

type workerSourceUpload struct {
	Key          string
	Organization string
	UseRevision  int64
}

// issueWorkerSourceUpload records the capability before its presigned URL is
// returned. Builds only accept keys present here, so an old arbitrary object
// cannot be introduced while object GC is deciding whether to delete it.
func (s *Store) issueWorkerSourceUpload(ctx context.Context, key, organization string) error {
	if s.pool == nil {
		return nil
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO krun_worker_source_uploads
		(object_key, org_id, state, created_at) VALUES ($1,$2,'issued',now())
		ON CONFLICT (object_key) DO NOTHING`, key, organization)
	return err
}

// reserveWorkerSourceUpload runs inside the build insertion transaction. Its
// use_revision is the arbitration token shared with GC: reservation and a
// stale deletion claim cannot both win.
func reserveWorkerSourceUpload(ctx context.Context, tx pgx.Tx, key, organization, build string) error {
	if key == "" {
		return nil
	}
	tag, err := tx.Exec(ctx, `UPDATE krun_worker_source_uploads
		SET state='reserved', build_id=$3, use_revision=use_revision+1,
		    gc_claimed_by='', gc_claimed_at=NULL
		WHERE object_key=$1 AND org_id=$2 AND state='issued'
		  AND created_at > now() - $4::interval`, key, organization, build, workerUploadRetain.String())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return problem(422, "WORKER_SOURCE_UPLOAD_INVALID", "Source upload is missing, expired, or already used")
	}
	return nil
}

// registerGeneratedBuildSource protects a freshly-created repository/inline
// bundle. These objects are new (and therefore outside the age-based GC
// window), so registering immediately after upload is sufficient; the build
// row and metadata become visible atomically.
func (s *Store) registerGeneratedBuildSource(ctx context.Context, build, organization, key string) error {
	if s.pool == nil {
		if st := s.memState(); st != nil {
			private := st.Workers.BuildPrivate[build]
			private.SourceKey, private.Files = key, nil
			st.Workers.BuildPrivate[build] = private
		}
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = registerGeneratedBuildSourceTx(ctx, tx, build, organization, key); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func registerGeneratedBuildSourceTx(ctx context.Context, tx pgx.Tx, build, organization, key string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO krun_worker_source_uploads
		(object_key, org_id, state, build_id, use_revision, created_at)
		VALUES ($1,$2,'reserved',$3,1,now())
		ON CONFLICT (object_key) DO UPDATE SET
			state='reserved', build_id=excluded.build_id,
			use_revision=krun_worker_source_uploads.use_revision+1,
			gc_claimed_by='', gc_claimed_at=NULL
		WHERE krun_worker_source_uploads.state<>'deleting'`, key, organization, build); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE krun_worker_build_private
		SET source_key=$2, files=NULL WHERE build_id=$1`, build, key)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("build source row is unavailable")
	}
	return nil
}

func (s *Store) garbageWorkerSourceUploads(ctx context.Context, cutoff time.Time, limit int) ([]workerSourceUpload, error) {
	if s.pool == nil {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT object_key, org_id, use_revision
		FROM krun_worker_source_uploads u
		WHERE u.created_at <= $1
		  AND (u.state IN ('issued','reserved') OR
		       (u.state='deleting' AND (u.gc_claimed_at IS NULL OR u.gc_claimed_at < now() - $3::interval)))
		  AND NOT EXISTS (SELECT 1 FROM krun_worker_build_private p WHERE p.source_key=u.object_key)
		ORDER BY u.created_at, u.object_key LIMIT $2`, cutoff, limit, workerSourceGCLease)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []workerSourceUpload{}
	for rows.Next() {
		var item workerSourceUpload
		if err = rows.Scan(&item.Key, &item.Organization, &item.UseRevision); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) claimGarbageWorkerSourceUpload(ctx context.Context, key, replica string, expectedRevision int64) (bool, error) {
	if s.pool == nil {
		return false, nil
	}
	tag, err := s.pool.Exec(ctx, `UPDATE krun_worker_source_uploads u
		SET state='deleting', gc_claimed_by=$2, gc_claimed_at=now()
		WHERE object_key=$1 AND use_revision=$3
		  AND (state IN ('issued','reserved') OR
		       (state='deleting' AND (gc_claimed_at IS NULL OR gc_claimed_at < now() - $4::interval)))
		  AND NOT EXISTS (SELECT 1 FROM krun_worker_build_private p WHERE p.source_key=u.object_key)`,
		key, replica, expectedRevision, workerSourceGCLease)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) deleteGarbageWorkerSourceUpload(ctx context.Context, key, replica string) error {
	if s.pool == nil {
		return nil
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM krun_worker_source_uploads u
		WHERE object_key=$1 AND state='deleting' AND gc_claimed_by=$2
		  AND NOT EXISTS (SELECT 1 FROM krun_worker_build_private p WHERE p.source_key=u.object_key)`, key, replica)
	return err
}

func (s *Store) workerSourceUploadKnown(ctx context.Context, key string) (bool, error) {
	if s.pool == nil {
		return false, nil
	}
	var known bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM krun_worker_source_uploads WHERE object_key=$1)`, key).Scan(&known)
	return known, err
}
