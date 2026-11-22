package db

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/config"
	"net/url"
)

type Query interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func Open(ctx context.Context) (*pgxpool.Pool, error) {
	password, err := config.Secret("postgres_password")
	if err != nil {
		return nil, err
	}
	uri := url.URL{Scheme: "postgres", User: url.UserPassword("flrp", password), Host: config.Env("FL_PG_HOST", "postgres:5432"), Path: config.Env("FL_PG_DATABASE", "flrp"), RawQuery: "sslmode=disable"}
	cfg, err := pgxpool.ParseConfig(uri.String())
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err = pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}
func Transaction(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
