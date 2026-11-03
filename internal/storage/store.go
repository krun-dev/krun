package storage

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/uptrace/bun"
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
	oauthAuthorizations map[string]domain.OAuthAuthorization
	oauthGrants         map[string]domain.OAuthGrant
	oauthRefresh        map[string]domain.OAuthRefresh
	scripts             map[string]domain.WorkerScript
	pool                *pgxpool.Pool
	orm                 *bun.DB
	mu                  sync.Mutex
	memory              *domain.State
	// memoryAudits backs the in-memory store used by tests.
	memoryAudits []domain.Audit
	// memoryRateLimits backs distributed rate-limit buckets in memory tests.
	rateMu           sync.Mutex
	memoryRateLimits map[rateLimitBucket]int
	// memTx is the working copy of an in-flight memory Update. Session helpers
	// write here so their changes are not overwritten by the clone commit.
	memTx *domain.State
	// onPersist, when set, is called whenever a write would be committed. It
	// exists so a test can assert that read requests never write.
	onPersist func()

	// Config distribution state. Revision/ops live outside the control-plane
	// rows so runtime reads never touch them.
	configMu   sync.Mutex
	configCh   chan struct{}
	configRevs []domain.WorkerConfigRevision
	configSeq  int64

	ackMu sync.Mutex
	acks  map[string]domain.WorkerAck
	// memorySecretValues backs encrypted secret rows in the test-only store.
	memorySecretValues map[string]domain.EncryptedWorkerSecret
}

// StatePartition selects which tables Update loads and persists. Keeping the
// halves apart means an IAM mutation never shares a transaction with Workers
// rows, which AGENTS.md section 9 requires.
type StatePartition int

const (
	// PartitionIAM covers identity, organizations, permissions and keys.
	PartitionIAM StatePartition = 1 << iota
	// PartitionWorkers covers services, builds, versions and operations.
	PartitionWorkers
	// PartitionAll loads both halves. Read paths may use it; a mutation must
	// not, because that would put IAM and Workers in one transaction.
	PartitionAll = PartitionIAM | PartitionWorkers
)

func Open(ctx context.Context, dsn string) (*Store, error) {
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
	if err = initializeStoreSchema(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool, orm: newORM(pool)}, nil
}

func (s *Store) Close() {
	if s.orm != nil {
		_ = s.orm.Close()
	}
	if s.pool != nil {
		s.pool.Close()
	}
}

// Ping verifies that the store can serve a query without loading any control
// plane tables. The in-memory implementation is used only by tests, where an
// initialized state is the equivalent readiness check.
func (s *Store) Ping(ctx context.Context) error {
	if s.pool != nil {
		var one int
		return s.pool.QueryRow(ctx, `SELECT 1`).Scan(&one)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.memory == nil {
		return errors.New("store is not initialized")
	}
	return nil
}

// load reads one partition into a fresh State.
func (s *Store) load(ctx context.Context, partition StatePartition) (*domain.State, error) {
	st := domain.NewState()
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
func (s *Store) Open(ctx context.Context, partition StatePartition) (*domain.State, error) {
	return s.load(ctx, partition)
}

// Update loads the requested partitions, applies fn, and persists the diff.
// Returning an error from fn leaves the database untouched.
func (s *Store) Update(ctx context.Context, partition StatePartition, fn func(*domain.State) error) error {
	return s.Mutate(ctx, partition, partition, fn)
}

// Mutate loads loadPartition for reading and persists only persistPartition.
// Workers handlers use it to authorize against IAM while writing Workers rows,
// which keeps IAM and Workers out of a shared transaction (AGENTS.md §9).
func (s *Store) Mutate(ctx context.Context, loadPartition, persistPartition StatePartition, fn func(*domain.State) error) error {
	if s.pool != nil {
		st, err := s.load(ctx, loadPartition|persistPartition)
		if err != nil {
			return err
		}
		// A read persists nothing, so it needs no pre-image to diff against.
		// Skipping the clone here keeps GET requests to one batched read.
		if persistPartition == 0 {
			if err = fn(st); err != nil {
				if auditErr := s.flushAudits(ctx, deniedAudits(st.PendingAudits)); auditErr != nil {
					return auditErr
				}
				return err
			}
			return s.flushAudits(ctx, st.PendingAudits)
		}
		before, err := domain.CloneState(st)
		if err != nil {
			return err
		}
		if err = fn(st); err != nil {
			if auditErr := s.flushAudits(ctx, deniedAudits(st.PendingAudits)); auditErr != nil {
				return auditErr
			}
			return err
		}
		if persistPartition&PartitionIAM != 0 {
			if err = s.persistIAM(ctx, before, st, st.PendingAudits); err != nil {
				return err
			}
			st.PendingAudits = nil
		}
		if persistPartition&PartitionWorkers != 0 {
			if err = s.persistWorkers(ctx, before, st, st.PendingAudits); err != nil {
				return err
			}
			st.PendingAudits = nil
		}
		return s.flushAudits(ctx, st.PendingAudits)
	}
	if persistPartition == 0 {
		return s.readMemory(fn)
	}
	return s.updateMemory(fn)
}

// readMemory gives tests the same request-local snapshot semantics as the
// PostgreSQL read path. Handler mutations are discarded, while audit entries
// are copied to their separately retained stream.
func (s *Store) readMemory(fn func(*domain.State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.memory == nil {
		return errors.New("store is not initialized")
	}
	st, err := domain.CloneState(s.memory)
	if err != nil {
		return err
	}
	err = fn(st)
	audits := st.PendingAudits
	if err != nil {
		audits = deniedAudits(audits)
	}
	s.memoryAudits = append(s.memoryAudits, audits...)
	s.memoryAudits = PruneMemoryAudits(s.memoryAudits, now())
	return err
}

func (s *Store) updateMemory(fn func(*domain.State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.memory == nil {
		return errors.New("store is not initialized")
	}
	src := s.memory
	after, err := domain.CloneState(s.memory)
	if err != nil {
		return err
	}
	// Session helpers called from inside fn must land on this working copy;
	// otherwise the assignment below would discard them.
	s.memTx = after
	defer func() { s.memTx = nil }()
	if err = fn(after); err != nil {
		s.memoryAudits = append(s.memoryAudits, deniedAudits(after.PendingAudits)...)
		s.memoryAudits = PruneMemoryAudits(s.memoryAudits, now())
		return err
	}
	if len(after.PendingWorkerSecrets) > 0 {
		if s.memorySecretValues == nil {
			s.memorySecretValues = map[string]domain.EncryptedWorkerSecret{}
		}
		for id, secret := range after.PendingWorkerSecrets {
			s.memorySecretValues[id] = secret
		}
		after.PendingWorkerSecrets = nil
	}
	audits := after.PendingAudits
	after.PendingAudits = nil
	s.memory = after
	if s.onPersist != nil && !sameStateContent(src, after) {
		s.onPersist()
	}
	s.memoryAudits = append(s.memoryAudits, audits...)
	s.memoryAudits = PruneMemoryAudits(s.memoryAudits, now())
	return nil
}

// sameStateContent reports whether two snapshots carry identical data. It is
// only used by the read-path assertion in tests.
func sameStateContent(a, b *domain.State) bool {
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
func (s *Store) memState() *domain.State {
	if s.memTx != nil {
		return s.memTx
	}
	return s.memory
}

// View runs fn against a read-only snapshot. It never writes.
func (s *Store) View(ctx context.Context, partition StatePartition, fn func(*domain.State) error) error {
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
	st, err := domain.CloneState(s.memory)
	if err != nil {
		return err
	}
	return fn(st)
}
