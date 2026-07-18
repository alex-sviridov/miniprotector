package main

import (
	"net/http"

	pb "github.com/alex-sviridov/miniprotector/api"
)

type clientDTO struct {
	Hostname     string            `json:"hostname"`
	Revoked      bool              `json:"revoked"`
	RevokedAt    int64             `json:"revoked_at"`
	LastSeenAt   int64             `json:"last_seen_at"`
	Sans         []string          `json:"sans"`
	Attributes   map[string]string `json:"attributes"`
	Descriptions map[string]string `json:"descriptions"`
}

func toClientDTO(c *pb.Client) clientDTO {
	return clientDTO{
		Hostname:     c.GetHostname(),
		Revoked:      c.GetRevoked(),
		RevokedAt:    c.GetRevokedAt(),
		LastSeenAt:   c.GetLastSeenAt(),
		Sans:         c.GetSans(),
		Attributes:   c.GetAttributes(),
		Descriptions: c.GetDescriptions(),
	}
}

func (s *server) handleListClients(w http.ResponseWriter, r *http.Request) {
	resp, err := s.clientManager.ListClients(r.Context(), &pb.ListClientsRequest{})
	if err != nil {
		s.logger.Error("handleListClients: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}

	clients := make([]clientDTO, len(resp.GetClients()))
	for i, c := range resp.GetClients() {
		clients[i] = toClientDTO(c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": clients})
}

func (s *server) handleGetClient(w http.ResponseWriter, r *http.Request) {
	hostname := r.PathValue("hostname")
	client, err := s.clientManager.GetClient(r.Context(), &pb.GetClientRequest{Hostname: hostname})
	if err != nil {
		s.logger.Error("handleGetClient: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toClientDTO(client))
}
