package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type countingLokiClient struct {
	calls   atomic.Int32
	streams []lokiStream
}

func (c *countingLokiClient) QueryRange(ctx context.Context, query string, start, end time.Time, limit int) ([]lokiStream, error) {
	c.calls.Add(1)
	return c.streams, nil
}

func TestCachingLokiClient_ReturnsCachedResultWithinTTL(t *testing.T) {
	inner := &countingLokiClient{streams: []lokiStream{{Stream: map[string]string{"hostname": "a"}}}}
	cache := newCachingLokiClient(inner, time.Minute)

	now := time.Now()
	_, err := cache.QueryRange(context.Background(), `{}`, now.Add(-time.Hour), now, 100)
	require.NoError(t, err)
	_, err = cache.QueryRange(context.Background(), `{}`, now.Add(-time.Hour), now, 100)
	require.NoError(t, err)

	assert.Equal(t, int32(1), inner.calls.Load(), "second call within TTL and the same time bucket must be served from cache")
}

func TestCachingLokiClient_RequeriesAfterTTLExpires(t *testing.T) {
	inner := &countingLokiClient{}
	cache := newCachingLokiClient(inner, 10*time.Millisecond)

	now := time.Now()
	_, err := cache.QueryRange(context.Background(), `{}`, now.Add(-time.Hour), now, 100)
	require.NoError(t, err)

	time.Sleep(20 * time.Millisecond)

	_, err = cache.QueryRange(context.Background(), `{}`, now.Add(-time.Hour), now, 100)
	require.NoError(t, err)

	assert.Equal(t, int32(2), inner.calls.Load())
}

func TestCachingLokiClient_DifferentQueriesNotConflated(t *testing.T) {
	inner := &countingLokiClient{}
	cache := newCachingLokiClient(inner, time.Minute)

	now := time.Now()
	_, err := cache.QueryRange(context.Background(), `{binary="agent"}`, now.Add(-time.Hour), now, 100)
	require.NoError(t, err)
	_, err = cache.QueryRange(context.Background(), `{binary="brfs"}`, now.Add(-time.Hour), now, 100)
	require.NoError(t, err)

	assert.Equal(t, int32(2), inner.calls.Load())
}
