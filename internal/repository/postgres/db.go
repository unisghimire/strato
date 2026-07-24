// Package postgres implements the domain repository ports on PostgreSQL
// using pgx. No ORM: schemas of this shape reward explicit SQL — every query
// is visible, indexable, and EXPLAINable.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/unisghimire/strato/internal/config"
	"github.com/unisghimire/strato/internal/domain"
)

// DB wraps a pgx connection pool and implements domain.TxManager.
type DB struct {
	pool *pgxpool.Pool
}

// New builds a pooled connection from config and verifies connectivity.
func New(ctx context.Context, cfg config.Postgres) (*DB, error) {
	pc, err := pgxpool.ParseConfig(cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("parsing postgres config: %w", err)
	}
	pc.MaxConns = cfg.MaxConns
	pc.MinConns = cfg.MinConns
	pc.MaxConnLifetime = cfg.MaxConnLifetime

	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("creating postgres pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging postgres: %w", err)
	}
	return &DB{pool: pool}, nil
}

// Close releases the pool.
func (d *DB) Close() { d.pool.Close() }

// Ping verifies liveness (used by health checks).
func (d *DB) Ping(ctx context.Context) error { return d.pool.Ping(ctx) }

// queryer is the subset of pgx satisfied by both the pool and a transaction,
// letting every repository method run inside or outside a transaction
// transparently.
type queryer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type txKey struct{}

// q returns the active transaction from ctx if present, else the pool.
func (d *DB) q(ctx context.Context) queryer {
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return tx
	}
	return d.pool
}

// WithinTx implements domain.TxManager. The transaction rides the context;
// nested calls join the outer transaction rather than opening a second one.
func (d *DB) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return fn(ctx)
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after commit

	if err := fn(context.WithValue(ctx, txKey{}, tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing tx: %w", err)
	}
	return nil
}

// mapErr translates driver errors into domain errors so nothing above the
// repository layer ever inspects SQLSTATEs.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return fmt.Errorf("%w: %s", domain.ErrAlreadyExists, pgErr.ConstraintName)
		case "23503": // foreign_key_violation
			return fmt.Errorf("%w: referenced row missing (%s)", domain.ErrNotFound, pgErr.ConstraintName)
		}
	}
	return err
}
