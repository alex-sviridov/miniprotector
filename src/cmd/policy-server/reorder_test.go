package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/alex-sviridov/miniprotector/api"
)

func ids(ps []*pb.Policy) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.GetId()
	}
	return out
}

func listRetention(t *testing.T, srv *policyServerServer) []*pb.Policy {
	t.Helper()
	resp, err := srv.ListPolicies(context.Background(), &pb.ListPoliciesRequest{Type: "retention"})
	require.NoError(t, err)
	return resp.GetPolicies()
}

func TestReorderRetentionPolicies_RewritesPrioritiesInRequestedOrder(t *testing.T) {
	srv := newTestWriteServer(t, t.TempDir())
	a := createRetention(t, srv, "a", "/a")
	b := createRetention(t, srv, "b", "/b")
	c := createRetention(t, srv, "c", "/c")

	resp, err := srv.ReorderRetentionPolicies(context.Background(),
		&pb.ReorderRetentionPoliciesRequest{Ids: []string{c.GetId(), a.GetId(), b.GetId()}})
	require.NoError(t, err)

	assert.Equal(t, []string{c.GetId(), a.GetId(), b.GetId()}, ids(resp.GetPolicies()))
	for i, p := range resp.GetPolicies() {
		assert.Equal(t, int32(i+1), p.GetRetention().GetPriority())
	}
	assert.Equal(t, []string{c.GetId(), a.GetId(), b.GetId()}, ids(listRetention(t, srv)), "list must agree with the reorder response")
}

func TestReorderRetentionPolicies_PersistsAcrossReload(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	a := createRetention(t, srv, "a", "/a")
	b := createRetention(t, srv, "b", "/b")
	_, err := srv.ReorderRetentionPolicies(context.Background(),
		&pb.ReorderRetentionPoliciesRequest{Ids: []string{b.GetId(), a.GetId()}})
	require.NoError(t, err)

	reloaded := newTestWriteServer(t, dir)
	assert.Equal(t, []string{b.GetId(), a.GetId()}, ids(listRetention(t, reloaded)))
}

func TestReorderRetentionPolicies_RejectsWrongIDSetAndChangesNothing(t *testing.T) {
	srv := newTestWriteServer(t, t.TempDir())
	a := createRetention(t, srv, "a", "/a")
	b := createRetention(t, srv, "b", "/b")
	storageID := createTestStoragePolicy(t, srv, "store-1", 9400)
	original := ids(listRetention(t, srv))

	cases := map[string][]string{
		"missing an id":    {a.GetId()},
		"unknown id":       {a.GetId(), b.GetId(), "nope"},
		"duplicate id":     {a.GetId(), a.GetId(), b.GetId()},
		"non-retention id": {a.GetId(), b.GetId(), storageID},
		"empty with some":  {},
		"only unknown":     {"nope"},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := srv.ReorderRetentionPolicies(context.Background(), &pb.ReorderRetentionPoliciesRequest{Ids: req})
			require.Error(t, err)
			assert.Equal(t, codes.InvalidArgument, status.Code(err))
			assert.Equal(t, original, ids(listRetention(t, srv)))
		})
	}
}

func TestReorderRetentionPolicies_EmptyListWithNoPoliciesSucceeds(t *testing.T) {
	srv := newTestWriteServer(t, t.TempDir())
	resp, err := srv.ReorderRetentionPolicies(context.Background(), &pb.ReorderRetentionPoliciesRequest{})
	require.NoError(t, err)
	assert.Empty(t, resp.GetPolicies())
}

func TestReorderRetentionPolicies_NewPolicyAfterReorderGoesLast(t *testing.T) {
	srv := newTestWriteServer(t, t.TempDir())
	a := createRetention(t, srv, "a", "/a")
	b := createRetention(t, srv, "b", "/b")
	_, err := srv.ReorderRetentionPolicies(context.Background(),
		&pb.ReorderRetentionPoliciesRequest{Ids: []string{b.GetId(), a.GetId()}})
	require.NoError(t, err)

	c := createRetention(t, srv, "c", "/c")

	assert.Equal(t, int32(3), c.GetRetention().GetPriority())
	assert.Equal(t, []string{b.GetId(), a.GetId(), c.GetId()}, ids(listRetention(t, srv)))
}

func TestRoleRequirements_ReorderRetentionPoliciesIsControlPlaneOnly(t *testing.T) {
	svc := pb.PolicyService_ServiceDesc.ServiceName
	assert.Equal(t, []string{"control-plane"}, roleRequirements()["/"+svc+"/ReorderRetentionPolicies"])
}
