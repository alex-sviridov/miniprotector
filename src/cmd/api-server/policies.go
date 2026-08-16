package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"google.golang.org/protobuf/types/known/timestamppb"
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

type checkinDTO struct {
	Hostname   string `json:"hostname"`
	LastSeenAt int64  `json:"last_seen_at"`
}

type ruleDTO struct {
	Host      string `json:"host"`
	Path      string `json:"path"`
	Include   bool   `json:"include"`
	DestPath  string `json:"dest_path,omitempty"`
	NotBefore int64  `json:"not_before,omitempty"`
	NotAfter  int64  `json:"not_after,omitempty"`
}

type policyDTO struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	CreatedAt       int64             `json:"created_at"`
	UpdatedAt       int64             `json:"updated_at"`
	ClientFilters   clientFiltersDTO  `json:"client_filters"`
	ObjectFilters   []objectFilterDTO `json:"object_filters"`
	RPO             string            `json:"rpo"`
	BackupWindow    []string          `json:"backup_window"`
	Destinations    []string          `json:"destinations"`
	StoragePolicyID string            `json:"storage_policy_id,omitempty"`
	Type            string            `json:"type"`
	Port            int32             `json:"port"`
	Config          string            `json:"config"`
	Rules           []ruleDTO         `json:"rules,omitempty"`
	Mode            string            `json:"mode,omitempty"`
	Overwrite       bool              `json:"overwrite,omitempty"`
	DisabledAt      int64             `json:"disabled_at,omitempty"`
	Checkins        []checkinDTO      `json:"checkins"`
}

func toPolicyDTO(p *pb.Policy) policyDTO {
	objectFilters := make([]objectFilterDTO, len(p.GetObjectFilters()))
	for i, f := range p.GetObjectFilters() {
		objectFilters[i] = objectFilterDTO{ID: f.GetId(), Path: f.GetPath(), Include: f.GetInclude(), Exclude: f.GetExclude()}
	}
	checkins := make([]checkinDTO, len(p.GetCheckins()))
	for i, c := range p.GetCheckins() {
		checkins[i] = checkinDTO{Hostname: c.GetHostname(), LastSeenAt: c.GetLastSeenAt().AsTime().Unix()}
	}
	rules := make([]ruleDTO, len(p.GetRules()))
	for i, r := range p.GetRules() {
		rules[i] = ruleDTO{Host: r.GetHost(), Path: r.GetPath(), Include: r.GetInclude(), DestPath: r.GetDestPath(), NotBefore: r.GetNotBefore(), NotAfter: r.GetNotAfter()}
	}
	dto := policyDTO{
		ID:        p.GetId(),
		Name:      p.GetName(),
		CreatedAt: p.GetCreatedAt().AsTime().Unix(),
		UpdatedAt: p.GetUpdatedAt().AsTime().Unix(),
		ClientFilters: clientFiltersDTO{
			Hostnames: p.GetClientFilters().GetHostnames(),
			Labels:    p.GetClientFilters().GetLabels(),
		},
		ObjectFilters:   objectFilters,
		RPO:             p.GetRpo(),
		BackupWindow:    p.GetBackupWindow(),
		Destinations:    p.GetDestinations(),
		StoragePolicyID: p.GetStoragePolicyId(),
		Type:            p.GetType(),
		Port:            p.GetPort(),
		Config:          p.GetConfig(),
		Rules:           rules,
		Mode:            p.GetMode(),
		Overwrite:       p.GetOverwrite(),
		Checkins:        checkins,
	}
	if p.GetDisabledAt() != nil {
		dto.DisabledAt = p.GetDisabledAt().AsTime().Unix()
	}
	return dto
}

func (s *server) handleListPolicies(w http.ResponseWriter, r *http.Request) {
	resp, err := s.policy.ListPolicies(r.Context(), &pb.ListPoliciesRequest{Type: r.URL.Query().Get("type")})
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
	Name            string              `json:"name"`
	ClientFilters   clientFiltersDTO    `json:"client_filters"`
	ObjectFilters   []objectFilterInput `json:"object_filters"`
	RPO             string              `json:"rpo"`
	BackupWindow    []string            `json:"backup_window"`
	StoragePolicyID string              `json:"storage_policy_id"`
	DisabledAt      int64               `json:"disabled_at,omitempty"`
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

// disabledAtToProto converts an optional unix-seconds REST input value to
// a proto Timestamp, treating 0 (the zero value of an omitted/absent
// field) as "not set" -- mirrors write.go's disabledAtFromProto on the
// policy-server side, which treats a nil Timestamp the same way.
func disabledAtToProto(unixSeconds int64) *timestamppb.Timestamp {
	if unixSeconds == 0 {
		return nil
	}
	return timestamppb.New(time.Unix(unixSeconds, 0))
}

func (s *server) handleCreatePolicy(w http.ResponseWriter, r *http.Request) {
	in, err := decodePolicyInput(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	resp, err := s.policy.CreatePolicy(r.Context(), &pb.CreatePolicyRequest{
		Name:            in.Name,
		Type:            "backup",
		ClientFilters:   toProtoClientFiltersInput(in.ClientFilters),
		ObjectFilters:   toProtoObjectFiltersInput(in.ObjectFilters),
		Rpo:             in.RPO,
		BackupWindow:    in.BackupWindow,
		StoragePolicyId: in.StoragePolicyID,
		DisabledAt:      disabledAtToProto(in.DisabledAt),
	})
	if err != nil {
		s.logger.Error("handleCreatePolicy: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toPolicyDTO(resp))
}

// handleCreateAdhocPolicy creates a one-time backup policy: same input
// shape as POST /api/v1/policies, but backup_window/rpo/disabled_at are
// always computed from s.adhocPolicyTimeout rather than read from the
// request body (any caller-supplied values for those three fields are
// silently ignored) -- backup_window opens every minute so the policy is
// due as soon as a matched node next polls, rpo equals the timeout so it
// fires at most once per node, and disabled_at = now+timeout removes the
// policy (pruning matched nodes' state for it) once every node has had a
// chance to receive and run it.
func (s *server) handleCreateAdhocPolicy(w http.ResponseWriter, r *http.Request) {
	in, err := decodePolicyInput(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	resp, err := s.policy.CreatePolicy(r.Context(), &pb.CreatePolicyRequest{
		Name:            "adhoc_" + in.Name,
		Type:            "backup",
		ClientFilters:   toProtoClientFiltersInput(in.ClientFilters),
		ObjectFilters:   toProtoObjectFiltersInput(in.ObjectFilters),
		Rpo:             s.adhocPolicyTimeout.String(),
		BackupWindow:    []string{"* * * * *"},
		StoragePolicyId: in.StoragePolicyID,
		DisabledAt:      timestamppb.New(time.Now().UTC().Add(s.adhocPolicyTimeout)),
	})
	if err != nil {
		s.logger.Error("handleCreateAdhocPolicy: backend call failed", "error", err)
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
		Id:              id,
		Name:            in.Name,
		ClientFilters:   toProtoClientFiltersInput(in.ClientFilters),
		ObjectFilters:   toProtoObjectFiltersInput(in.ObjectFilters),
		Rpo:             in.RPO,
		BackupWindow:    in.BackupWindow,
		StoragePolicyId: in.StoragePolicyID,
		DisabledAt:      disabledAtToProto(in.DisabledAt),
	})
	if err != nil {
		s.logger.Error("handleUpdatePolicy: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPolicyDTO(resp))
}

type storagePolicyInput struct {
	Name          string           `json:"name"`
	ClientFilters clientFiltersDTO `json:"client_filters"`
	Port          int32            `json:"port"`
	Config        string           `json:"config"`
	DisabledAt    int64            `json:"disabled_at,omitempty"`
}

func decodeStoragePolicyInput(r *http.Request) (storagePolicyInput, error) {
	var in storagePolicyInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return storagePolicyInput{}, err
	}
	return in, nil
}

func (s *server) handleCreateStoragePolicy(w http.ResponseWriter, r *http.Request) {
	in, err := decodeStoragePolicyInput(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	resp, err := s.policy.CreatePolicy(r.Context(), &pb.CreatePolicyRequest{
		Name:          in.Name,
		Type:          "storage",
		ClientFilters: toProtoClientFiltersInput(in.ClientFilters),
		Port:          in.Port,
		Config:        in.Config,
		DisabledAt:    disabledAtToProto(in.DisabledAt),
	})
	if err != nil {
		s.logger.Error("handleCreateStoragePolicy: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toPolicyDTO(resp))
}

func (s *server) handleUpdateStoragePolicy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	in, err := decodeStoragePolicyInput(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	resp, err := s.policy.UpdatePolicy(r.Context(), &pb.UpdatePolicyRequest{
		Id:            id,
		Name:          in.Name,
		ClientFilters: toProtoClientFiltersInput(in.ClientFilters),
		Port:          in.Port,
		Config:        in.Config,
		DisabledAt:    disabledAtToProto(in.DisabledAt),
	})
	if err != nil {
		s.logger.Error("handleUpdateStoragePolicy: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPolicyDTO(resp))
}

type restorePolicyInput struct {
	Name            string           `json:"name"`
	ClientFilters   clientFiltersDTO `json:"client_filters"`
	StoragePolicyID string           `json:"storage_policy_id"`
	Rules           []ruleDTO        `json:"rules"`
	DisabledAt      int64            `json:"disabled_at,omitempty"`
	Mode            string           `json:"mode"`
	Overwrite       bool             `json:"overwrite"`
}

func decodeRestorePolicyInput(r *http.Request) (restorePolicyInput, error) {
	var in restorePolicyInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return restorePolicyInput{}, err
	}
	return in, nil
}

// handleCreateRestore is the sole creation path for "restore"-typed
// policies: POST /api/v1/restore, not POST/PUT /api/v1/restore-policies --
// a restore policy is launched, not managed as a long-lived resource, and
// is never updatable (PUT /api/v1/policies/{id} against one is rejected by
// policy-server itself, see write.go's buildPolicyForUpdate).
//
// mode distinguishes verification (agent runs rwfs verify against the
// resolved rules, no files written) from restore execution (agent runs
// rwfs restore, which this round only resolves and logs the file list --
// still no files written -- see
// docs/superpowers/specs/2026-08-16-restore-execute-log-only-design.md).
// Both modes reach policy-server identically; only the created policy's
// mode field differs.
func (s *server) handleCreateRestore(w http.ResponseWriter, r *http.Request) {
	in, err := decodeRestorePolicyInput(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	mode := in.Mode
	if mode == "" {
		mode = "verify"
	}
	if mode != "verify" && mode != "restore" {
		writeJSONError(w, http.StatusBadRequest, "mode must be 'verify' or 'restore'")
		return
	}

	rules := make([]*pb.RestoreRule, len(in.Rules))
	for i, ru := range in.Rules {
		rules[i] = &pb.RestoreRule{Host: ru.Host, Path: ru.Path, Include: ru.Include, DestPath: ru.DestPath, NotBefore: ru.NotBefore, NotAfter: ru.NotAfter}
	}
	resp, err := s.policy.CreatePolicy(r.Context(), &pb.CreatePolicyRequest{
		Name:            in.Name,
		Type:            "restore",
		ClientFilters:   toProtoClientFiltersInput(in.ClientFilters),
		StoragePolicyId: in.StoragePolicyID,
		Rules:           rules,
		DisabledAt:      disabledAtToProto(in.DisabledAt),
		Mode:            mode,
		Overwrite:       in.Overwrite,
	})
	if err != nil {
		s.logger.Error("handleCreateRestore: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toPolicyDTO(resp))
}

type certStatusDTO struct {
	Hostname      string `json:"hostname"`
	LastError     string `json:"last_error,omitempty"`
	LastAttemptAt int64  `json:"last_attempt_at,omitempty"`
}

// handleGetClientCertStatus reports a node's most recent bootstrap-certificate
// renewal error, if any. Scope is exactly agent's "bootstrap-refresh" task --
// the only key policyclient reads out of agent-state.json. "operating-refresh"
// failures are an explicit non-goal of this design and are never reported
// here, so an empty last_error means "bootstrap renewal reported nothing," not
// "certificate renewal is healthy."
//
// A hostname that has never had a bootstrap renewal failure (or has never
// reported at all) returns 200 with last_error and last_attempt_at omitted --
// proto3's zero values, empty string and a nil Timestamp whose AsTime().Unix()
// is 0 -- rather than 404, matching GetNodeCertStatus's own "not an error"
// contract (Task 1/6). See
// docs/superpowers/specs/2026-08-16-bootstrap-cert-renewal-design.md.
func (s *server) handleGetClientCertStatus(w http.ResponseWriter, r *http.Request) {
	hostname := r.PathValue("hostname")
	resp, err := s.policy.GetNodeCertStatus(r.Context(), &pb.GetNodeCertStatusRequest{Hostname: hostname})
	if err != nil {
		s.logger.Error("handleGetClientCertStatus: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, certStatusDTO{
		Hostname:      resp.GetHostname(),
		LastError:     resp.GetLastError(),
		LastAttemptAt: resp.GetLastAttemptAt().AsTime().Unix(),
	})
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
