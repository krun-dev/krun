package storage

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/krun-dev/krun/internal/domain"
)

// flushAudits writes a request-local batch atomically. IAM mutations call the
// same insertion helper from their business transaction instead.
func (s *Store) flushAudits(ctx context.Context, entries []domain.Audit) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
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
	if s.pool != nil {
		return s.flushAudits(ctx, entries)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.memory == nil {
		return errors.New("store is not initialized")
	}
	s.memoryAudits = append(s.memoryAudits, entries...)
	s.memoryAudits = PruneMemoryAudits(s.memoryAudits, now())
	return nil
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

func PruneMemoryAudits(entries []domain.Audit, at time.Time) []domain.Audit {
	cutoff := at.Add(-AuditRetention)
	kept := entries[:0]
	for _, entry := range entries {
		if !entry.Time.Before(cutoff) {
			kept = append(kept, entry)
		}
	}
	clear(entries[len(kept):])
	return kept
}

// readAudits returns entries newer than cutoff, newest first, optionally
// scoped to an organization.
func (s *Store) ReadAudits(ctx context.Context, cutoff time.Time, organization string, limit int) ([]domain.Audit, error) {
	if s.pool != nil {
		rows, err := s.pool.Query(ctx, `SELECT id, time, actor, credential, key_id, action, resource, organization_id, allowed, request_id, revision FROM krun_audits WHERE time >= $1 AND ($2 = '' OR organization_id = $2) ORDER BY time DESC, id DESC LIMIT $3`, cutoff, organization, limit)
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
	// No lock here: auditList runs inside protected()'s Update transaction,
	// so taking mu would self-deadlock. Tests using the memory store are
	// single-threaded.
	all := PruneMemoryAudits(append([]domain.Audit(nil), s.memoryAudits...), now())
	sort.Slice(all, func(i, j int) bool {
		if all[i].Time.Equal(all[j].Time) {
			return all[i].ID > all[j].ID
		}
		return all[i].Time.After(all[j].Time)
	})
	out := []domain.Audit{}
	for _, a := range all {
		if a.Time.Before(cutoff) || (organization != "" && a.Organization != organization) {
			continue
		}
		out = append(out, a)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// deleteAuditsBefore drops entries older than the retention window.
func (s *Store) DeleteAuditsBefore(ctx context.Context, cutoff time.Time) error {
	if s.pool != nil {
		_, err := s.pool.Exec(ctx, `DELETE FROM krun_audits WHERE time < $1`, cutoff)
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.memoryAudits[:0]
	for _, entry := range s.memoryAudits {
		if !entry.Time.Before(cutoff) {
			kept = append(kept, entry)
		}
	}
	clear(s.memoryAudits[len(kept):])
	s.memoryAudits = kept
	return nil
}
