package storage

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
)

// The ORM borrows the existing pgx pool. OpenDBFromPool keeps its own idle
// count at zero, so native batch/CAS work and ORM work share one connection cap.
// Neither this constructor nor any model runs DDL or migrations.
func newORM(pool *pgxpool.Pool) *bun.DB {
	return bun.NewDB(stdlib.OpenDBFromPool(pool), pgdialect.New())
}

// ormTransaction combines typed ORM writes with the small PostgreSQL-specific
// statements still needed for revision allocation and
// one-shot login consumption. Both use the same database/sql transaction.
type ormTransaction struct{ orm bun.Tx }

func (s *Store) beginORMTransaction(ctx context.Context) (ormTransaction, error) {
	tx, err := s.orm.BeginTx(ctx, nil)
	return ormTransaction{orm: tx}, err
}
func (tx ormTransaction) Commit(context.Context) error   { return tx.orm.Commit() }
func (tx ormTransaction) Rollback(context.Context) error { return tx.orm.Rollback() }
func (tx ormTransaction) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	result, err := tx.orm.Tx.ExecContext(ctx, query, args...)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	operation := strings.ToUpper(strings.Fields(query)[0])
	if operation == "INSERT" {
		operation += " 0"
	}
	return pgconn.NewCommandTag(operation + " " + strconv.FormatInt(count, 10)), nil
}
func (tx ormTransaction) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	return ormRow{row: tx.orm.Tx.QueryRowContext(ctx, query, args...)}
}

type ormRow struct{ row *sql.Row }

func (row ormRow) Scan(dest ...any) error {
	err := row.row.Scan(dest...)
	if errors.Is(err, sql.ErrNoRows) {
		return pgx.ErrNoRows
	}
	return err
}

type ormMutation interface {
	Exec(context.Context, ...any) (sql.Result, error)
}

// Keep CAS observable: a missing row or lost revision is a conflict, including
// INSERT ... ON CONFLICT DO NOTHING. Never hide it behind an ORM Save/upsert.
func ormCAS(ctx context.Context, message string, query ormMutation) error {
	result, err := query.Exec(ctx)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return domain.Problem(409, "REVISION_CONFLICT", message)
		}
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return domain.Problem(409, "REVISION_CONFLICT", message)
	}
	return nil
}
