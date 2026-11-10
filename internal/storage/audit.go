package storage

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/storage/models"
)

// flushAudits writes a request-local batch atomically. IAM mutations call the
// same insertion helper from their business transaction instead.
func (s *Store) flushAudits(ctx context.Context, entries []domain.Audit) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := s.beginORMTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = insertAudits(ctx, tx, entries); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func insertAudits(ctx context.Context, tx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, entries []domain.Audit) error {
	if len(entries) == 0 {
		return nil
	}
	if transaction, ok := tx.(ormTransaction); ok {
		rows := make([]models.Audit, 0, len(entries))
		for _, entry := range entries {
			rows = append(rows, models.Audit{ID: entry.ID, Time: entry.Time, Actor: entry.Actor, Credential: entry.Credential, KeyID: entry.KeyID, Action: entry.Action, Resource: entry.Resource, OrganizationID: entry.Organization, Allowed: entry.Allowed, RequestID: entry.RequestID, Revision: entry.Revision})
		}
		_, err := transaction.orm.NewInsert().Model(&rows).On("CONFLICT (id) DO NOTHING").Returning("NULL").Exec(ctx)
		return err
	}
	for _, a := range entries {
		if _, err := tx.Exec(ctx, `INSERT INTO krun_audits (id, time, actor, credential, key_id, action, resource, organization_id, allowed, request_id, revision) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT (id) DO NOTHING`, a.ID, a.Time, a.Actor, a.Credential, a.KeyID, a.Action, a.Resource, a.Organization, a.Allowed, a.RequestID, a.Revision); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) AppendAudits(ctx context.Context, entries []domain.Audit) error {
	if len(entries) == 0 {
		return nil
	}
	return s.flushAudits(ctx, entries)
}

func deniedAudits(entries []domain.Audit) []domain.Audit {
	denied := make([]domain.Audit, 0, len(entries))
	for _, entry := range entries {
		if !entry.Allowed {
			denied = append(denied, entry)
		}
	}
	return denied
}

// ReadAudits returns entries newer than cutoff, newest first, optionally
// scoped to an organization.
func (s *Store) ReadAudits(ctx context.Context, cutoff time.Time, organization string, limit int) ([]domain.Audit, error) {
	return s.ReadAuditSnapshot(ctx, cutoff, now(), organization, limit)
}

// ReadAuditSnapshot applies the time window before limiting the result so newer
// records cannot displace rows from a previous snapshot.
func (s *Store) ReadAuditSnapshot(ctx context.Context, cutoff, asOf time.Time, organization string, limit int) ([]domain.Audit, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, time, actor, credential, key_id, action, resource, organization_id, allowed, request_id, revision FROM krun_audits WHERE time BETWEEN $1 AND $2
			  AND ($3 = '' OR organization_id = $3)
			ORDER BY time DESC, id DESC LIMIT $4`, cutoff, asOf, organization, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Audit{}
	for rows.Next() {
		var a domain.Audit
		if err = rows.Scan(&a.ID, &a.Time, &a.Actor, &a.Credential, &a.KeyID, &a.Action, &a.Resource, &a.Organization, &a.Allowed, &a.RequestID, &a.Revision); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// deleteAuditsBefore drops entries older than the retention window.
func (s *Store) DeleteAuditsBefore(ctx context.Context, cutoff time.Time) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM krun_audits WHERE time < $1`, cutoff)
	return err
}
