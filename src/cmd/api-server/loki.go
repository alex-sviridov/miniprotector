// src/cmd/api-server/loki.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// lokiQuerier is the subset of a Loki-query-capable client GET /api/v1/jobs
// and GET /api/v1/jobs/{job_id}/logs need -- satisfied by httpLokiClient
// (Task 9), cachingLokiClient (Task 10), and a fake in tests.
type lokiQuerier interface {
	QueryRange(ctx context.Context, query string, start, end time.Time, limit int) ([]lokiStream, error)
}

// lokiStream is one label-set's worth of matching log lines, as returned by
// Loki's query_range API.
type lokiStream struct {
	Stream map[string]string `json:"stream"`
	Values []lokiValue       `json:"values"`
}

// lokiValue is one matched log line: Loki always returns [timestamp, line],
// and -- when the queried stream carries Loki structured metadata (see
// cmd/agent/vector.go's sink config) -- a third element holding it, which
// custom UnmarshalJSON below decodes into Metadata.
type lokiValue struct {
	Timestamp int64 // unix nanoseconds, Loki's own wire unit
	Line      string
	Metadata  map[string]string
}

func (v *lokiValue) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if len(raw) < 2 {
		return fmt.Errorf("loki value entry has fewer than 2 elements")
	}

	var tsStr string
	if err := json.Unmarshal(raw[0], &tsStr); err != nil {
		return fmt.Errorf("parse loki value timestamp: %w", err)
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return fmt.Errorf("parse loki value timestamp: %w", err)
	}
	v.Timestamp = ts

	if err := json.Unmarshal(raw[1], &v.Line); err != nil {
		return fmt.Errorf("parse loki value line: %w", err)
	}

	if len(raw) >= 3 {
		v.Metadata = map[string]string{}
		if err := json.Unmarshal(raw[2], &v.Metadata); err != nil {
			return fmt.Errorf("parse loki value structured metadata: %w", err)
		}
	}
	return nil
}

type lokiQueryResponse struct {
	Status string `json:"status"`
	Data   struct {
		Result []lokiStream `json:"result"`
	} `json:"data"`
}

// httpLokiClient calls Loki's query_range API through log-gateway's
// read-proxy route (Task 6) rather than dialing Loki directly -- Loki is
// never directly reachable from any agent-managed node, api-server
// included (see docs/SECURITY.md).
type httpLokiClient struct {
	baseURL    string // log-gateway's base URL, e.g. "https://log-gateway:9400"
	httpClient *http.Client
}

func newHTTPLokiClient(baseURL string, httpClient *http.Client) *httpLokiClient {
	return &httpLokiClient{baseURL: baseURL, httpClient: httpClient}
}

func (c *httpLokiClient) QueryRange(ctx context.Context, query string, start, end time.Time, limit int) ([]lokiStream, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/loki/api/v1/query_range", nil)
	if err != nil {
		return nil, fmt.Errorf("build loki query request: %w", err)
	}
	q := req.URL.Query()
	q.Set("query", query)
	q.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	q.Set("end", strconv.FormatInt(end.UnixNano(), 10))
	q.Set("limit", strconv.Itoa(limit))
	req.URL.RawQuery = q.Encode()

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query loki: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read loki response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("loki query returned %d: %s", resp.StatusCode, body)
	}

	var parsed lokiQueryResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode loki response: %w", err)
	}
	return parsed.Data.Result, nil
}
