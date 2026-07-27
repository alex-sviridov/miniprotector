package main

import (
	"encoding/json"
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
	Type          string            `json:"type"`
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
		Type:          p.GetType(),
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

type objectFilterInput struct {
	Path    string   `json:"path"`
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

type policyInput struct {
	Name          string              `json:"name"`
	ClientFilters clientFiltersDTO    `json:"client_filters"`
	ObjectFilters []objectFilterInput `json:"object_filters"`
	RPO           string              `json:"rpo"`
	BackupWindow  []string            `json:"backup_window"`
	Destination   string              `json:"destination"`
}

func decodePolicyInput(r *http.Request) (policyInput, error) {
	var in policyInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return policyInput{}, err
	}
	return in, nil
}

func toProtoClientFiltersInput(cf clientFiltersDTO) *pb.ClientFilters {
	return &pb.ClientFilters{Hostnames: cf.Hostnames, Labels: cf.Labels}
}

func toProtoObjectFiltersInput(filters []objectFilterInput) []*pb.ObjectFilter {
	out := make([]*pb.ObjectFilter, len(filters))
	for i, f := range filters {
		out[i] = &pb.ObjectFilter{Path: f.Path, Include: f.Include, Exclude: f.Exclude}
	}
	return out
}

func (s *server) handleCreatePolicy(w http.ResponseWriter, r *http.Request) {
	in, err := decodePolicyInput(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	resp, err := s.policy.CreatePolicy(r.Context(), &pb.CreatePolicyRequest{
		Name:          in.Name,
		ClientFilters: toProtoClientFiltersInput(in.ClientFilters),
		ObjectFilters: toProtoObjectFiltersInput(in.ObjectFilters),
		Rpo:           in.RPO,
		BackupWindow:  in.BackupWindow,
		Destination:   in.Destination,
	})
	if err != nil {
		s.logger.Error("handleCreatePolicy: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toPolicyDTO(resp))
}

func (s *server) handleUpdatePolicy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	in, err := decodePolicyInput(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	resp, err := s.policy.UpdatePolicy(r.Context(), &pb.UpdatePolicyRequest{
		Id:            id,
		Name:          in.Name,
		ClientFilters: toProtoClientFiltersInput(in.ClientFilters),
		ObjectFilters: toProtoObjectFiltersInput(in.ObjectFilters),
		Rpo:           in.RPO,
		BackupWindow:  in.BackupWindow,
		Destination:   in.Destination,
	})
	if err != nil {
		s.logger.Error("handleUpdatePolicy: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPolicyDTO(resp))
}

func (s *server) handleDeletePolicy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_, err := s.policy.DeletePolicy(r.Context(), &pb.DeletePolicyRequest{Id: id})
	if err != nil {
		s.logger.Error("handleDeletePolicy: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
