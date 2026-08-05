package main

import (
	"net/http"
	"strconv"
	"strings"

	pb "github.com/alex-sviridov/miniprotector/api"
)

const (
	defaultCatalogLimit = 100
	maxCatalogLimit     = 500
)

type entryDTO struct {
	ID             int64  `json:"id"`
	SourceHost     string `json:"source_host"`
	StoreHost      string `json:"store_host"`
	JobID          string `json:"job_id"`
	ObjectID       string `json:"object_id"`
	Ctime          int64  `json:"ctime"`
	StoreCreatedAt int64  `json:"store_created_at"`
	ReceivedAt     int64  `json:"received_at"`
	Path           string `json:"path"`
	Size           int64  `json:"size"`
	Mode           string `json:"mode"`
	Owner          uint32 `json:"owner"`
	Group          uint32 `json:"group"`
	ModTime        int64  `json:"mod_time"`
}

func toEntryDTO(e *pb.Entry) entryDTO {
	return entryDTO{
		ID:             e.GetId(),
		SourceHost:     e.GetSourceHost(),
		StoreHost:      e.GetStoreHost(),
		JobID:          e.GetJobId(),
		ObjectID:       e.GetObjectId(),
		Ctime:          e.GetCtime(),
		StoreCreatedAt: e.GetStoreCreatedAt(),
		ReceivedAt:     e.GetReceivedAt(),
		Path:           e.GetPath(),
		Size:           e.GetSize(),
		Mode:           e.GetMode(),
		Owner:          e.GetOwner(),
		Group:          e.GetGroup(),
		ModTime:        e.GetModTime(),
	}
}

func (s *server) handleListCatalog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	limit := defaultCatalogLimit
	if raw := q.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxCatalogLimit {
			writeJSONError(w, http.StatusBadRequest, "limit must be an integer between 1 and 500")
			return
		}
		limit = parsed
	}

	var startingAfter int64
	if raw := q.Get("starting_after"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			writeJSONError(w, http.StatusBadRequest, "starting_after must be a non-negative integer")
			return
		}
		startingAfter = parsed
	}

	receivedAfter, ok := parseUnixParam(q.Get("received_after"))
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "received_after must be a non-negative integer")
		return
	}
	receivedBefore, ok := parseUnixParam(q.Get("received_before"))
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "received_before must be a non-negative integer")
		return
	}

	resp, err := s.catalog.ListEntries(r.Context(), &pb.ListEntriesRequest{
		SourceHost:     q.Get("source_host"),
		StoreHost:      q.Get("store_host"),
		Pattern:        q.Get("pattern"),
		Limit:          int32(limit),
		StartingAfter:  startingAfter,
		ReceivedAfter:  receivedAfter,
		ReceivedBefore: receivedBefore,
		SourceHosts:    splitCommaParam(q.Get("source_hosts")),
		JobNames:       splitCommaParam(q.Get("job_names")),
	})
	if err != nil {
		s.logger.Error("handleListCatalog: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}

	entries := make([]entryDTO, len(resp.GetEntries()))
	for i, e := range resp.GetEntries() {
		entries[i] = toEntryDTO(e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": entries, "has_more": resp.GetHasMore()})
}

// parseUnixParam parses an optional unix-seconds query param. An empty
// string is "unset" (returns 0, true); anything else must be a
// non-negative integer.
func parseUnixParam(raw string) (int64, bool) {
	if raw == "" {
		return 0, true
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || parsed < 0 {
		return 0, false
	}
	return parsed, true
}

// splitCommaParam splits a comma-separated query param into a slice,
// trimming surrounding whitespace from each segment and dropping empty
// segments; an empty input yields nil (no filter).
func splitCommaParam(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

type facetDTO struct {
	Name     string `json:"name"`
	Count    int64  `json:"count"`
	LastSeen int64  `json:"last_seen"`
}

func toFacetDTO(f *pb.Facet) facetDTO {
	return facetDTO{Name: f.GetName(), Count: f.GetCount(), LastSeen: f.GetLastSeen()}
}

func (s *server) handleListCatalogClients(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	receivedAfter, ok := parseUnixParam(q.Get("received_after"))
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "received_after must be a non-negative integer")
		return
	}
	receivedBefore, ok := parseUnixParam(q.Get("received_before"))
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "received_before must be a non-negative integer")
		return
	}

	resp, err := s.catalog.ListClientFacets(r.Context(), &pb.ListFacetsRequest{
		ReceivedAfter:  receivedAfter,
		ReceivedBefore: receivedBefore,
		Pattern:        q.Get("pattern"),
		JobNames:       splitCommaParam(q.Get("job_names")),
	})
	if err != nil {
		s.logger.Error("handleListCatalogClients: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}

	facets := make([]facetDTO, len(resp.GetFacets()))
	for i, f := range resp.GetFacets() {
		facets[i] = toFacetDTO(f)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": facets})
}

func (s *server) handleListCatalogJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	receivedAfter, ok := parseUnixParam(q.Get("received_after"))
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "received_after must be a non-negative integer")
		return
	}
	receivedBefore, ok := parseUnixParam(q.Get("received_before"))
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "received_before must be a non-negative integer")
		return
	}

	resp, err := s.catalog.ListJobFacets(r.Context(), &pb.ListFacetsRequest{
		ReceivedAfter:  receivedAfter,
		ReceivedBefore: receivedBefore,
		Pattern:        q.Get("pattern"),
		SourceHosts:    splitCommaParam(q.Get("source_hosts")),
	})
	if err != nil {
		s.logger.Error("handleListCatalogJobs: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}

	facets := make([]facetDTO, len(resp.GetFacets()))
	for i, f := range resp.GetFacets() {
		facets[i] = toFacetDTO(f)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": facets})
}
