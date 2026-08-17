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
  rpc GetNodeCertStatus(GetNodeCertStatusRequest) returns (NodeCertStatus);
}

message GetPoliciesRequest {
  string bootstrap_refresh_last_error = 1; // Set only when this node's bootstrap-refresh task is currently failing; empty means healthy or nothing to report
  int64  bootstrap_refresh_last_attempt_at = 2; // unix seconds; 0 = not reported
}

message GetPoliciesResponse {
  repeated Policy policies = 1;
}

message ListPoliciesRequest {
  // Optional. "backup", "storage", or "restore" -- when set, only policies
  // of this type are returned. Empty returns every type (unfiltered,
  // today's behavior).
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

message PolicyCheckin {
  string hostname = 1;
  google.protobuf.Timestamp last_seen_at = 2;
}

message GetNodeCertStatusRequest {
  string hostname = 1; // required
}

message NodeCertStatus {
  string hostname = 1;
  string last_error = 2; // "" = healthy or never reported
  google.protobuf.Timestamp last_attempt_at = 3;
}

message RestoreRule {
  string host      = 1; // "" = host-agnostic, matches every source host
  string path      = 2;
  bool   include    = 3;
  string dest_path = 4; // destination to restore to if renamed; "" or == path means no rename; only valid when include is true
  int64  not_before = 5; // restore this rule's latest backup dated on/after this unix time; 0 = unbounded
  int64  not_after  = 6; // ...and on/before this unix time; 0 = unbounded. Outside the window = ignored, not a fallback.
}

message Policy {
  string name = 1;
  google.protobuf.Timestamp created_at = 2;
  google.protobuf.Timestamp updated_at = 3;
  repeated ObjectFilter object_filters = 4;
  string rpo = 5;
  repeated string backup_window = 6;
  reserved 7; reserved "destination"; // removed -- replaced by destinations, see below
  string id = 8;
  ClientFilters client_filters = 9;
  string type = 10;
  reserved 11; reserved "hostname"; // formerly hostname -- removed, see below
  int32 port = 12;
  // "storage" policy only -- opaque JSON text, verbatim passthrough. Never
  // parsed or interpreted by policy-server beyond checking
  // well-formedness. A "restore" policy carries no config: its selection
  // lives in the structured rules field (19) instead, as of 2026-08-10.
  string config = 13;
  google.protobuf.Timestamp disabled_at = 14;
  string storage_policy_id = 15; // "backup" and "restore" policy only, required
  repeated PolicyCheckin checkins = 16; // ListPolicies only, not GetPolicies; one entry per host that has received this policy
  repeated string destinations = 17; // backup and restore policy only, derived, read-only -- see below
  reserved 18; reserved "source_store"; // removed 2026-08-10, replaced by storage_policy_id (15) + rules (19) -- see below
  // "restore" policy only.
  repeated RestoreRule rules = 19;
  // "restore" policy only. "" or "verify" behaves exactly as every restore
  // policy does today (agent runs rwfs verify, writes nothing). "restore"
  // dispatches rwfs restore, which creates the resolved directory
  // structure and writes the resolved file content. A restore
  // policy JSON file written before this field existed has no "mode" key
  // at all and is unaffected -- absent is read as "verify".
  string mode = 20;
  // "restore" policy only. Carried through and logged by rwfs restore;
  // has no effect when mode is "verify" or unset -- the web UI already
  // sends this checkbox unconditionally on every submit (see
  // docs/superpowers/specs/2026-08-14-restore-verify-execute-split-design.md),
  // so it is simply inert for a verify submission.
  bool overwrite = 21;
}

message CreatePolicyRequest {
  string name = 1;
  ClientFilters client_filters = 2;
  repeated ObjectFilter object_filters = 3;
  string rpo = 4;
  repeated string backup_window = 5;
  reserved 6; reserved "destination"; // removed -- never itself writable, see below
  // "backup", "storage", or "restore" -- required. Determines which of the
  // type-specific fields are valid; mixing fields across types is rejected
  // (e.g. a "restore" request must not set object_filters/rpo/backup_window/port/config).
  string type = 7;
  reserved 8; reserved "hostname"; // formerly hostname -- removed, see below
  int32 port = 9;
  string config = 10;
  google.protobuf.Timestamp disabled_at = 11;
  string storage_policy_id = 12; // "backup" and "restore" policy only, required
  reserved 13; reserved "source_store"; // removed 2026-08-10, see Policy.rules above
  // "restore" policy only, required.
  repeated RestoreRule rules = 14;
  // "restore" policy only. See Policy.mode above.
  string mode = 15;
  // "restore" policy only. See Policy.overwrite above.
  bool overwrite = 16;
}

message UpdatePolicyRequest {
  string id = 1;
  string name = 2;
  ClientFilters client_filters = 3;
  repeated ObjectFilter object_filters = 4;
  string rpo = 5;
  repeated string backup_window = 6;
  reserved 7; reserved "destination"; // removed -- never itself writable, see below
  reserved 8; reserved "hostname"; // formerly hostname -- removed, see below
  int32 port = 9;
  string config = 10;
  google.protobuf.Timestamp disabled_at = 11;
  string storage_policy_id = 12; // backup policy only, required
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

- `GetPoliciesRequest` carries optional fields: `bootstrap_refresh_last_error` (empty if healthy or nothing to report) and `bootstrap_refresh_last_attempt_at` (unix seconds; 0 = not reported). `policy-server` records whatever is sent, healthy or not, so a recovery is visible too.
- `GetNodeCertStatus(GetNodeCertStatusRequest{hostname})` queries a node's certificate renewal status. Returns a `NodeCertStatus` with the queried `hostname`, the most recent `last_error` (empty if healthy or never reported), and `last_attempt_at` (a timestamp, or unset if never attempted). Scope is exactly `agent`'s `bootstrap-refresh` task; `operating-refresh` failures are never reported here. A hostname with no reported status returns a present result with an empty `last_error` and a genuinely **unset** (nil) `last_attempt_at` — never an error, and never a filled-in timestamp. The distinction matters downstream: `api-server` renders this field as `AsTime().Unix()` into an `omitempty` int64, so a set-but-zero-valued `Timestamp` built from Go's zero `time.Time` would serialize as `-62135596800` instead of being omitted. See [Design: Bootstrap Certificate Renewal](../superpowers/specs/2026-08-16-bootstrap-cert-renewal-design.md).
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
- `port`/`config` are only meaningful on a `"storage"`-typed policy -- unset/zero on a
  `"backup"`-typed one, and vice versa for `object_filters`/`rpo`/`backup_window`/`storage_policy_id`.
  `destinations` is never itself a settable field on either type -- see below.
  `config` is opaque, pass-through JSON text -- `policy-server` validates it's well-formed at load
  and write time but never interprets its contents. There is no separate `hostname` field on a
  storage policy (removed -- see
  [Design: agent storage-policy supervision](../superpowers/specs/2026-07-28-agent-storage-supervision-design.md));
  targeting a node is `client_filters` only, identical to a backup policy.
- A `"restore"` policy has `storage_policy_id` (required, references a `"storage"`-typed policy's `id` —
  the same field and live-resolution mechanism a `"backup"` policy already uses; its `destinations` is
  computed the identical way) and `rules` (required, at least one entry — `{host, path, include}`, where
  an empty `host` means the rule applies across every source host; each rule may also set `not_before`
  and `not_after` to scope which backed-up version of that rule's selection is used — resolved at verify time).
  It has no `object_filters`, `rpo`, `backup_window`, `port`, or `config`. A `"restore"` policy is never updatable:
  `UpdatePolicy` returns `INVALID_ARGUMENT` for any request whose target policy is type `"restore"`, regardless of which
  fields it sets -- a restore is a point-in-time instruction, so changing one after the fact is a
  new policy, not an edit (which is also why `UpdatePolicyRequest` has no `rules` field). `mode`
  (`"verify"`, the default, or `"restore"`) selects which action `agent` performs -- `rwfs verify`
  (unchanged) or `rwfs restore` (resolves the file list, creates the resolved directory structure on
  disk, and writes the resolved file content). `overwrite` is carried through by `rwfs restore` and
  now enforced: an existing destination file is skipped when false, overwritten when true.
- `disabled_at` is generic across every policy type -- unset (zero/nil) means never disabled. Once it
  passes, `GetPolicies` stops returning that policy to any node, checked live against the current
  time on every call (not cached at load/reload time) -- no `.changed`-touch or restart needed for a
  policy to disappear once its `disabled_at` arrives. `ListPolicies` is unaffected: it keeps returning
  every policy regardless of `disabled_at`, since it's the full-visibility admin surface `api-server`
  proxies. `UpdatePolicy` treats `disabled_at` as full-replace, the same as every other editable
  field -- an update that omits it clears it, it is not preserved automatically the way `created_at`
  is. There is no validation rejecting a `disabled_at` already in the past; a policy created or
  updated that way is simply already inert.
- Each `object_filters` entry's `include`/`exclude` are opaque, pass-through glob-pattern lists —
  `policy-server` validates their syntax at load time but never evaluates them; `brfs` is what
  applies them, during its own directory walk.
- `rpo` and `backup_window` are opaque, pass-through strings — `policy-server` never parses or
  evaluates either. `destinations` is derived, read-only: a `"backup"` or `"restore"` policy instead
  carries `storage_policy_id`, a required reference to a `"storage"`-typed policy's `id`.
  `GetPolicies`/`ListPolicies`/`CreatePolicy`/`UpdatePolicy` all resolve it live, on every response,
  from that storage policy's checkin records (see [Design: backup destination from checkin
  list](../superpowers/specs/2026-08-04-backup-destination-checkin-list-design.md)) — one
  `"host:port"` entry per host that has checked in against the storage policy, combined with its
  `port`, ordered freshest-checked-in-first. This is a real, client-confirmed list of storage
  servers, not a static `client_filters.hostnames` guess: a storage policy targeted purely by labels
  (no `client_filters.hostnames`) now still resolves correctly once any matching node checks in,
  closing the gap the previous hostname-pattern-based resolution had. `destinations` is empty if the
  reference doesn't resolve (an id that doesn't exist, or no longer names a storage policy) or if the
  referenced storage policy has no checkins yet (a brand-new one, or one every check-in for has aged
  past `CheckinRetentionSec`).
- `ListPolicies`/`CreatePolicy`/`UpdatePolicy`/`DeletePolicy` are the admin surface `api-server`
  proxies for browsing and editing the full policy set — never called by a mesh node. Unlike
  `GetPolicies`, `ListPolicies`'s response (and `Create`/`UpdatePolicy`'s echoed-back result)
  includes `client_filters`. `Create`/`UpdatePolicy` validate the same way `parsePolicyFile` does
  (non-empty `metadata.name`, syntactically valid glob patterns) before writing anything; a write
  that fails validation returns `INVALID_ARGUMENT` and touches no file. For a `"backup"` policy,
  both also require `storage_policy_id` to be non-empty and to name a currently-loaded `"storage"`
  policy, checked against the live cache at write time — something `Validate()` alone can't check,
  since it never sees the rest of the loaded set. `DeletePolicy` on a `"storage"` policy fails with
  `INVALID_ARGUMENT`, naming the offending policies, if any `"backup"` policy still references it.
  `Update`/`Delete` address a policy by its `id`; `Update` keeps the on-disk filename (and therefore
  the `id`) unchanged, overwriting only the file's content. Every write reloads `policy-server`'s own
  in-memory cache synchronously before responding, bypassing the `.changed` sentinel entirely — that
  remains solely the mechanism for an operator's own manual, possibly multi-file, batch edits.
- `ListPoliciesRequest.type` is an optional filter — `"backup"`, `"storage"`, or `"restore"` restricts the
  response to that type; empty (the default) returns every type, unchanged from before this field
  existed. A `type` value that matches no loaded policy's `Kind()` returns an empty list, not an
  error — there is no closed enum at this layer, `Kind()` is just whatever string the type
  subfolder produced.
- `Policy.checkins` is populated only by `ListPolicies` -- `GetPolicies`, `CreatePolicy`, and
  `UpdatePolicy` always leave it empty, the same way `GetPolicies`'s response never echoes back
  `client_filters`. Each entry is one host's most recent check-in for that policy (`hostname` +
  `last_seen_at`) -- `GetPolicies` upserts one such row per policy it returns to a caller, on every
  call. See [Design: Policy Check-in Tracking](../superpowers/specs/2026-08-03-policy-checkin-tracking-design.md).

## See Also

- [policy-server](../components/policy-server.md)
- [issuer](../components/issuer.md) — embeds the attribute extension this protocol's authorization
  depends on
- [Design: Policy Server](../superpowers/specs/2026-07-10-policy-server-design.md)
- [Design: Policy Type Subfolders](../superpowers/specs/2026-07-20-policy-type-subfolders-design.md)
- [Design: Storage Policy Type](../superpowers/specs/2026-07-28-storage-policy-type-design.md)
- [Design: link backup policies to storage policies by id](../superpowers/specs/2026-08-03-backup-policy-storage-link-design.md)
- [Design: Policy Check-in Tracking](../superpowers/specs/2026-08-03-policy-checkin-tracking-design.md)
- [Design: Bootstrap Certificate Renewal](../superpowers/specs/2026-08-16-bootstrap-cert-renewal-design.md)
