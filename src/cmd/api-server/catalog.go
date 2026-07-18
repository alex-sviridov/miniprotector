package main

import (
	"net/http"
	"strconv"

	pb "github.com/alex-sviridov/miniprotector/api"
)

const (
	defaultCatalogLimit = 100
	maxCatalogLimit     = 500
)

type entryDTO struct {
	ID              int64  `json:"id"`
	SourceHost      string `json:"source_host"`
	JobID           string `json:"job_id"`
	ObjectID        string `json:"object_id"`
	Ctime           int64  `json:"ctime"`
	SourceCreatedAt int64  `json:"source_created_at"`
	ReceivedAt      int64  `json:"received_at"`
	Path            string `json:"path"`
	Size            int64  `json:"size"`
	Mode            string `json:"mode"`
	Owner           uint32 `json:"owner"`
	Group           uint32 `json:"group"`
	ModTime         int64  `json:"mod_time"`
}

func toEntryDTO(e *pb.Entry) entryDTO {
	return entryDTO{
		ID:              e.GetId(),
		SourceHost:      e.GetSourceHost(),
		JobID:           e.GetJobId(),
		ObjectID:        e.GetObjectId(),
		Ctime:           e.GetCtime(),
		SourceCreatedAt: e.GetSourceCreatedAt(),
		ReceivedAt:      e.GetReceivedAt(),
		Path:            e.GetPath(),
		Size:            e.GetSize(),
		Mode:            e.GetMode(),
		Owner:           e.GetOwner(),
		Group:           e.GetGroup(),
		ModTime:         e.GetModTime(),
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

	resp, err := s.catalog.ListEntries(r.Context(), &pb.ListEntriesRequest{
		SourceHost:    q.Get("source_host"),
		Pattern:       q.Get("pattern"),
		Limit:         int32(limit),
		StartingAfter: startingAfter,
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
