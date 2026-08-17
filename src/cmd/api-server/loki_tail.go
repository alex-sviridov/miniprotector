// src/cmd/api-server/loki_tail.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
)

// lokiTailMessage mirrors one batch of Loki's WS tail wire shape (see
// docs/protocols/log-gateway.md's new tail-proxy section) -- Loki batches
// however its own querier chooses to flush, so one message can carry
// several streams' worth of new lines.
type lokiTailMessage struct {
	Streams []lokiStream `json:"streams"`
}

// lokiTailer is the subset of tail behavior handleJobLogsStream (Task 6)
// and jobAggregator (Task 9) need -- satisfied by httpLokiTailer, and by a
// fake in tests.
type lokiTailer interface {
	// Tail opens a Loki tail connection for query starting at start and
	// calls onMessage for every batch received, until ctx is cancelled
	// (returns nil) or the connection errors or onMessage itself returns a
	// non-nil error (returns that error either way).
	Tail(ctx context.Context, query string, start time.Time, onMessage func(lokiTailMessage) error) error
}

// httpLokiTailer calls log-gateway's WS tail-proxy route (Task 2) rather
// than dialing Loki directly -- Loki is never directly reachable from any
// agent-managed node, api-server included (see docs/SECURITY.md).
type httpLokiTailer struct {
	baseURL string // log-gateway's ws(s):// base URL, e.g. "wss://log-gateway:9400"
	dialer  *websocket.Dialer
}

func newHTTPLokiTailer(baseURL string, dialer *websocket.Dialer) *httpLokiTailer {
	return &httpLokiTailer{baseURL: baseURL, dialer: dialer}
}

func (t *httpLokiTailer) Tail(ctx context.Context, query string, start time.Time, onMessage func(lokiTailMessage) error) error {
	u := t.baseURL + "/loki/api/v1/tail?query=" + url.QueryEscape(query) + "&start=" + strconv.FormatInt(start.UnixNano(), 10)

	conn, _, err := t.dialer.DialContext(ctx, u, nil)
	if err != nil {
		return fmt.Errorf("dial log-gateway tail: %w", err)
	}
	defer conn.Close()

	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read tail message: %w", err)
		}
		var msg lokiTailMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue // a malformed frame is dropped, not fatal to the tail
		}
		if err := onMessage(msg); err != nil {
			return err
		}
	}
}
