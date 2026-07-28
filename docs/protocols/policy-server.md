# Policy Server Protocol

Any enrolled node (authenticated with its operating credential, `client.crt`/`client.key`) →
`policy-server`'s `GetPolicies` RPC, mTLS (`common/mtls`, same transport every other gRPC call in
this project uses).

## RPC

```proto
service PolicyService {
  rpc GetPolicies(GetPoliciesRequest) returns (GetPoliciesResponse);
  rpc ListPolicies(ListPoliciesRequest) returns (ListPoliciesResponse);
  rpc CreatePolicy(CreatePolicyRequest) returns (Policy);
  rpc UpdatePolicy(UpdatePolicyRequest) returns (Policy);
  rpc DeletePolicy(DeletePolicyRequest) returns (DeletePolicyResponse);
}

message GetPoliciesRequest {}

message GetPoliciesResponse {
  repeated Policy policies = 1;
}

message ListPoliciesRequest {
  string type = 1;
}

message ListPoliciesResponse {
  repeated Policy policies = 1;
}

message ClientFilters {
  repeated string hostnames = 1;
  map<string, string> labels = 2;
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
  ClientFilters client_filters = 9;
  string type = 10;
  string hostname = 11;
  int32 port = 12;
  string config = 13;
}

message CreatePolicyRequest {
  string name = 1;
  ClientFilters client_filters = 2;
  repeated ObjectFilter object_filters = 3;
  string rpo = 4;
  repeated string backup_window = 5;
  string destination = 6;
  string type = 7;
  string hostname = 8;
  int32 port = 9;
  string config = 10;
}

message UpdatePolicyRequest {
  string id = 1;
  string name = 2;
  ClientFilters client_filters = 3;
  repeated ObjectFilter object_filters = 4;
  string rpo = 5;
  repeated string backup_window = 6;
  string destination = 7;
  string hostname = 8;
  int32 port = 9;
  string config = 10;
}

message DeletePolicyRequest {
  string id = 1;
}

message DeletePolicyResponse {}
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
- `Policy.type` is likewise computed, not read from the file -- derived from the name of the
  immediate subfolder the policy file lives in under `$MP_CONFIG_PATH/policies/` (`"backup"` or
  `"storage"` today). Populated by both `GetPolicies` and `ListPolicies`. `CreatePolicyRequest.type`
  is required and selects which policy type is created (`policies/<type>/`, creating that
  subdirectory if missing); a request that also sets fields belonging to the other type is rejected.
  `UpdatePolicyRequest` carries no `type` field -- a policy's type is immutable via `UpdatePolicy`,
  derived from the record being updated. See
  [Design: Policy Type Subfolders](../superpowers/specs/2026-07-20-policy-type-subfolders-design.md)
  and
  [Design: Storage Policy Type](../superpowers/specs/2026-07-28-storage-policy-type-design.md).
- `hostname`/`port`/`config` are only meaningful on a `"storage"`-typed policy -- unset/zero on a
  `"backup"`-typed one, and vice versa for `object_filters`/`rpo`/`backup_window`/`destination`.
  `config` is opaque, pass-through JSON text -- `policy-server` validates it's well-formed at load
  and write time but never interprets its contents.
- Each `object_filters` entry's `include`/`exclude` are opaque, pass-through glob-pattern lists —
  `policy-server` validates their syntax at load time but never evaluates them; `brfs` is what
  applies them, during its own directory walk.
- `rpo` and `backup_window` are opaque, pass-through strings — `policy-server` never parses or
  evaluates either. `destination` is likewise opaque, pass-through data — `policy-server` never
  validates or connects to it.
- `ListPolicies`/`CreatePolicy`/`UpdatePolicy`/`DeletePolicy` are the admin surface `api-server`
  proxies for browsing and editing the full policy set — never called by a mesh node. Unlike
  `GetPolicies`, `ListPolicies`'s response (and `Create`/`UpdatePolicy`'s echoed-back result)
  includes `client_filters`. `Create`/`UpdatePolicy` validate the same way `parsePolicyFile` does
  (non-empty `metadata.name`, syntactically valid glob patterns) before writing anything; a write
  that fails validation returns `INVALID_ARGUMENT` and touches no file. `Update`/`Delete` address a
  policy by its `id`; `Update` keeps the on-disk filename (and therefore the `id`) unchanged,
  overwriting only the file's content. Every write reloads `policy-server`'s own in-memory cache
  synchronously before responding, bypassing the `.changed` sentinel entirely — that remains solely
  the mechanism for an operator's own manual, possibly multi-file, batch edits.
- `ListPoliciesRequest.type` is an optional filter — `"backup"` or `"storage"` restricts the
  response to that type; empty (the default) returns every type, unchanged from before this field
  existed. A `type` value that matches no loaded policy's `Kind()` returns an empty list, not an
  error — there is no closed enum at this layer, `Kind()` is just whatever string the type
  subfolder produced.

## See Also

- [policy-server](../components/policy-server.md)
- [issuer](../components/issuer.md) — embeds the attribute extension this protocol's authorization
  depends on
- [Design: Policy Server](../superpowers/specs/2026-07-10-policy-server-design.md)
- [Design: Policy Type Subfolders](../superpowers/specs/2026-07-20-policy-type-subfolders-design.md)
- [Design: Storage Policy Type](../superpowers/specs/2026-07-28-storage-policy-type-design.md)
