// log-gateway is an mTLS-terminating HTTP reverse proxy in front of Loki.
// Loki's push API has no concept of mTLS peer identity, and this project
// never trusts a caller-asserted identity field (see docs/SECURITY.md) --
// so every proxied push has its hostname label force-overwritten from the
// verified peer certificate before being forwarded.
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/alex-sviridov/miniprotector/common/mtls"
)

// pushRequest and stream mirror the subset of Loki's push API JSON body
// (POST /loki/api/v1/push) log-gateway needs to touch: the per-stream
// label set. Values (timestamp/line pairs) pass through completely
// unexamined and unmodified.
type pushRequest struct {
	Streams []stream `json:"streams"`
}

type stream struct {
	Stream map[string]string `json:"stream"`
	Values [][2]string       `json:"values"`
}

// logGatewayServer implements the sole HTTP handler an already-bootstrapped
// node's log shipper calls to push its logs toward Loki. The caller's
// identity is always the verified mTLS peer hostname -- never a field in
// the request body.
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

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	var req pushRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "parse push request: "+err.Error(), http.StatusBadRequest)
		return
	}

	for i := range req.Streams {
		if req.Streams[i].Stream == nil {
			req.Streams[i].Stream = make(map[string]string)
		}
		req.Streams[i].Stream["hostname"] = hostname
	}

	rewritten, err := json.Marshal(req)
	if err != nil {
		http.Error(w, "re-marshal push request: "+err.Error(), http.StatusInternalServerError)
		return
	}

	resp, err := s.httpClient.Post(s.lokiPushURL, "application/json", bytes.NewReader(rewritten))
	if err != nil {
		s.logger.Error("forward to loki failed", "hostname", hostname, "error", err)
		http.Error(w, "forward to loki: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
