package mtls

import (
	"context"
	"fmt"
	"net/http"
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
	if !hasRole(attrs, allowed) {
		return status.Errorf(codes.PermissionDenied, "method %s requires role in %v", fullMethod, allowed)
	}
	return nil
}

// hasRole reports whether attrs' comma-separated authz-role includes any of
// allowed. A caller with no role attribute never matches.
func hasRole(attrs map[string]string, allowed []string) bool {
	for _, callerRole := range splitRoles(attrs["authz-role"]) {
		for _, a := range allowed {
			if callerRole == a {
				return true
			}
		}
	}
	return false
}

// RequireRolesHTTP is RequireRoles' plain-HTTP counterpart, for a server (like
// log-gateway) built on net/http.Server with mTLS terminated by it. It wraps a
// handler so only a caller whose verified peer certificate carries one of
// allowed in its authz-role attribute reaches it: 401 when no peer
// certificate was presented, 403 when the role does not match.
func RequireRolesHTTP(allowed ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attrs, err := PeerAttributesFromConnState(r.TLS)
			if err != nil {
				http.Error(w, "determine caller identity: "+err.Error(), http.StatusUnauthorized)
				return
			}
			if !hasRole(attrs, allowed) {
				http.Error(w, fmt.Sprintf("requires role in %v", allowed), http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func splitRoles(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, ",")
}
