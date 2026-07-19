// src/cmd/api-server/clients_admin.go
package main

import (
	"context"
	"encoding/json"
	"net/http"

	pb "github.com/alex-sviridov/miniprotector/api"
	"google.golang.org/grpc"
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

type kvUpdateInput struct {
	Set   map[string]string `json:"set"`
	Unset []string          `json:"unset"`
}

func (s *server) handleUpdateDescription(w http.ResponseWriter, r *http.Request) {
	s.handleUpdateKV(w, r, s.clientManagerAdmin.UpdateDescription)
}

func (s *server) handleUpdateAttributes(w http.ResponseWriter, r *http.Request) {
	s.handleUpdateKV(w, r, s.clientManagerAdmin.UpdateAttributes)
}

func (s *server) handleUpdateKV(w http.ResponseWriter, r *http.Request, call func(ctx context.Context, in *pb.UpdateClientKVRequest, opts ...grpc.CallOption) (*pb.Client, error)) {
	hostname := r.PathValue("hostname")
	var in kvUpdateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	client, err := call(r.Context(), &pb.UpdateClientKVRequest{Hostname: hostname, Set: in.Set, Unset: in.Unset})
	if err != nil {
		s.logger.Error("handleUpdateKV: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toClientDTO(client))
}
