package storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/storage/models"
)

func (s *Store) InsertWorkerAssetSession(ctx context.Context, session domain.WorkerAssetSession) error {
	tx, err := s.beginORMTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	row := models.AssetSession{SessionID: session.ID, OrgID: session.Organization, WorkerID: session.Worker, UploadHash: session.UploadHash, CompletionHash: session.CompletionHash, CreatedAt: session.Created, ExpiresAt: session.Expires}
	if _, err = tx.orm.NewInsert().Model(&row).Returning("NULL").Exec(ctx); err != nil {
		return err
	}
	files := make([]models.AssetSessionFile, 0, len(session.Files))
	sources := []models.SourceUpload{}
	seen := map[string]bool{}
	for _, f := range session.Files {
		files = append(files, models.AssetSessionFile{SessionID: session.ID, Path: f.Path, Hash: f.Hash, SizeBytes: f.Size, ObjectKey: f.ObjectKey, ContentType: f.ContentType})
		if !seen[f.ObjectKey] {
			sources = append(sources, models.SourceUpload{ObjectKey: f.ObjectKey, OrgID: session.Organization, State: "issued", CreatedAt: session.Created})
			seen[f.ObjectKey] = true
		}
	}
	if len(files) > 0 {
		if _, err = tx.orm.NewInsert().Model(&files).Returning("NULL").Exec(ctx); err != nil {
			return err
		}
		if _, err = tx.orm.NewInsert().Model(&sources).Returning("NULL").Exec(ctx); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) WorkerAssetSession(ctx context.Context, org, hash string, completion bool) (domain.WorkerAssetSession, error) {
	column := "upload_hash"
	if completion {
		column = "completion_hash"
	}
	var row models.AssetSession
	err := s.orm.NewSelect().Model(&row).Where("org_id = ?", org).Where(column+" = ?", hash).Where("expires_at > now()").Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.WorkerAssetSession{}, domain.Problem(401, "ASSET_UPLOAD_EXPIRED", "Asset upload token is invalid or expired")
	}
	if err != nil {
		return domain.WorkerAssetSession{}, err
	}
	a := domain.WorkerAssetSession{ID: row.SessionID, Organization: row.OrgID, Worker: row.WorkerID, UploadHash: row.UploadHash, CompletionHash: row.CompletionHash, Created: row.CreatedAt, Expires: row.ExpiresAt}
	var files []models.AssetSessionFile
	if err = s.orm.NewSelect().Model(&files).Where("session_id = ?", a.ID).OrderExpr("path ASC").Scan(ctx); err != nil {
		return a, err
	}
	for _, f := range files {
		a.Files = append(a.Files, domain.WorkerAssetSessionFile{Path: f.Path, Hash: f.Hash, Size: f.SizeBytes, ObjectKey: f.ObjectKey, ContentType: f.ContentType, Uploaded: f.Uploaded})
	}
	return a, nil
}

func (s *Store) MarkWorkerAssetUploaded(ctx context.Context, id, hash, contentType string) error {
	result, err := s.orm.NewUpdate().Model((*models.AssetSessionFile)(nil)).Set("uploaded = true").Set("content_type = ?", contentType).Where("session_id = ? AND hash = ?", id, hash).Where("EXISTS (SELECT 1 FROM krun_worker_asset_sessions s WHERE s.session_id = ? AND s.expires_at > now())", id).Exec(ctx)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return domain.NotFound()
	}
	return err
}
