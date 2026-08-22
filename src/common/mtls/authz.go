package mtls

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RequireRoles builds a paired gRPC unary/stream interceptor enforcing a
// per-method allowed-role list, read from the caller's peer certificate's
// attribute extension (the "authz-role" key, comma-separated) via
// PeerAttributes. requirements maps a full gRPC method name (e.g.
// "/catalogservice.CatalogService/SyncFileVersions", matching
// grpc.UnaryServerInfo.FullMethod / grpc.StreamServerInfo.FullMethod
// exactly) to the roles allowed to call it. A method absent from
// requirements is unrestricted -- open to any caller regardless of role,
// including one with no role attribute at all (e.g. policy-server's
// GetPolicies, which every enrolled node must be able to call). See
// docs/superpowers/specs/2026-08-22-role-based-grpc-authz-design.md.
func RequireRoles(requirements map[string][]string) (grpc.UnaryServerInterceptor, grpc.StreamServerInterceptor) {
	unary := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if err := checkRole(ctx, requirements, info.FullMethod); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
	stream := func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if err := checkRole(ss.Context(), requirements, info.FullMethod); err != nil {
			return err
		}
		return handler(srv, ss)
	}
	return unary, stream
}

func checkRole(ctx context.Context, requirements map[string][]string, fullMethod string) error {
	allowed, restricted := requirements[fullMethod]
	if !restricted {
		return nil
	}
	attrs, err := PeerAttributes(ctx)
	if err != nil {
		return status.Errorf(codes.PermissionDenied, "role check failed: %v", err)
	}
	for _, callerRole := range splitRoles(attrs["authz-role"]) {
		for _, a := range allowed {
			if callerRole == a {
				return nil
			}
		}
	}
	return status.Errorf(codes.PermissionDenied, "method %s requires role in %v", fullMethod, allowed)
}

func splitRoles(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, ",")
}
