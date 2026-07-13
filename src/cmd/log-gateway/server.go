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
	"time"

	"github.com/alex-sviridov/miniprotector/common/mtls"
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

// logGatewayServer implements the sole HTTP handler an already-bootstrapped
// node's log shipper calls to push its logs toward Loki. Every caller must
// present a verified, non-revoked operating-tier mTLS certificate; nothing
// about the request body is required to identify itself.
type logGatewayServer struct {
	lokiPushURL string
	httpClient  *http.Client
	logger      *slog.Logger
}

func newLogGatewayServer(lokiBaseURL string, logger *slog.Logger) *logGatewayServer {
	return &logGatewayServer{
		lokiPushURL: lokiBaseURL + "/loki/api/v1/push",
		httpClient:  &http.Client{},
		logger:      logger,
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
