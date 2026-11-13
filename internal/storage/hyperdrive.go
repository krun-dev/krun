package storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/storage/models"
)

func hyperdriveConfig(row models.HyperdriveConfig) domain.HyperdriveConfig {
	return domain.HyperdriveConfig{
		ID: row.ConfigID, Organization: row.OrgID, Name: row.Name, Host: row.Host,
		Port: row.Port, Database: row.DatabaseName, User: row.UserName,
		Revision: row.Revision, Created: row.CreatedAt,
	}
}

func (s *Store) ListHyperdrive(ctx context.Context, org string) ([]domain.HyperdriveConfig, error) {
	var rows []models.HyperdriveConfig
	err := s.orm.NewSelect().Model(&rows).
		Column("config_id", "org_id", "name", "host", "port", "database_name", "user_name", "revision", "created_at").
		Where("org_id = ?", org).Order("name ASC").Scan(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]domain.HyperdriveConfig, 0, len(rows))
	for _, row := range rows {
		result = append(result, hyperdriveConfig(row))
	}
	return result, nil
}

func (s *Store) GetHyperdrive(ctx context.Context, org, id string) (domain.HyperdriveConfig, error) {
	var row models.HyperdriveConfig
	err := s.orm.NewSelect().Model(&row).
		Column("config_id", "org_id", "name", "host", "port", "database_name", "user_name", "revision", "created_at").
		Where("org_id = ? AND config_id = ?", org, id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		err = domain.NotFound()
	}
	return hyperdriveConfig(row), err
}

func (s *Store) CreateHyperdrive(ctx context.Context, origin domain.HyperdriveOrigin) error {
	secret := origin.Secret
	row := models.HyperdriveConfig{
		ConfigID: origin.ID, OrgID: origin.Organization, Name: origin.Name, Host: origin.Host,
		Port: origin.Port, DatabaseName: origin.Database, UserName: origin.User,
		SecretID: secret.Version.ID, ValueDigest: secret.Version.ValueDigest, KeyVersion: secret.Version.KeyVersion,
		Ciphertext: secret.Ciphertext, Nonce: secret.Nonce, Revision: origin.Revision, CreatedAt: origin.Created,
	}
	_, err := s.orm.NewInsert().Model(&row).Returning("NULL").Exec(ctx)
	return hyperdriveWriteError(err)
}

func (s *Store) UpdateHyperdrive(ctx context.Context, origin domain.HyperdriveOrigin) error {
	secret := origin.Secret
	var row models.HyperdriveConfig
	query := s.orm.NewUpdate().Model(&row).Set("name = ?", origin.Name).
		Set("revision = revision + 1").
		Where("org_id = ? AND config_id = ? AND revision = ?", origin.Organization, origin.ID, origin.Revision)
	if secret.Version.ID != "" {
		query = query.Set("secret_id = ?", secret.Version.ID).
			Set("key_version = ?", secret.Version.KeyVersion).
			Set("value_digest = ?", secret.Version.ValueDigest).
			Set("ciphertext = ?", secret.Ciphertext).
			Set("nonce = ?", secret.Nonce)
	}
	result, err := query.Exec(ctx)
	if err != nil {
		return hyperdriveWriteError(err)
	}
	count, err := result.RowsAffected()
	if err == nil && count != 1 {
		err = domain.Conflict("REVISION_CONFLICT", "Hyperdrive configuration changed")
	}
	return err
}

func (s *Store) DeleteHyperdrive(ctx context.Context, org, id string) error {
	var row models.HyperdriveConfig
	result, err := s.orm.NewDelete().Model(&row).Where("org_id = ? AND config_id = ?", org, id).Exec(ctx)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count != 1 {
		err = domain.NotFound()
	}
	return err
}

func hyperdriveWriteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return domain.Conflict("HYPERDRIVE_ALREADY_EXISTS", "Hyperdrive name already exists")
	}
	return err
}

// RuntimeHyperdriveOrigins reads the encrypted bindings of a version in one
// statement. A version with bindings must be served by the active primary pool.
func (s *Store) RuntimeHyperdriveOrigins(ctx context.Context, version, pool string) (map[string]domain.HyperdriveOrigin, error) {
	rows, err := s.pool.Query(ctx, `
SELECT binding.name, h.config_id, h.org_id, h.name, h.host, h.port, h.database_name,
	h.user_name, h.secret_id, h.key_version, h.value_digest, h.ciphertext, h.nonce,
	h.revision, h.created_at
FROM krun_worker_versions v
JOIN krun_worker_artifacts a ON a.artifact_id=v.artifact_id AND a.state='finalized'
JOIN krun_worker_placements p ON p.worker_id=v.worker_id AND p.org_id=v.org_id
	AND p.pool_id=$2 AND p.state IN ('preparing','ready','active')
JOIN krun_resources r ON r.resource_id=p.worker_id AND r.kind='worker' AND r.org_id=p.org_id
LEFT JOIN LATERAL jsonb_each_text(NULLIF(v.hyperdrive_bindings, 'null'::jsonb)) binding(name, config_id) ON true
LEFT JOIN krun_worker_hyperdrive_configs h ON h.config_id=binding.config_id AND h.org_id=v.org_id
WHERE v.version_id=$1 AND (
	binding.name IS NULL OR (
		p.role='primary' AND p.state='active' AND EXISTS (
			SELECT 1 FROM krun_worker_environments e
			WHERE e.worker_id=v.worker_id AND e.env_name=v.env_name AND e.enabled
			AND (v.version_id IN (e.version_id,e.candidate_version_id) OR EXISTS (
				SELECT 1 FROM krun_worker_deployments d
				WHERE v.version_id IN (d.version_id,d.candidate_version_id) AND d.worker_id=v.worker_id
				AND d.org_id=v.org_id AND d.env_name=v.env_name
				AND d.committed_at IS NOT NULL AND d.expires_at>now()
			))
		)
	)
)`, version, pool)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	origins := make(map[string]domain.HyperdriveOrigin)
	found := false
	for rows.Next() {
		found = true
		// The left joins distinguish a version with no bindings (NULL binding
		// name) from a binding whose configuration is missing (NULL config ID).
		values := rows.RawValues()
		if values[0] == nil {
			continue
		}
		if values[1] == nil {
			return nil, domain.NotFound()
		}
		var binding string
		var row models.HyperdriveConfig
		if err := rows.Scan(
			&binding, &row.ConfigID, &row.OrgID, &row.Name, &row.Host, &row.Port, &row.DatabaseName,
			&row.UserName, &row.SecretID, &row.KeyVersion, &row.ValueDigest, &row.Ciphertext, &row.Nonce,
			&row.Revision, &row.CreatedAt,
		); err != nil {
			return nil, err
		}
		origins[binding] = encryptedHyperdriveOrigin(row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !found {
		return nil, domain.NotFound()
	}
	return origins, nil
}

func encryptedHyperdriveOrigin(row models.HyperdriveConfig) domain.HyperdriveOrigin {
	return domain.HyperdriveOrigin{
		HyperdriveConfig: hyperdriveConfig(row),
		Secret: domain.EncryptedWorkerSecret{
			Version: domain.WorkerSecretVersion{
				ID: row.SecretID, Organization: row.OrgID, Worker: row.ConfigID,
				Environment: "hyperdrive", Name: "password", ValueDigest: row.ValueDigest, KeyVersion: row.KeyVersion,
			},
			Ciphertext: row.Ciphertext, Nonce: row.Nonce,
		},
	}
}
