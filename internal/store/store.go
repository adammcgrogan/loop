package store

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
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
		);
		CREATE TABLE IF NOT EXISTS events (
			id         BIGSERIAL PRIMARY KEY,
			type       TEXT NOT NULL,
			ip         TEXT,
			user_agent TEXT,
			referrer   TEXT,
			created_at TIMESTAMPTZ DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS events_type_created ON events (type, created_at);
	`); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Event types
const (
	EventPageView        = "page_view"
	EventRouteGenerated  = "route_generated"
	EventShareCreated    = "share_created"
	EventShareViewed     = "share_viewed"
)

// LogEvent records an analytics event. Errors are silently dropped so a
// logging failure never affects the user-facing request.
func (s *Store) LogEvent(ctx context.Context, eventType, ip, userAgent, referrer string) {
	s.pool.Exec(ctx,
		"INSERT INTO events (type, ip, user_agent, referrer) VALUES ($1, $2, $3, $4)",
		eventType, ip, userAgent, referrer,
	)
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

type ReferrerStat struct {
	Host  string
	Count int
}

type ShareSummary struct {
	ID         string
	DistanceKm string
	Surface    string
	Hills      string
	CreatedAt  time.Time
}

type Metrics struct {
	// Visits
	TotalVisits    int
	UniqueVisitors int
	VisitsToday    int
	VisitsWeek     int

	// Routes generated
	RoutesTotal int
	RoutesToday int
	RoutesWeek  int

	// Shares
	SharesTotal int
	SharesToday int
	SharesWeek  int

	// Breakdown
	AvgDistanceKm float64
	BySurface     []PrefStat
	ByHills       []PrefStat
	MobilePct     int
	DesktopPct    int
	TopReferrers  []ReferrerStat

	Recent []ShareSummary
}

func (s *Store) Metrics(ctx context.Context) (Metrics, error) {
	var m Metrics

	// Visit counts
	s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM events WHERE type = 'page_view'`).Scan(&m.TotalVisits)
	s.pool.QueryRow(ctx, `SELECT COUNT(DISTINCT ip) FROM events WHERE type = 'page_view'`).Scan(&m.UniqueVisitors)
	s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM events WHERE type = 'page_view' AND created_at >= NOW() - INTERVAL '24 hours'`).Scan(&m.VisitsToday)
	s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM events WHERE type = 'page_view' AND created_at >= NOW() - INTERVAL '7 days'`).Scan(&m.VisitsWeek)

	// Route generation counts
	s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM events WHERE type = 'route_generated'`).Scan(&m.RoutesTotal)
	s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM events WHERE type = 'route_generated' AND created_at >= NOW() - INTERVAL '24 hours'`).Scan(&m.RoutesToday)
	s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM events WHERE type = 'route_generated' AND created_at >= NOW() - INTERVAL '7 days'`).Scan(&m.RoutesWeek)

	// Share counts
	s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM shares`).Scan(&m.SharesTotal)
	s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM shares WHERE created_at >= NOW() - INTERVAL '24 hours'`).Scan(&m.SharesToday)
	s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM shares WHERE created_at >= NOW() - INTERVAL '7 days'`).Scan(&m.SharesWeek)

	// Average distance from shares
	var avgDistM float64
	s.pool.QueryRow(ctx, `
		SELECT COALESCE(AVG(((meta::jsonb)->>'distance')::int), 0)
		FROM shares WHERE (meta::jsonb)->>'distance' IS NOT NULL
	`).Scan(&avgDistM)
	m.AvgDistanceKm = avgDistM / 1000

	// Device breakdown
	var mobile, desktop int
	s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM events WHERE type = 'page_view' AND LOWER(user_agent) LIKE '%mobile%'`).Scan(&mobile)
	s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM events WHERE type = 'page_view' AND LOWER(user_agent) NOT LIKE '%mobile%'`).Scan(&desktop)
	total := mobile + desktop
	if total > 0 {
		m.MobilePct = mobile * 100 / total
		m.DesktopPct = desktop * 100 / total
	}

	// Top referrers
	rows, err := s.pool.Query(ctx, `
		SELECT referrer, COUNT(*) as count
		FROM events
		WHERE type = 'page_view' AND referrer IS NOT NULL AND referrer != ''
		GROUP BY referrer
		ORDER BY count DESC
		LIMIT 8
	`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var ref string
			var count int
			if rows.Scan(&ref, &count) == nil {
				m.TopReferrers = append(m.TopReferrers, ReferrerStat{
					Host:  referrerHost(ref),
					Count: count,
				})
			}
		}
	}

	// Preference breakdowns from shares
	m.BySurface = queryPrefStats(ctx, s.pool, "surface", map[string]string{
		"road":  "Roads",
		"trail": "Trails",
	})
	m.ByHills = queryPrefStats(ctx, s.pool, "hills", map[string]string{
		"any":  "Any",
		"flat": "Prefer flat",
	})

	// Recent shares
	shareRows, err := s.pool.Query(ctx, `
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
	defer shareRows.Close()

	surfaceLabel := map[string]string{"road": "Roads", "trail": "Trails"}
	hillsLabel := map[string]string{"any": "Any", "flat": "Prefer flat"}

	for shareRows.Next() {
		var ss ShareSummary
		var distanceM int
		var surface, hills string
		if err := shareRows.Scan(&ss.ID, &distanceM, &surface, &hills, &ss.CreatedAt); err != nil {
			continue
		}
		ss.DistanceKm = fmt.Sprintf("%.1f km", float64(distanceM)/1000)
		ss.Surface = surfaceLabel[surface]
		ss.Hills = hillsLabel[hills]
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

func referrerHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return u.Host
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
