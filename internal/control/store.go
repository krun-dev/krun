package control

import (
	"context"
	"sort"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store serializes IAM changes and protected operations on one durable snapshot.
// The PostgreSQL row lock is shared across replicas; memory is only for tests.
// No upstream HTTP calls may run inside Update.
type Store struct {
	pool   *pgxpool.Pool
	mu     sync.Mutex
	memory *State
	// Pending audit entries gathered during an Update; flushed on commit.
	pendingAudits []Audit
	// memoryAudits backs the in-memory store used by tests.
	memoryAudits []Audit
}

func OpenStore(ctx context.Context, dsn string) (*Store, error) {
	if dsn == "" {
		return nil, errors.New("PostgreSQL db required")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("invalid PostgreSQL db connection string")
	}
	// Detect connections invalidated by a quick database restart before beginning
	// the transaction; never retry an already-running state mutation.
	config.ShouldPing = func(context.Context, pgxpool.ShouldPingParams) bool { return true }
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	_, err = pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS krun_control_state (id integer PRIMARY KEY CHECK (id=1), data jsonb NOT NULL); INSERT INTO krun_control_state(id,data) VALUES(1,'{}') ON CONFLICT DO NOTHING`)
	if err != nil {
		pool.Close()
		return nil, err
	}
	// Audits are an append-only event stream in their own table; the state
	// blob stays small and hot.
	if _, err = pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS krun_audits (id text PRIMARY KEY, time timestamptz NOT NULL, actor text NOT NULL DEFAULT '', credential text NOT NULL DEFAULT '', key_id text NOT NULL DEFAULT '', action text NOT NULL DEFAULT '', resource text NOT NULL DEFAULT '', organization_id text NOT NULL DEFAULT '', allowed boolean NOT NULL DEFAULT false, request_id text NOT NULL DEFAULT '', revision integer NOT NULL DEFAULT 0); CREATE INDEX IF NOT EXISTS krun_audits_time ON krun_audits (time DESC)`); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}
func (s *Store) Close() {
	if s.pool != nil {
		s.pool.Close()
	}
}
func (s *Store) Update(ctx context.Context, fn func(*State) error) error {
	if s.pool != nil {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		var raw []byte
		if err = tx.QueryRow(ctx, `SELECT data FROM krun_control_state WHERE id=1 FOR UPDATE`).Scan(&raw); err != nil {
			return err
		}
		state := newState()
		if err = json.Unmarshal(raw, state); err != nil {
			return err
		}
		s.pendingAudits = nil
		if err = fn(state); err != nil {
			return err
		}
		raw, err = json.Marshal(state)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE krun_control_state SET data=$1 WHERE id=1`, raw); err != nil {
			return err
		}
		for _, a := range s.pendingAudits {
			if _, err = tx.Exec(ctx, `INSERT INTO krun_audits (id, time, actor, credential, key_id, action, resource, organization_id, allowed, request_id, revision) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT (id) DO NOTHING`, a.ID, a.Time, a.Actor, a.Credential, a.KeyID, a.Action, a.Resource, a.Organization, a.Allowed, a.RequestID, a.Revision); err != nil {
				return err
			}
		}
		s.pendingAudits = nil
		return tx.Commit(ctx)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.memory == nil {
		return errors.New("store is not initialized")
	}
	s.pendingAudits = nil
	state := newState()
	raw, err := json.Marshal(s.memory)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(raw, state); err != nil {
		return err
	}
	if err = fn(state); err != nil {
		s.pendingAudits = nil
		return err
	}
	s.memory = state
	s.memoryAudits = append(s.memoryAudits, s.pendingAudits...)
	s.memoryAudits = pruneMemoryAudits(s.memoryAudits, now())
	s.pendingAudits = nil
	return nil
}

// recordAudit queues an entry for the current Update transaction.
func (s *Store) recordAudit(a Audit) {
	s.pendingAudits = append(s.pendingAudits, a)
}
func pruneMemoryAudits(entries []Audit, at time.Time) []Audit {
	cutoff := at.Add(-auditRetention)
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
func (s *Store) readAudits(ctx context.Context, cutoff time.Time, organization string, limit int) ([]Audit, error) {
	if s.pool != nil {
		rows, err := s.pool.Query(ctx, `SELECT id, time, actor, credential, key_id, action, resource, organization_id, allowed, request_id, revision FROM krun_audits WHERE time >= $1 AND ($2 = '' OR organization_id = $2) ORDER BY time DESC, id DESC LIMIT $3`, cutoff, organization, limit)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []Audit{}
		for rows.Next() {
			var a Audit
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
	all := pruneMemoryAudits(append([]Audit(nil), s.memoryAudits...), now())
	sort.Slice(all, func(i, j int) bool {
		if all[i].Time.Equal(all[j].Time) {
			return all[i].ID > all[j].ID
		}
		return all[i].Time.After(all[j].Time)
	})
	out := []Audit{}
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
func (s *Store) deleteAuditsBefore(ctx context.Context, cutoff time.Time) error {
	if s.pool != nil {
		_, err := s.pool.Exec(ctx, `DELETE FROM krun_audits WHERE time < $1`, cutoff)
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.memoryAudits = pruneMemoryAudits(s.memoryAudits, now())
	return nil
}
func Bootstrap(ctx context.Context, s *Store, user string) error {
	return s.Update(ctx, func(st *State) error {
		if st.Bootstrapped {
			return errors.New("bootstrap already completed")
		}
		u, ok := st.Users[user]
		if !ok || u.Status != "active" {
			return errors.New("an existing active local user_id is required")
		}
		id := randomID("role")
		st.Assignments[id] = Assignment{ID: id, SubjectKind: "user", SubjectID: user, Role: "system_admin", ScopeKind: "system", ScopeID: "system", Revision: 1}
		st.Bootstrapped = true
		st.Revision++
		s.recordAudit(Audit{ID: randomID("audit"), Time: now(), Actor: user, Credential: "bootstrap", Action: "system.bootstrap", Resource: "system", Allowed: true, Revision: st.Revision})
		return nil
	})
}
func Invite(ctx context.Context, s *Store, source, subject string) error {
	if source == "" || subject == "" || len(subject) > 512 {
		return errors.New("source and stable subject required")
	}
	return s.Update(ctx, func(st *State) error {
		st.Invitations[source+"\x00"+subject] = now().Add(7 * 24 * time.Hour)
		st.Revision++
		return nil
	})
}
