// Package ratelimit implements a simple in-memory sliding-window rate limiter
// keyed by an arbitrary string (typically an IP address).
package ratelimit

import (
	"sync"
	"time"
)

type Limiter struct {
	mu     sync.Mutex
	hits   map[string][]time.Time
	max    int
	window time.Duration
}

func New(maxPerWindow int, window time.Duration) *Limiter {
	return &Limiter{
		hits:   map[string][]time.Time{},
		max:    maxPerWindow,
		window: window,
	}
}

// Allow records a hit for the given key and returns (true, 0) if the key is
// under its quota, or (false, retryAfter) if it's over. retryAfter is the
// duration until the oldest hit in the window falls off.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-l.window)

	var kept []time.Time
	for _, t := range l.hits[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}

	if len(kept) >= l.max {
		l.hits[key] = kept
		return false, kept[0].Add(l.window).Sub(now)
	}

	kept = append(kept, now)
	l.hits[key] = kept
	return true, 0
}
