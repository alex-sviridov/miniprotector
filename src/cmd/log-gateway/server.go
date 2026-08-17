// log-gateway is an mTLS-terminating HTTP reverse proxy in front of Loki.
// It authenticates every caller via a verified operating-tier mTLS
// certificate (common/mtls) and forwards the request body to Loki's push
// API completely unexamined and unmodified -- log-gateway never parses
// Loki's push format (JSON or, e.g. Vector's default, snappy-compressed
// protobuf), it only decides whether the caller is allowed to push at
// all. The hostname label a shipper puts on its own streams is trusted as
// sent: log-gateway's security boundary is "must present a valid,
// non-revoked operating certificate to push anything," not "must not
// mislabel its own logs" -- see docs/SECURITY.md.
package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/alex-sviridov/miniprotector/common/mtls"
	"github.com/gorilla/websocket"
)

// maxPushBodyBytes bounds how much of an inbound push body log-gateway will
// buffer in memory. Every request runs through io.ReadAll, so without a cap
// a single misbehaving (or compromised) mTLS-authenticated node could send
// an arbitrarily large body and OOM the gateway -- which, since log-gateway
// is the sole path to Loki for the whole fleet, would take down ingestion
// fleet-wide. 10MB is generous for a batched log push (Loki's own default
// push body limit, distributor.max-recv-msg-size-in-bytes, is in the same
// ballpark) while still bounding worst-case memory use per request.
const maxPushBodyBytes = 10 << 20 // 10MB

// maxQueryResponseBytes bounds how much of a query_range response
// log-gateway will buffer in memory -- the read-path mirror of
// maxPushBodyBytes: an unusually broad query returning a huge result must
// not OOM the sole path to Loki for the whole fleet.
const maxQueryResponseBytes = 10 << 20 // 10MB

// lokiForwardTimeout bounds how long log-gateway will wait on a single
// outbound push to Loki. Without a timeout, a degraded (not down) Loki --
// GC pause, disk pressure, backpressure -- leaves the outbound
// httpClient.Post call blocked indefinitely, and every inbound push
// goroutine piles up behind it until the gateway itself wedges or OOMs.
// 10s is generous for a single push under normal conditions but short
// enough to shed load and free the goroutine when Loki is unhealthy.
const lokiForwardTimeout = 10 * time.Second

// passthroughHeaders lists the request headers forwarded to Loki as-is.
// Loki's push body can be JSON or snappy-compressed protobuf (Vector's
// default) -- log-gateway doesn't decode either, so it must preserve
// whatever Content-Type/Content-Encoding the caller used or Loki will
// fail to decode a body it never actually altered.
var passthroughHeaders = []string{"Content-Type", "Content-Encoding"}

// tailUpgrader is shared across every caller's WS upgrade -- default
// buffer sizes are fine at this scale, mirroring the reasoning behind
// maxPushBodyBytes/maxQueryResponseBytes on the REST routes. CheckOrigin
// always returns true: the mTLS peer certificate check below is this
// route's real auth boundary, not browser origin -- log-gateway is never
// called directly from a browser (api-server proxies for browsers; see
// docs/SECURITY.md).
var tailUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// httpToWS rewrites an http(s):// base URL to its ws(s):// equivalent --
// Loki's tail endpoint is a WebSocket upgrade on the same host/port as its
// plain HTTP push/query_range endpoints.
func httpToWS(base string) string {
	switch {
	case strings.HasPrefix(base, "https://"):
		return "wss://" + strings.TrimPrefix(base, "https://")
	case strings.HasPrefix(base, "http://"):
		return "ws://" + strings.TrimPrefix(base, "http://")
	default:
		return base
	}
}

// logGatewayServer implements the sole HTTP handler an already-bootstrapped
// node's log shipper calls to push its logs toward Loki. Every caller must
// present a verified, non-revoked operating-tier mTLS certificate; nothing
// about the request body is required to identify itself.
type logGatewayServer struct {
	lokiPushURL  string
	lokiQueryURL string
	lokiTailURL  string
	httpClient   *http.Client
	logger       *slog.Logger
}

func newLogGatewayServer(lokiBaseURL string, logger *slog.Logger) *logGatewayServer {
	return &logGatewayServer{
		lokiPushURL:  lokiBaseURL + "/loki/api/v1/push",
		lokiQueryURL: lokiBaseURL + "/loki/api/v1/query_range",
		lokiTailURL:  httpToWS(lokiBaseURL) + "/loki/api/v1/tail",
		httpClient:   &http.Client{},
		logger:       logger,
	}
}

func (s *logGatewayServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	hostname, err := mtls.PeerHostnameFromConnState(r.TLS)
	if err != nil {
		http.Error(w, "determine caller identity: "+err.Error(), http.StatusUnauthorized)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxPushBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "read request body: "+err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "read request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), lokiForwardTimeout)
	defer cancel()

	lokiReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.lokiPushURL, bytes.NewReader(body))
	if err != nil {
		http.Error(w, "build loki request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	for _, h := range passthroughHeaders {
		if v := r.Header.Get(h); v != "" {
			lokiReq.Header.Set(h, v)
		}
	}

	resp, err := s.httpClient.Do(lokiReq)
	if err != nil {
		s.logger.Error("forward to loki failed", "hostname", hostname, "error", err)
		http.Error(w, "forward to loki: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// ServeQuery proxies a caller's query_range parameters to Loki's real
// query_range endpoint unmodified -- the read-path counterpart to
// ServeHTTP's push forwarding, gated by the same operating-tier mTLS
// check. Reachable by any operating-tier mesh node, not just api-server --
// the same "any operating-tier cert may call any RPC it can reach"
// convention already accepted for clientmanager-api/catalog/policy-server.
func (s *logGatewayServer) ServeQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if _, err := mtls.PeerHostnameFromConnState(r.TLS); err != nil {
		http.Error(w, "determine caller identity: "+err.Error(), http.StatusUnauthorized)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), lokiForwardTimeout)
	defer cancel()

	lokiReq, err := http.NewRequestWithContext(ctx, http.MethodGet, s.lokiQueryURL, nil)
	if err != nil {
		http.Error(w, "build loki request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	lokiReq.URL.RawQuery = r.URL.RawQuery

	resp, err := s.httpClient.Do(lokiReq)
	if err != nil {
		s.logger.Error("forward query to loki failed", "error", err)
		http.Error(w, "forward to loki: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxQueryResponseBytes+1))
	if err != nil {
		http.Error(w, "read loki response: "+err.Error(), http.StatusBadGateway)
		return
	}
	if len(body) > maxQueryResponseBytes {
		http.Error(w, "loki response exceeds size cap", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

// ServeTail proxies a caller's WebSocket tail connection to Loki's real
// tail endpoint -- the read-path live counterpart to ServeQuery's
// query_range proxying, gated by the same operating-tier mTLS check.
// Query parameters (query, start, delay_for, limit) are forwarded
// unmodified, same unexamined-passthrough philosophy as every other route
// here. Reachable by any operating-tier mesh node, same convention already
// accepted for the push/query routes.
func (s *logGatewayServer) ServeTail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if _, err := mtls.PeerHostnameFromConnState(r.TLS); err != nil {
		http.Error(w, "determine caller identity: "+err.Error(), http.StatusUnauthorized)
		return
	}

	lokiConn, _, err := websocket.DefaultDialer.DialContext(r.Context(), s.lokiTailURL+"?"+r.URL.RawQuery, nil)
	if err != nil {
		http.Error(w, "dial loki tail: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer lokiConn.Close()

	clientConn, err := tailUpgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error("serveTail: client upgrade failed", "error", err)
		return
	}
	defer clientConn.Close()

	relayTail(clientConn, lokiConn)
}

// relayTail pumps every message from loki to client until either side
// closes or errors. A second goroutine drains client's own incoming
// frames (a tail client sends nothing but control frames -- pings, a
// close) purely to detect a client-initiated disconnect promptly and tear
// down the upstream Loki connection with it, since ReadMessage is the only
// way gorilla/websocket surfaces a close.
func relayTail(client, loki *websocket.Conn) {
	clientClosed := make(chan struct{})
	go func() {
		defer close(clientClosed)
		for {
			if _, _, err := client.ReadMessage(); err != nil {
				return
			}
		}
	}()

	lokiDone := make(chan struct{})
	go func() {
		defer close(lokiDone)
		for {
			msgType, msg, err := loki.ReadMessage()
			if err != nil {
				return
			}
			if err := client.WriteMessage(msgType, msg); err != nil {
				return
			}
		}
	}()

	select {
	case <-clientClosed:
	case <-lokiDone:
	}
}
