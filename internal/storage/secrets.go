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
				WHERE v.secret_version_id=$4 AND v.service_id=$1 AND (v.env_name=$2 OR ($2<>'production' AND v.env_name='@previews-base')) AND v.name=$3
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

func replaceWorkerVersionSecrets(ctx context.Context, tx pgx.Tx, version, service, environment string, versions map[string]string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM krun_worker_version_secrets WHERE version_id=$1`, version); err != nil {
		return err
	}
	for _, name := range domain.WorkerSecretNames(versions) {
		versionID := versions[name]
		tag, err := tx.Exec(ctx, `INSERT INTO krun_worker_version_secrets
			(version_id, name, secret_version_id)
			SELECT $1,$2,$3 WHERE EXISTS (
				SELECT 1 FROM krun_worker_secret_versions v
				WHERE v.secret_version_id=$3 AND v.service_id=$4 AND (v.env_name=$5 OR ($5<>'production' AND v.env_name='@previews-base')) AND v.name=$2
			)`, version, name, versionID, service, environment)
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
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.memory == nil {
			return "", nil, errors.New("store is not initialized")
		}
		version, ok := s.memory.Workers.Versions[versionID]
		if !ok || !domain.WorkerServiceAvailableInPool(s.memory.Workers, version.Service, runtimePool) {
			return "", nil, domain.NotFound()
		}
		out := make([]domain.EncryptedWorkerSecret, 0, len(version.SecretVersions))
		for name, versionID := range version.SecretVersions {
			encrypted, ok := s.memorySecretValues[versionID]
			if !ok || encrypted.Version.Name != name || encrypted.Version.Service != version.Service || encrypted.Version.Environment != version.Environment {
				return "", nil, errors.New("Worker secret binding is inconsistent")
			}
			out = append(out, encrypted)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Version.Name < out[j].Version.Name })
		return version.SecretDigest, out, nil
	}
	var digest string
	if err := s.pool.QueryRow(ctx, `SELECT version.secret_digest
		FROM krun_worker_versions version
		JOIN krun_worker_service_placements placement ON placement.service_id=version.service_id
			AND placement.org_id=version.org_id AND placement.pool_id=$2
			AND placement.state IN ('preparing','ready','active')
		WHERE version.version_id=$1`, versionID, runtimePool).Scan(&digest); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil, domain.NotFound()
		}
		return "", nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT v.secret_version_id, v.org_id, v.service_id, v.env_name,
		v.name, v.value_digest, v.key_version, v.created_by, v.created_at, v.ciphertext, v.nonce
		FROM krun_worker_version_secrets binding
		JOIN krun_worker_secret_versions v ON v.secret_version_id=binding.secret_version_id
		JOIN krun_worker_versions version ON version.version_id=binding.version_id
		WHERE binding.version_id=$1 AND binding.name=v.name
		  AND v.org_id=version.org_id AND v.service_id=version.service_id AND (v.env_name=version.env_name OR (version.env_name<>'production' AND v.env_name='@previews-base'))
		ORDER BY v.name`, versionID)
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
