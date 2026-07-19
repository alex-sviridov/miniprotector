// src/cmd/api-server/loki_cache.go
package main

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// cachingLokiClient wraps a lokiQuerier with a short in-memory TTL cache --
// a UI polling GET /api/v1/jobs or GET /api/v1/jobs/{job_id}/logs every few
// seconds would otherwise re-run a near-identical Loki query on every poll.
//
// start/end are rounded (Truncate) to a ttl-sized bucket for cache-key
// purposes only, not for the actual query sent to inner: callers typically
// derive start/end from time.Now(), which differs by milliseconds between
// otherwise-identical requests -- keying on exact timestamps would mean the
// cache never hits at all. The bucketed key still lets rapid repeated polls
// within the same window share a cache entry.
type cachingLokiClient struct {
	inner lokiQuerier
	ttl   time.Duration

	mu    sync.Mutex
	cache map[string]cachedLokiResult
}

type cachedLokiResult struct {
	streams []lokiStream
	err     error
	at      time.Time
}

func newCachingLokiClient(inner lokiQuerier, ttl time.Duration) *cachingLokiClient {
	return &cachingLokiClient{inner: inner, ttl: ttl, cache: make(map[string]cachedLokiResult)}
}

func (c *cachingLokiClient) QueryRange(ctx context.Context, query string, start, end time.Time, limit int) ([]lokiStream, error) {
	key := fmt.Sprintf("%s|%d|%d|%d", query, start.Truncate(c.ttl).UnixNano(), end.Truncate(c.ttl).UnixNano(), limit)

	c.mu.Lock()
	if cached, ok := c.cache[key]; ok && time.Since(cached.at) < c.ttl {
		c.mu.Unlock()
		return cached.streams, cached.err
	}
	c.mu.Unlock()

	streams, err := c.inner.QueryRange(ctx, query, start, end, limit)

	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range c.cache {
		if time.Since(v.at) >= c.ttl {
			delete(c.cache, k) // opportunistic sweep -- bounds the map without a background goroutine
		}
	}
	c.cache[key] = cachedLokiResult{streams: streams, err: err, at: time.Now()}

	return streams, err
}
