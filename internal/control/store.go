package control

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store persists the control plane as one row per entity.
//
// Update and View keep a callback shape so handlers stay readable, but they no
// longer read or write a whole document. Update loads only the partitions the
// mutation may touch, snapshots that state in memory, runs fn, then persists
// just the rows that differ. A mutation of one user never rewrites builds.
//
// No upstream HTTP calls may run inside Update.
type Store struct {
	pool   *pgxpool.Pool
	mu     sync.Mutex
	memory *State
	// Pending audit entries gathered during an Update; flushed on commit.
	pendingAudits []Audit
	// memoryAudits backs the in-memory store used by tests.
	memoryAudits []Audit
	// memTx is the working copy of an in-flight memory Update. Session helpers
	// write here so their changes are not overwritten by the clone commit.
	memTx *State
	// onPersist, when set, is called whenever a write would be committed. It
	// exists so a test can assert that read requests never write.
	onPersist func()

	// Config distribution state. Revision/ops live outside the control-plane
	// rows so runtime reads never touch them.
	configMu   sync.Mutex
	configCh   chan struct{}
	configRevs []workerConfigRevision
	configSeq  int64

	ackMu sync.Mutex
	acks  map[string]WorkerAck
}

// StatePartition selects which tables Update loads and persists. Keeping the
// halves apart means an IAM mutation never shares a transaction with Workers
// rows, which AGENTS.md section 9 requires.
type StatePartition int

const (
	// PartitionIAM covers identity, organizations, permissions and keys.
	PartitionIAM StatePartition = 1 << iota
	// PartitionWorkers covers services, builds, releases and operations.
	PartitionWorkers
	// PartitionAll loads both halves. Read paths may use it; a mutation must
	// not, because that would put IAM and Workers in one transaction.
	PartitionAll = PartitionIAM | PartitionWorkers
)

func OpenStore(ctx context.Context, dsn string) (*Store, error) {
	if dsn == "" {
		return nil, errors.New("PostgreSQL db required")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("invalid PostgreSQL db connection string")
	}
	// Detect connections invalidated by a quick database restart before beginning
	// a transaction; never retry an already-running state mutation.
	config.ShouldPing = func(context.Context, pgxpool.ShouldPingParams) bool { return true }
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	if _, err = pool.Exec(ctx, storeSchema); err != nil {
		pool.Close()
		return nil, err
	}
	// Audits are an append-only event stream in their own table.
	if _, err = pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS krun_audits (id text PRIMARY KEY, time timestamptz NOT NULL, actor text NOT NULL DEFAULT '', credential text NOT NULL DEFAULT '', key_id text NOT NULL DEFAULT '', action text NOT NULL DEFAULT '', resource text NOT NULL DEFAULT '', organization_id text NOT NULL DEFAULT '', allowed boolean NOT NULL DEFAULT false, request_id text NOT NULL DEFAULT '', revision integer NOT NULL DEFAULT 0); CREATE INDEX IF NOT EXISTS krun_audits_time ON krun_audits (time DESC)`); err != nil {
		pool.Close()
		return nil, err
	}
	// Runtime-facing edge configuration is a small append-only revision log.
	// Full snapshots are materialised to object storage; these rows are the
	// incremental feed and the bootstrap fallback.
	if _, err = pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS worker_config_revisions (config_revision bigserial PRIMARY KEY, digest text NOT NULL, payload jsonb NOT NULL, ops jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		pool.Close()
		return nil, err
	}
	// Acks are per runtime Pod, not part of the control-plane rows: they change
	// on a timer and must not rewrite anything else.
	if _, err = pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS worker_runtime_acks (pod_uid text PRIMARY KEY, pool text NOT NULL, revision bigint NOT NULL, updated_at timestamptz NOT NULL)`); err != nil {
		pool.Close()
		return nil, err
	}
	if _, err = pool.Exec(ctx, `INSERT INTO krun_meta (key, value) VALUES ('schema_version',$1) ON CONFLICT (key) DO UPDATE SET value=excluded.value`, storeSchemaVersion); err != nil {
		pool.Close()
		return nil, err
	}
	s := &Store{pool: pool}
	if err = s.importLegacySnapshot(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() {
	if s.pool != nil {
		s.pool.Close()
	}
}

// load reads one partition into a fresh State.
func (s *Store) load(ctx context.Context, partition StatePartition) (*State, error) {
	st := newState()
	if partition&PartitionIAM != 0 {
		if err := s.loadIAM(ctx, st); err != nil {
			return nil, err
		}
	}
	if partition&PartitionWorkers != 0 {
		if err := s.loadWorkers(ctx, st); err != nil {
			return nil, err
		}
	}
	return st, nil
}

// Open exposes a read-only snapshot of the requested partitions.
func (s *Store) Open(ctx context.Context, partition StatePartition) (*State, error) {
	return s.load(ctx, partition)
}

// Update loads the requested partitions, applies fn, and persists the diff.
// Returning an error from fn leaves the database untouched.
func (s *Store) Update(ctx context.Context, partition StatePartition, fn func(*State) error) error {
	return s.Mutate(ctx, partition, partition, fn)
}

// Mutate loads loadPartition for reading and persists only persistPartition.
// Workers handlers use it to authorize against IAM while writing Workers rows,
// which keeps IAM and Workers out of a shared transaction (AGENTS.md §9).
func (s *Store) Mutate(ctx context.Context, loadPartition, persistPartition StatePartition, fn func(*State) error) error {
	if s.pool != nil {
		st, err := s.load(ctx, loadPartition|persistPartition)
		if err != nil {
			return err
		}
		s.pendingAudits = nil
		// A read persists nothing, so it needs no pre-image to diff against.
		// Skipping the clone here keeps GET requests to one batched read.
		if persistPartition == 0 {
			if err = fn(st); err != nil {
				s.pendingAudits = nil
				return err
			}
			return s.flushAudits(ctx)
		}
		before, err := cloneState(st)
		if err != nil {
			return err
		}
		if err = fn(st); err != nil {
			s.pendingAudits = nil
			return err
		}
		if persistPartition&PartitionIAM != 0 {
			if err = s.persistIAM(ctx, before, st); err != nil {
				return err
			}
		}
		if persistPartition&PartitionWorkers != 0 {
			if err = s.persistWorkers(ctx, before, st); err != nil {
				return err
			}
		}
		return s.flushAudits(ctx)
	}
	return s.updateMemory(fn)
}

func (s *Store) updateMemory(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.memory == nil {
		return errors.New("store is not initialized")
	}
	s.pendingAudits = nil
	src := s.memory
	after, err := cloneState(s.memory)
	if err != nil {
		return err
	}
	// Session helpers called from inside fn must land on this working copy;
	// otherwise the assignment below would discard them.
	s.memTx = after
	defer func() { s.memTx = nil }()
	if err = fn(after); err != nil {
		s.pendingAudits = nil
		return err
	}
	s.memory = after
	if s.onPersist != nil && !sameStateContent(src, after) {
		s.onPersist()
	}
	s.memoryAudits = append(s.memoryAudits, s.pendingAudits...)
	s.memoryAudits = pruneMemoryAudits(s.memoryAudits, now())
	s.pendingAudits = nil
	return nil
}

// sameStateContent reports whether two snapshots carry identical data. It is
// only used by the read-path assertion in tests.
func sameStateContent(a, b *State) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	if err != nil || len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// memState is the state the single-row helpers operate on when there is no
// database. Memory stores are test-only and single-threaded.
func (s *Store) memState() *State {
	if s.memTx != nil {
		return s.memTx
	}
	return s.memory
}

// View runs fn against a read-only snapshot. It never writes.
func (s *Store) View(ctx context.Context, partition StatePartition, fn func(*State) error) error {
	if s.pool != nil {
		st, err := s.load(ctx, partition)
		if err != nil {
			return err
		}
		return fn(st)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.memory == nil {
		return errors.New("store is not initialized")
	}
	st, err := cloneState(s.memory)
	if err != nil {
		return err
	}
	return fn(st)
}

// flushAudits writes queued audit entries after the state rows are durable.
func (s *Store) flushAudits(ctx context.Context) error {
	if len(s.pendingAudits) == 0 {
		return nil
	}
	entries := s.pendingAudits
	s.pendingAudits = nil
	for _, a := range entries {
		if _, err := s.pool.Exec(ctx, `INSERT INTO krun_audits (id, time, actor, credential, key_id, action, resource, organization_id, allowed, request_id, revision) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT (id) DO NOTHING`, a.ID, a.Time, a.Actor, a.Credential, a.KeyID, a.Action, a.Resource, a.Organization, a.Allowed, a.RequestID, a.Revision); err != nil {
			return err
		}
	}
	return nil
}

// recordAudit queues an entry for the current Update.
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
	return s.Update(ctx, PartitionIAM, func(st *State) error {
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
	return s.Update(ctx, PartitionIAM, func(st *State) error {
		st.Invitations[source+"\x00"+subject] = now().Add(7 * 24 * time.Hour)
		st.Revision++
		return nil
	})
}

// cloneState deep-copies through JSON, matching the snapshot semantics the
// previous whole-document store provided.
func cloneState(src *State) (*State, error) {
	if src == nil {
		return nil, errors.New("state is not initialized")
	}
	raw, err := json.Marshal(src)
	if err != nil {
		return nil, err
	}
	dst := newState()
	if err = json.Unmarshal(raw, dst); err != nil {
		return nil, err
	}
	return dst, nil
}
