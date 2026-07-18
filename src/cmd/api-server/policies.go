package main

import (
	"fmt"
	"net/http"

	pb "github.com/alex-sviridov/miniprotector/api"
)

type clientFiltersDTO struct {
	Hostnames []string          `json:"hostnames"`
	Labels    map[string]string `json:"labels"`
}

type objectFilterDTO struct {
	ID      string   `json:"id"`
	Path    string   `json:"path"`
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

type policyDTO struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	CreatedAt     int64             `json:"created_at"`
	UpdatedAt     int64             `json:"updated_at"`
	ClientFilters clientFiltersDTO  `json:"client_filters"`
	ObjectFilters []objectFilterDTO `json:"object_filters"`
	RPO           string            `json:"rpo"`
	BackupWindow  []string          `json:"backup_window"`
	Destination   string            `json:"destination"`
}

func toPolicyDTO(p *pb.Policy) policyDTO {
	objectFilters := make([]objectFilterDTO, len(p.GetObjectFilters()))
	for i, f := range p.GetObjectFilters() {
		objectFilters[i] = objectFilterDTO{ID: f.GetId(), Path: f.GetPath(), Include: f.GetInclude(), Exclude: f.GetExclude()}
	}
	return policyDTO{
		ID:        p.GetId(),
		Name:      p.GetName(),
		CreatedAt: p.GetCreatedAt().AsTime().Unix(),
		UpdatedAt: p.GetUpdatedAt().AsTime().Unix(),
		ClientFilters: clientFiltersDTO{
			Hostnames: p.GetClientFilters().GetHostnames(),
			Labels:    p.GetClientFilters().GetLabels(),
		},
		ObjectFilters: objectFilters,
		RPO:           p.GetRpo(),
		BackupWindow:  p.GetBackupWindow(),
		Destination:   p.GetDestination(),
	}
}

func (s *server) handleListPolicies(w http.ResponseWriter, r *http.Request) {
	resp, err := s.policy.ListPolicies(r.Context(), &pb.ListPoliciesRequest{})
	if err != nil {
		s.logger.Error("handleListPolicies: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	policies := make([]policyDTO, len(resp.GetPolicies()))
	for i, p := range resp.GetPolicies() {
		policies[i] = toPolicyDTO(p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": policies})
}

func (s *server) handleGetPolicy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	resp, err := s.policy.ListPolicies(r.Context(), &pb.ListPoliciesRequest{})
	if err != nil {
		s.logger.Error("handleGetPolicy: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	for _, p := range resp.GetPolicies() {
		if p.GetId() == id {
			writeJSON(w, http.StatusOK, toPolicyDTO(p))
			return
		}
	}
	writeJSONError(w, http.StatusNotFound, fmt.Sprintf("policy %q not found", id))
}
