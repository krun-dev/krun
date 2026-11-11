package storage

import (
	"context"
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
	pool                *pgxpool.Pool
	orm                 *bun.DB
	repositoryTokenKeys RepositoryTokenKeys

	configMu sync.Mutex
	configCh chan struct{}
}

// StatePartition selects which tables Update loads and persists. Keeping the
// halves apart means an IAM mutation never shares a transaction with Workers
// rows, which AGENTS.md section 9 requires.
type StatePartition int

const (
	// PartitionIAM covers identity, organizations, permissions and keys.
	PartitionIAM StatePartition = 1 << iota
	// PartitionWorkers covers workers, builds, versions and operations.
	PartitionWorkers
	// PartitionAll loads both halves. Read paths may use it; a mutation must
	// not, because that would put IAM and Workers in one transaction.
	PartitionAll = PartitionIAM | PartitionWorkers
)

func Open(ctx context.Context, dsn string, keys RepositoryTokenKeys) (*Store, error) {
	pool, err := openPostgresPool(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err = initializeStoreSchema(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool, orm: newORM(pool), repositoryTokenKeys: keys}, nil
}

func openPostgresPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
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
	return pgxpool.NewWithConfig(ctx, config)
}

func (s *Store) Close() {
	if s.orm != nil {
		_ = s.orm.Close()
	}
	s.pool.Close()
}

// Ping verifies that the store can serve a query without loading any control
// plane tables.
func (s *Store) Ping(ctx context.Context) error {
	var one int
	return s.pool.QueryRow(ctx, `SELECT 1`).Scan(&one)
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

// View runs fn against a read-only snapshot. It never writes.
func (s *Store) View(ctx context.Context, partition StatePartition, fn func(*domain.State) error) error {
	st, err := s.load(ctx, partition)
	if err != nil {
		return err
	}
	return fn(st)
}
