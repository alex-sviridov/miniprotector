# policyclient

Fetches this node's applicable backup policies from `policy-server` and caches them locally as
`policies-cache.json`. Does not act on the cached policies — no scheduling, no interpretation of
`rpo`/`backup_window`; that remains separate, later work. See
[Design: Agent Policy-Update Job](../superpowers/specs/2026-07-10-agent-policy-update-job-design.md).
**Agent tool** — bundled onto every node also running `agent`.

## Usage

```bash
policyclient fetch
policyclient --debug fetch
```

| Flag | Subcommand | Default | Description |
|------|------------|---------|-------------|
| `--debug` | root (applies to all subcommands) | `false` | Enable debug logging |

Requires `policy_server_host` set in `local.conf` (`policy_server_port` defaults to `9300`).

## Behavior

`fetch` dials `policy-server` authenticated with this node's existing **operating credential**
(`client.crt`/`client.key`, the same default identity `bwfs`/`brfs`/`rwfs`/`catalogsync`/`catalog`
already use) — required, not a choice: `policy-server` matches policies against the attribute
labels embedded specifically in the operating certificate, so the bootstrap credential wouldn't
authenticate as anything meaningful to it. Calls `GetPolicies` and writes the returned policy list
to `<var_dir>/policies-cache.json`, atomically (via `common/atomicfile`: temp file + rename).

On any failure (unreachable `policy-server`, RPC error, marshal error), the existing cache file is
left completely untouched — no special-casing between failure kinds; `agent`'s existing backoff
handles all of them identically. Always fetches when invoked; there's no staleness check — run it
on a schedule (`agent`'s `policy-update` policy, or a bare cron/systemd timer) if periodic
refreshing is wanted.

The cache file is a plain JSON array, one object per policy, mirroring the RPC response's fields
directly:

```json
[
  {
    "name": "daily-db-backup",
    "created_at": "2026-07-01T00:00:00Z",
    "updated_at": "2026-07-05T00:00:00Z",
    "object_filters": ["/var/lib/postgres", "/etc/postgres"],
    "rpo": "24h",
    "backup_window": ["0 2 * * *"]
  }
]
```

## Building

```bash
make policyclient
```

## See Also

- [policy-server](./policy-server.md) — what `fetch` dials
- [Policy Server Protocol](../protocols/policy-server.md) — `GetPolicies` RPC details
- [agent](./agent.md) — runs `policy-update` as a scheduled policy
- [certclient](./certclient.md) — the sibling binary `agent` execs for credential refresh
- [Design: Agent Policy-Update Job](../superpowers/specs/2026-07-10-agent-policy-update-job-design.md)
- [Architecture](../ARCHITECTURE.md)
