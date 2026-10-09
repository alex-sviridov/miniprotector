package mtls

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func handlerRecorder() (grpc.UnaryHandler, *bool) {
	called := false
	return func(ctx context.Context, req any) (any, error) {
		called = true
		return "ok", nil
	}, &called
}

func TestRequireRoles_AllowsMatchingRole(t *testing.T) {
	cert := selfSignedCertWithAttributes(t, "node-1", map[string]string{"authz-role": "control-plane"})
	ctx := contextWithPeerCert(cert)
	unary, _ := RequireRoles(map[string][]string{"/svc/Method": {"control-plane"}})
	handler, called := handlerRecorder()

	resp, err := unary(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/svc/Method"}, handler)
	require.NoError(t, err)
	assert.Equal(t, "ok", resp)
	assert.True(t, *called)
}

func TestRequireRoles_DeniesMismatchedRole(t *testing.T) {
	cert := selfSignedCertWithAttributes(t, "node-1", map[string]string{"authz-role": "client"})
	ctx := contextWithPeerCert(cert)
	unary, _ := RequireRoles(map[string][]string{"/svc/Method": {"control-plane"}})
	handler, called := handlerRecorder()

	_, err := unary(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/svc/Method"}, handler)
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	assert.False(t, *called)
}

func TestRequireRoles_DeniesMissingRoleAttribute(t *testing.T) {
	cert := selfSignedCertNoSAN(t, "node-1")
	ctx := contextWithPeerCert(cert)
	unary, _ := RequireRoles(map[string][]string{"/svc/Method": {"control-plane"}})
	handler, called := handlerRecorder()

	_, err := unary(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/svc/Method"}, handler)
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	assert.False(t, *called)
}

func TestRequireRoles_MethodNotInMapIsOpen(t *testing.T) {
	cert := selfSignedCertNoSAN(t, "node-1")
	ctx := contextWithPeerCert(cert)
	unary, _ := RequireRoles(map[string][]string{"/svc/Restricted": {"control-plane"}})
	handler, called := handlerRecorder()

	_, err := unary(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/svc/Open"}, handler)
	require.NoError(t, err)
	assert.True(t, *called)
}

func TestRequireRoles_MultiRoleCommaListMatchesAny(t *testing.T) {
	cert := selfSignedCertWithAttributes(t, "node-1", map[string]string{"authz-role": "store,client"})
	ctx := contextWithPeerCert(cert)
	unary, _ := RequireRoles(map[string][]string{"/svc/Method": {"client"}})
	handler, called := handlerRecorder()

	_, err := unary(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/svc/Method"}, handler)
	require.NoError(t, err)
	assert.True(t, *called)
}

func TestRequireRoles_NilRequirementsIsFullyOpen(t *testing.T) {
	cert := selfSignedCertNoSAN(t, "node-1")
	ctx := contextWithPeerCert(cert)
	unary, _ := RequireRoles(nil)
	handler, called := handlerRecorder()

	_, err := unary(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/svc/Method"}, handler)
	require.NoError(t, err)
	assert.True(t, *called)
}

// fakeServerStream is a minimal grpc.ServerStream: RequireRoles's stream
// path only ever calls Context().
type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f *fakeServerStream) Context() context.Context { return f.ctx }

func TestRequireRoles_StreamDeniesMismatchedRole(t *testing.T) {
	cert := selfSignedCertWithAttributes(t, "node-1", map[string]string{"authz-role": "client"})
	ctx := contextWithPeerCert(cert)
	_, stream := RequireRoles(map[string][]string{"/svc/Stream": {"control-plane"}})
	called := false
	handler := func(srv any, ss grpc.ServerStream) error {
		called = true
		return nil
	}

	err := stream(nil, &fakeServerStream{ctx: ctx}, &grpc.StreamServerInfo{FullMethod: "/svc/Stream"}, handler)
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	assert.False(t, called)
}

func TestRequireRoles_StreamAllowsMatchingRole(t *testing.T) {
	cert := selfSignedCertWithAttributes(t, "node-1", map[string]string{"authz-role": "client"})
	ctx := contextWithPeerCert(cert)
	_, stream := RequireRoles(map[string][]string{"/svc/Stream": {"client"}})
	called := false
	handler := func(srv any, ss grpc.ServerStream) error {
		called = true
		return nil
	}

	err := stream(nil, &fakeServerStream{ctx: ctx}, &grpc.StreamServerInfo{FullMethod: "/svc/Stream"}, handler)
	require.NoError(t, err)
	assert.True(t, called)
}
