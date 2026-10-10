package control

import (
	"context"
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
		pruneAudits(state, now())
		if err = fn(state); err != nil {
			return err
		}
		pruneAudits(state, now())
		raw, err = json.Marshal(state)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE krun_control_state SET data=$1 WHERE id=1`, raw); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.memory == nil {
		return errors.New("store is not initialized")
	}
	state := newState()
	raw, err := json.Marshal(s.memory)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(raw, state); err != nil {
		return err
	}
	pruneAudits(state, now())
	if err = fn(state); err != nil {
		return err
	}
	pruneAudits(state, now())
	s.memory = state
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
		st.Audits = append(st.Audits, Audit{ID: randomID("audit"), Time: now(), Actor: user, Credential: "bootstrap", Action: "system.bootstrap", Resource: "system", Allowed: true, Revision: st.Revision})
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
