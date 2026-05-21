package store

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("share not found")

type Store struct {
	pool *pgxpool.Pool
}

func New(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping: %w", err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS shares (
			id         TEXT PRIMARY KEY,
			route      TEXT NOT NULL,
			meta       TEXT NOT NULL,
			created_at TIMESTAMPTZ DEFAULT NOW()
		)
	`); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Save(ctx context.Context, route, meta string) (string, error) {
	id := newID()
	_, err := s.pool.Exec(ctx,
		"INSERT INTO shares (id, route, meta) VALUES ($1, $2, $3)",
		id, route, meta,
	)
	return id, err
}

func (s *Store) Get(ctx context.Context, id string) (route, meta string, err error) {
	row := s.pool.QueryRow(ctx,
		"SELECT route, meta FROM shares WHERE id = $1", id,
	)
	if err = row.Scan(&route, &meta); errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	return
}

func newID() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	rand.Read(b)
	for i, v := range b {
		b[i] = alphabet[int(v)%len(alphabet)]
	}
	return string(b)
}
