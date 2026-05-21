package store

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

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

// Metrics

type PrefStat struct {
	Label   string
	Count   int
	Percent int
}

type ShareSummary struct {
	ID         string
	DistanceKm string
	Surface    string
	Hills      string
	CreatedAt  time.Time
}

type Metrics struct {
	Total       int
	Today       int
	ThisWeek    int
	AvgDistance float64
	BySurface   []PrefStat
	ByHills     []PrefStat
	Recent      []ShareSummary
}

func (s *Store) Metrics(ctx context.Context) (Metrics, error) {
	var m Metrics

	// Counts
	s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM shares`).Scan(&m.Total)
	s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM shares WHERE created_at >= NOW() - INTERVAL '24 hours'`).Scan(&m.Today)
	s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM shares WHERE created_at >= NOW() - INTERVAL '7 days'`).Scan(&m.ThisWeek)

	// Average distance
	s.pool.QueryRow(ctx, `
		SELECT COALESCE(AVG(((meta::jsonb)->>'distance')::int), 0)
		FROM shares WHERE (meta::jsonb)->>'distance' IS NOT NULL
	`).Scan(&m.AvgDistance)

	// By surface
	m.BySurface = queryPrefStats(ctx, s.pool, "surface", map[string]string{
		"road":  "Roads",
		"trail": "Trails",
	})

	// By hills
	m.ByHills = queryPrefStats(ctx, s.pool, "hills", map[string]string{
		"any":  "Any",
		"flat": "Prefer flat",
	})

	// Recent shares
	rows, err := s.pool.Query(ctx, `
		SELECT
			id,
			COALESCE((meta::jsonb)->>'distance', '0') as distance,
			COALESCE((meta::jsonb)->>'surface', '') as surface,
			COALESCE((meta::jsonb)->>'hills', '') as hills,
			created_at
		FROM shares
		ORDER BY created_at DESC
		LIMIT 20
	`)
	if err != nil {
		return m, err
	}
	defer rows.Close()

	for rows.Next() {
		var ss ShareSummary
		var distanceM int
		var surface, hills string
		if err := rows.Scan(&ss.ID, &distanceM, &surface, &hills, &ss.CreatedAt); err != nil {
			continue
		}
		ss.DistanceKm = fmt.Sprintf("%.1f km", float64(distanceM)/1000)
		ss.Surface = map[string]string{"road": "Roads", "trail": "Trails"}[surface]
		ss.Hills = map[string]string{"any": "Any", "flat": "Prefer flat"}[hills]
		if ss.Surface == "" {
			ss.Surface = surface
		}
		if ss.Hills == "" {
			ss.Hills = hills
		}
		m.Recent = append(m.Recent, ss)
	}

	return m, nil
}

func queryPrefStats(ctx context.Context, pool *pgxpool.Pool, field string, labels map[string]string) []PrefStat {
	rows, err := pool.Query(ctx, fmt.Sprintf(`
		SELECT COALESCE((meta::jsonb)->>'%s', 'unknown') as val, COUNT(*) as count
		FROM shares GROUP BY (meta::jsonb)->>'%s'
	`, field, field))
	if err != nil {
		return nil
	}
	defer rows.Close()

	var stats []PrefStat
	total := 0
	for rows.Next() {
		var val string
		var count int
		if rows.Scan(&val, &count) == nil {
			label := labels[val]
			if label == "" {
				label = val
			}
			stats = append(stats, PrefStat{Label: label, Count: count})
			total += count
		}
	}
	for i := range stats {
		if total > 0 {
			stats[i].Percent = stats[i].Count * 100 / total
		}
	}
	return stats
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
