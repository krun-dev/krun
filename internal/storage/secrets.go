package storage

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/krun-dev/krun/internal/domain"
)

func writeWorkerSecretVersions(ctx context.Context, tx pgx.Tx, before, after *domain.WorkerState, pending map[string]domain.EncryptedWorkerSecret) error {
	for id, version := range after.SecretVersions {
		if old, exists := before.SecretVersions[id]; exists {
			if !domain.SameWorkerSecretVersion(old, version) {
				return errors.New("Worker secret version is immutable")
			}
			continue
		}
		encrypted, ok := pending[id]
		if !ok || !domain.SameWorkerSecretVersion(encrypted.Version, version) {
			return errors.New("Worker secret ciphertext is missing")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO krun_worker_secret_versions
			(secret_version_id, org_id, worker_id, env_name, name, ciphertext, nonce,
			 key_version, value_digest, created_by, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, version.ID,
			version.Organization, version.Worker, version.Environment, version.Name,
			encrypted.Ciphertext, encrypted.Nonce, version.KeyVersion, version.ValueDigest,
			version.CreatedBy, version.Created); err != nil {
			return err
		}
	}
	return nil
}

func replaceWorkerEnvironmentSecrets(ctx context.Context, tx pgx.Tx, worker, environment string, versions map[string]string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM krun_worker_environment_secrets WHERE worker_id=$1 AND env_name=$2`, worker, environment); err != nil {
		return err
	}
	for _, name := range domain.WorkerSecretNames(versions) {
		versionID := versions[name]
		tag, err := tx.Exec(ctx, `INSERT INTO krun_worker_environment_secrets
			(worker_id, env_name, name, secret_version_id)
			SELECT $1,$2,$3,$4 WHERE EXISTS (
				SELECT 1 FROM krun_worker_secret_versions v
				WHERE v.secret_version_id=$4 AND v.worker_id=$1 AND (v.env_name=$2 OR ($2<>'production' AND v.env_name='@previews-base')) AND v.name=$3
			)`, worker, environment, name, versionID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("Worker environment secret version is unavailable")
		}
	}
	return nil
}

func replaceWorkerVersionSecrets(ctx context.Context, tx pgx.Tx, version, worker, environment string, versions map[string]string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM krun_worker_version_secrets WHERE version_id=$1`, version); err != nil {
		return err
	}
	for _, name := range domain.WorkerSecretNames(versions) {
		versionID := versions[name]
		tag, err := tx.Exec(ctx, `INSERT INTO krun_worker_version_secrets
			(version_id, name, secret_version_id)
			SELECT $1,$2,$3 WHERE EXISTS (
				SELECT 1 FROM krun_worker_secret_versions v
				WHERE v.secret_version_id=$3 AND v.worker_id=$4 AND (v.env_name=$5 OR ($5<>'production' AND v.env_name='@previews-base')) AND v.name=$2
			)`, version, name, versionID, worker, environment)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("Worker version secret version is unavailable")
		}
	}
	return nil
}

// encryptedVersionSecrets reads exactly one Version's bindings. Secret values
// are never included in loadWorkers, edge snapshots, or generic state clones.
func (s *Store) EncryptedVersionSecrets(ctx context.Context, versionID, runtimePool string) (string, []domain.EncryptedWorkerSecret, error) {
	var digest string
	if err := s.pool.QueryRow(ctx, `SELECT version.secret_digest
		FROM krun_worker_versions version
		JOIN krun_worker_placements placement ON placement.worker_id=version.worker_id
			AND placement.org_id=version.org_id AND placement.pool_id=$2
			AND placement.state IN ('preparing','ready','active')
		WHERE version.version_id=$1`, versionID, runtimePool).Scan(&digest); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil, domain.NotFound()
		}
		return "", nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT v.secret_version_id, v.org_id, v.worker_id, v.env_name,
		v.name, v.value_digest, v.key_version, v.created_by, v.created_at, v.ciphertext, v.nonce
		FROM krun_worker_version_secrets binding
		JOIN krun_worker_secret_versions v ON v.secret_version_id=binding.secret_version_id
		JOIN krun_worker_versions version ON version.version_id=binding.version_id
		WHERE binding.version_id=$1 AND binding.name=v.name
		  AND v.org_id=version.org_id AND v.worker_id=version.worker_id AND (v.env_name=version.env_name OR (version.env_name<>'production' AND v.env_name='@previews-base'))
		ORDER BY v.name`, versionID)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	out := []domain.EncryptedWorkerSecret{}
	for rows.Next() {
		var item domain.EncryptedWorkerSecret
		if err = rows.Scan(&item.Version.ID, &item.Version.Organization, &item.Version.Worker,
			&item.Version.Environment, &item.Version.Name, &item.Version.ValueDigest,
			&item.Version.KeyVersion, &item.Version.CreatedBy, &item.Version.Created,
			&item.Ciphertext, &item.Nonce); err != nil {
			return "", nil, err
		}
		out = append(out, item)
	}
	if err = rows.Err(); err != nil {
		return "", nil, err
	}
	if digest != "" && len(out) == 0 {
		return "", nil, errors.New("Worker secret bindings are missing")
	}
	return digest, out, nil
}
