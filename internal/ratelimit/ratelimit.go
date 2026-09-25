package ratelimit

import (
	"sync"
	"time"
)

// siteEventWindowSeconds / siteEventWindowLimit bound how many analytics
// events a single tracked site may submit per window — the ingestion
// endpoint (POST /api/v1/track) is the only route in this app reachable
// directly from arbitrary third-party browsers, so it's the only one that
// needs this; every other route is either session-authed or a personal
// API key tied to one account.
const (
	siteEventWindowSeconds = 60
	siteEventWindowLimit   = 600
)

type siteWindowCounter struct {
	windowStart time.Time
	count       int
}

// siteRateLimiter is a simple fixed-window counter keyed by tracked site
// ID (not by visitor IP, so one popular site's many visitors don't
// cannibalize each other's quota — the limit exists to bound abuse of a
// single leaked key, not to throttle legitimate traffic volume).
type siteRateLimiter struct {
	mu       sync.Mutex
	counters map[int64]*siteWindowCounter
}

var TrackRateLimiter = &siteRateLimiter{counters: make(map[int64]*siteWindowCounter)}

// Allow reports whether siteID may submit one more event right now,
// advancing to a fresh window once the current one has elapsed.
func (l *siteRateLimiter) Allow(siteID int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	c := l.counters[siteID]
	if c == nil || now.Sub(c.windowStart) >= siteEventWindowSeconds*time.Second {
		c = &siteWindowCounter{windowStart: now, count: 0}
		l.counters[siteID] = c
	}
	if c.count >= siteEventWindowLimit {
		return false
	}
	c.count++
	return true
}
