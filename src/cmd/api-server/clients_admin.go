// src/cmd/api-server/clients_admin.go
package main

import (
	"encoding/json"
	"net/http"

	pb "github.com/alex-sviridov/miniprotector/api"
)

type addClientInput struct {
	Hostname string   `json:"hostname"`
	Sans     []string `json:"sans"`
}

type reEnrollInput struct {
	Sans []string `json:"sans"`
}

func (s *server) handleAddClient(w http.ResponseWriter, r *http.Request) {
	var in addClientInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if in.Hostname == "" {
		writeJSONError(w, http.StatusBadRequest, "hostname is required")
		return
	}
	resp, err := s.clientManagerAdmin.AddClient(r.Context(), &pb.AddClientRequest{Hostname: in.Hostname, Sans: in.Sans})
	if err != nil {
		s.logger.Error("handleAddClient: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"hostname": in.Hostname, "token": resp.GetToken()})
}

func (s *server) handleReEnrollClient(w http.ResponseWriter, r *http.Request) {
	hostname := r.PathValue("hostname")
	var in reEnrollInput
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}
	resp, err := s.clientManagerAdmin.ReEnrollClient(r.Context(), &pb.ReEnrollClientRequest{Hostname: hostname, Sans: in.Sans})
	if err != nil {
		s.logger.Error("handleReEnrollClient: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hostname": hostname, "token": resp.GetToken()})
}

func (s *server) handleRevokeClient(w http.ResponseWriter, r *http.Request) {
	hostname := r.PathValue("hostname")
	client, err := s.clientManagerAdmin.RevokeClient(r.Context(), &pb.RevokeClientRequest{Hostname: hostname})
	if err != nil {
		s.logger.Error("handleRevokeClient: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toClientDTO(client))
}

func (s *server) handleUnrevokeClient(w http.ResponseWriter, r *http.Request) {
	hostname := r.PathValue("hostname")
	client, err := s.clientManagerAdmin.UnrevokeClient(r.Context(), &pb.UnrevokeClientRequest{Hostname: hostname})
	if err != nil {
		s.logger.Error("handleUnrevokeClient: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toClientDTO(client))
}
