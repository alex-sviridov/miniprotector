# Policy Server Protocol

Any enrolled node (authenticated with its operating credential, `client.crt`/`client.key`) →
`policy-server`'s `GetPolicies` RPC, mTLS (`common/mtls`, same transport every other gRPC call in
this project uses).

## RPC

```proto
service PolicyService {
  rpc GetPolicies(GetPoliciesRequest) returns (GetPoliciesResponse);
}

message GetPoliciesRequest {}

message GetPoliciesResponse {
  repeated Policy policies = 1;
}

message ObjectFilter {
  string path = 1;
  repeated string include = 2;
  repeated string exclude = 3;
  string id = 4;
}

message Policy {
  string name = 1;
  google.protobuf.Timestamp created_at = 2;
  google.protobuf.Timestamp updated_at = 3;
  repeated ObjectFilter object_filters = 4;
  string rpo = 5;
  repeated string backup_window = 6;
  string destination = 7;
  string id = 8;
}
```

## Authorization

The caller's hostname is always derived from its verified mTLS peer identity
(`mtls.PeerHostname`); the caller's attribute labels are always derived from the same peer
certificate's embedded attribute extension (`mtls.PeerAttributes`, reading the custom X.509
extension `issuer` bakes into every operating certificate it mints). Neither is ever a field on
`GetPoliciesRequest`. `policy-server`'s listener requires the default operating-tier peer
certificate — the same requirement every server except `issuer`'s own listener enforces.

## Behavior

- `GetPoliciesRequest` is empty — no fields to set.
- `GetPoliciesResponse.policies` contains every policy whose `client_filters` match the caller:
  hostname glob match (or no hostname restriction) **and** every required label present — both
  conditions must hold. `client_filters` itself is never echoed back.
- Both `Policy.id` and each `ObjectFilter.id` are computed by `policy-server` itself --
  deterministically, from the policy file's name (and each object filter's position within it) --
  never read from or written to the on-disk policy JSON. They exist so two policies, or two object
  filters within one policy, can never be confused with each other downstream even when their
  human-facing `name`/`path` happen to collide.
- Each `object_filters` entry's `include`/`exclude` are opaque, pass-through glob-pattern lists —
  `policy-server` validates their syntax at load time but never evaluates them; `brfs` is what
  applies them, during its own directory walk.
- `rpo` and `backup_window` are opaque, pass-through strings — `policy-server` never parses or
  evaluates either. `destination` is likewise opaque, pass-through data — `policy-server` never
  validates or connects to it.

## See Also

- [policy-server](../components/policy-server.md)
- [issuer](../components/issuer.md) — embeds the attribute extension this protocol's authorization
  depends on
- [Design: Policy Server](../superpowers/specs/2026-07-10-policy-server-design.md)
