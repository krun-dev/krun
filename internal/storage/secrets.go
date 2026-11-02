package storage

import (
	"context"
	"errors"
	"sort"

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
			(secret_version_id, org_id, service_id, env_name, name, ciphertext, nonce,
			 key_version, value_digest, created_by, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, version.ID,
			version.Organization, version.Service, version.Environment, version.Name,
			encrypted.Ciphertext, encrypted.Nonce, version.KeyVersion, version.ValueDigest,
			version.CreatedBy, version.Created); err != nil {
			return err
		}
	}
	return nil
}

func replaceWorkerEnvironmentSecrets(ctx context.Context, tx pgx.Tx, service, environment string, versions map[string]string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM krun_worker_environment_secrets WHERE service_id=$1 AND env_name=$2`, service, environment); err != nil {
		return err
	}
	for _, name := range domain.WorkerSecretNames(versions) {
		versionID := versions[name]
		tag, err := tx.Exec(ctx, `INSERT INTO krun_worker_environment_secrets
			(service_id, env_name, name, secret_version_id)
			SELECT $1,$2,$3,$4 WHERE EXISTS (
				SELECT 1 FROM krun_worker_secret_versions v
				WHERE v.secret_version_id=$4 AND v.service_id=$1 AND v.env_name=$2 AND v.name=$3
			)`, service, environment, name, versionID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("Worker environment secret version is unavailable")
		}
	}
	return nil
}

func replaceWorkerReleaseSecrets(ctx context.Context, tx pgx.Tx, release, service, environment string, versions map[string]string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM krun_worker_release_secrets WHERE release_id=$1`, release); err != nil {
		return err
	}
	for _, name := range domain.WorkerSecretNames(versions) {
		versionID := versions[name]
		tag, err := tx.Exec(ctx, `INSERT INTO krun_worker_release_secrets
			(release_id, name, secret_version_id)
			SELECT $1,$2,$3 WHERE EXISTS (
				SELECT 1 FROM krun_worker_secret_versions v
				WHERE v.secret_version_id=$3 AND v.service_id=$4 AND v.env_name=$5 AND v.name=$2
			)`, release, name, versionID, service, environment)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("Worker release secret version is unavailable")
		}
	}
	return nil
}

// encryptedReleaseSecrets reads exactly one Release's bindings. Secret values
// are never included in loadWorkers, edge snapshots, or generic state clones.
func (s *Store) EncryptedReleaseSecrets(ctx context.Context, releaseID, runtimePool string) (string, []domain.EncryptedWorkerSecret, error) {
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.memory == nil {
			return "", nil, errors.New("store is not initialized")
		}
		release, ok := s.memory.Workers.Releases[releaseID]
		if !ok || !domain.WorkerServiceAvailableInPool(s.memory.Workers, release.Service, runtimePool) {
			return "", nil, domain.NotFound()
		}
		out := make([]domain.EncryptedWorkerSecret, 0, len(release.SecretVersions))
		for name, versionID := range release.SecretVersions {
			encrypted, ok := s.memorySecretValues[versionID]
			if !ok || encrypted.Version.Name != name || encrypted.Version.Service != release.Service || encrypted.Version.Environment != release.Environment {
				return "", nil, errors.New("Worker secret binding is inconsistent")
			}
			out = append(out, encrypted)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Version.Name < out[j].Version.Name })
		return release.SecretDigest, out, nil
	}
	var digest string
	if err := s.pool.QueryRow(ctx, `SELECT release.secret_digest
		FROM krun_worker_releases release
		JOIN krun_worker_service_placements placement ON placement.service_id=release.service_id
			AND placement.org_id=release.org_id AND placement.pool_id=$2
			AND placement.state IN ('preparing','ready','active')
		WHERE release.release_id=$1`, releaseID, runtimePool).Scan(&digest); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil, domain.NotFound()
		}
		return "", nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT v.secret_version_id, v.org_id, v.service_id, v.env_name,
		v.name, v.value_digest, v.key_version, v.created_by, v.created_at, v.ciphertext, v.nonce
		FROM krun_worker_release_secrets binding
		JOIN krun_worker_secret_versions v ON v.secret_version_id=binding.secret_version_id
		JOIN krun_worker_releases release ON release.release_id=binding.release_id
		WHERE binding.release_id=$1 AND binding.name=v.name
		  AND v.org_id=release.org_id AND v.service_id=release.service_id AND v.env_name=release.env_name
		ORDER BY v.name`, releaseID)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	out := []domain.EncryptedWorkerSecret{}
	for rows.Next() {
		var item domain.EncryptedWorkerSecret
		if err = rows.Scan(&item.Version.ID, &item.Version.Organization, &item.Version.Service,
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
