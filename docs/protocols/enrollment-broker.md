# Enrollment Broker Protocol

`client-manager` → `certrequest serve`'s `MintEnrollmentToken` RPC, mTLS (`common/mtls`, same
transport every other gRPC call in this project uses — no bespoke wire format).

## RPC

```proto
service EnrollmentBrokerService {
  rpc MintEnrollmentToken(MintEnrollmentTokenRequest) returns (MintEnrollmentTokenResponse);
}

message MintEnrollmentTokenRequest {
  string hostname = 1;
  repeated string sans = 2;
}

message MintEnrollmentTokenResponse {
  string token = 1;
}
```

## Authorization

The server (`certrequest serve`) checks the caller's mTLS-verified hostname
(`mtls.PeerHostname(ctx)`) against its own configured `client_manager_host`. A mismatch is
rejected outright — this is the one RPC in the system that does **not** use "any CA-signed cert is
trusted"; it's equivalent to CA-admin privilege, so only the single configured caller may invoke
it at all.

## Behavior

- `hostname`/`sans` mirror `certrequest`'s existing one-shot CLI's positional argument and `--san`
  flags — same minting call underneath (`common/certmint`), same token semantics (short-lived,
  single-use, `jti`-tracked by the CA itself).
- The returned `token` is never persisted by either side — `client-manager` prints it to stdout
  for the operator to relay out-of-band, same as `certrequest`'s CLI does today.
- Any minting failure (bad hostname, CA unreachable, provisioner password unreadable) surfaces as
  a gRPC error; the caller (`client-manager add`/`re-enroll`) does not record anything locally
  unless a token was actually returned.

## See Also

- [certrequest](../components/certrequest.md) — `serve` mode
- [client-manager](../components/client-manager.md)
- [Design: Client Manager](../superpowers/specs/2026-07-04-client-manager-design.md)
