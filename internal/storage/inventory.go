package storage

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/krun-dev/krun/internal/domain"
)

type InventoryResource struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Organization     string `json:"organization_id"`
	OrganizationName string `json:"organization_name"`
	Enabled          *bool  `json:"enabled,omitempty"`
}

type InventoryKey struct {
	domain.APIKey
	OrganizationName string `json:"organization_name,omitempty"`
}

const workerInventory = `
	SELECT r.resource_id AS id, r.name, r.org_id, o.name AS organization_name, NULL::boolean AS enabled, o.short_name, r.locator
	FROM krun_resources r
	JOIN krun_organizations o ON o.org_id = r.org_id
	WHERE r.kind = 'worker'
`

const bucketInventory = `
	SELECT b.bucket_id AS id, b.name, b.org_id, o.name AS organization_name, b.enabled, o.short_name, '' AS locator
	FROM krun_worker_buckets b
	JOIN krun_organizations o ON o.org_id = b.org_id
`

func (s *Store) ListWorkerInventory(ctx context.Context, after, search string, limit int) ([]InventoryResource, error) {
	return s.listResourceInventory(ctx, workerInventory, after, search, limit)
}

func (s *Store) ListBucketInventory(ctx context.Context, after, search string, limit int) ([]InventoryResource, error) {
	return s.listResourceInventory(ctx, bucketInventory, after, search, limit)
}

func (s *Store) listResourceInventory(ctx context.Context, projection, after, search string, limit int) ([]InventoryResource, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, org_id, organization_name, enabled
		FROM (`+projection+`) inventory
		WHERE id > $1
		  AND ($2 = '' OR strpos(lower(concat_ws(' ', id, name, org_id, organization_name, short_name, locator)), lower($2)) > 0)
		ORDER BY id
		LIMIT $3
	`, after, search, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (InventoryResource, error) {
		var item InventoryResource
		err := row.Scan(&item.ID, &item.Name, &item.Organization, &item.OrganizationName, &item.Enabled)
		return item, err
	})
}

// ListKeyInventory projects API key metadata without reading authentication hashes.
func (s *Store) ListKeyInventory(ctx context.Context, after, search, status string, limit int) ([]InventoryKey, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT k.key_id, k.owner_user_id, k.org_id, k.scope_kind, k.name, k.prefix, k.policy,
		       k.expires_at, k.revoked, k.revision, coalesce(o.name, '')
		FROM krun_api_keys k
		LEFT JOIN krun_organizations o ON o.org_id = k.org_id
		WHERE k.key_id > $1
		  AND ($2 = '' OR strpos(lower(concat_ws(' ', k.key_id, k.name, k.prefix, k.owner_user_id, k.org_id, o.name, o.short_name)), lower($2)) > 0)
		  AND ($3 = 'all'
		    OR ($3 = 'revoked' AND k.revoked)
		    OR ($3 = 'expired' AND NOT k.revoked AND k.expires_at <= now())
		    OR ($3 = 'active' AND NOT k.revoked AND (k.expires_at IS NULL OR k.expires_at > now())))
		ORDER BY k.key_id
		LIMIT $4
	`, after, search, status, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (InventoryKey, error) {
		var key InventoryKey
		err := row.Scan(
			&key.ID, &key.Owner, &key.Organization, &key.ScopeKind, &key.Name, &key.Prefix,
			&key.Policy, &key.Expires, &key.Revoked, &key.Revision, &key.OrganizationName,
		)
		return key, err
	})
}
