// Package jobid provides the shared per-invocation correlation-ID
// convention used across this project: a caller resolves or generates one,
// attaches it to outgoing gRPC metadata, and the server requires and reads
// it back. brfs/bwfs originated this pattern (see
// docs/superpowers/specs/2026-07-11-fleet-log-aggregation-design.md);
// this package is that pattern extracted so every other cross-host caller
// agent drives (certclient, policyclient) and callee (issuer,
// policy-server) can share one implementation instead of three copies.
package jobid

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"google.golang.org/grpc/metadata"
)

// metadataKey is the gRPC metadata key job-id rides under, on the wire.
const metadataKey = "job-id"

// Resolve returns id unchanged if non-empty, otherwise a freshly generated
// UUID -- the shared "auto-generate if a --job-id flag was omitted"
// behavior every caller in this project uses.
func Resolve(id string) string {
	if id != "" {
		return id
	}
	return uuid.New().String()
}

// Outgoing attaches id to ctx's outgoing gRPC metadata under the job-id
// key, returning the derived context callers must use for the RPC call.
func Outgoing(ctx context.Context, id string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, metadataKey, id)
}

// FromIncoming reads the job-id gRPC metadata key from ctx's incoming
// metadata. There is no default: a call missing it returns an error rather
// than being silently treated as jobless -- callers decide how to handle
// that (every server in this project rejects the request outright).
func FromIncoming(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", fmt.Errorf("no metadata in request")
	}
	values := md.Get(metadataKey)
	if len(values) == 0 || values[0] == "" {
		return "", fmt.Errorf("missing job-id metadata")
	}
	return values[0], nil
}
