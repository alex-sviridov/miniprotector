# Design: `running_service=` — agent keeps a node's local services running

Date: 2026-10-10

## Goal

A container or VM runs only `agent`; `agent` keeps the node's own services (e.g. `listener`, `catalog`)
running, restarting them on crash. Services are listed in the node's `local.conf` by repeating a
`running_service=` line. This replaces the control-plane `entrypoint.sh` scripts' ad-hoc `&`/restart-loop
process management, in which a dead `agent`, `listener` or service went unnoticed.

## Config format

```
running_service=<binary> [args...]
```

- The key may repeat; each line is one supervised process.
- `<binary>` is a bare name (no path separator) resolved next to the `agent` executable, as `bwfs` and
  `catalogsync` are (`resolveExecPath`).
- Arguments are split on whitespace. Quote characters (`"` or `'`) anywhere in the value are rejected
  with an error rather than silently mis-split.
- `$VAR`, `${VAR}` and `${VAR:-default}` are expanded from the environment before splitting, so
  container knobs such as `DEBUG` and `STORAGE_PATH` keep working. An expanded value containing spaces
  splits into several arguments.
- Errors (at config parse): empty value, binary with a path separator, quote characters, a binary
  listed twice.
- `config.Config.RunningServices []RunningService` (`{Binary string; Args []string}`), in file order.

## Supervision (`cmd/agent`)

- A dedicated `storageManager` instance supervises the static services. It reuses the generic
  `processSupervisor`: crash restart with backoff (1 s base, 30 s cap — not the 30 s base policy-derived tasks
  use, since a dead `listener` takes the host's whole gRPC surface down), stability window, SIGTERM on stop, children inherit
  `agent`'s stdout/stderr (so `docker logs` shows them).
- It is separate from the policy-derived storage manager because `run()` skips storage reconcile when the
  policy cache is missing or unreadable (`ok == false`). A fresh `policy-server` has to run before any
  policy can be fetched from it, so static services must not depend on the cache.
- Gate: nothing starts until `<certs>/client.crt` exists (the operating certificate; the first
  `operating-refresh` is due immediately, so this takes seconds). `agent` polls for it every 500 ms, logs
  once that it is waiting, then starts every service once. This replaces the entrypoints' 60 s wait loop.
- Shutdown: `agent`'s existing signal handling stops the manager (`StopAll`), so SIGTERM to `agent`
  (PID 1 in a container) stops every child.
- Outcomes are logged by the supervisor (unexpected exits at error level). They are not recorded in
  `agent-state.json` (static services never change at runtime, so there is nothing to reconcile or prune).
- A node with no `running_service` lines behaves exactly as before.

## Containers (`deploy/control-plane`)

- `catalog`, `policy-server`, `api-server`, `clientmanager-api` entrypoints become: `certclient
  bootstrap` (first run, needs `MP_CERT_TOKEN`) or `renew`, then `exec ./agent serve`. Bootstrap stays in
  the entrypoint because `agent` derives its hostname from the bootstrap certificate at startup and the
  token is one-time. Any directory setup the entrypoint already did (e.g. `mkdir -p "$STORAGE_PATH"`) stays.
- Each service's `local.conf` gains its `running_service=` lines (`listener` plus the service;
  `clientmanager-api` has `listener`, `clientmanager-api`, `clientmanager-admin-api` with its CA flags).
- `issuer`, `step-ca` and `bwfs`/`brfs`/`rwfs` nodes are unchanged.

## Testing

- `common/config`: repeated lines in order, env expansion (`$V`, `${V}`, `${V:-d}`), and each error case.
- `cmd/agent`: services do not start before `client.crt` exists and do start after; run with an
  unreadable/missing policy cache; a crashing service is restarted; `StopAll` stops them.
- End to end: `make demo-up` and the e2e suite with all four control-plane containers running under `agent`.

## Documentation

`docs/components/agent.md` (new section), `docs/components/listener.md` (operations wording),
`docs/ARCHITECTURE.md`, `deploy/control-plane/README.md`, `CHANGELOG.md`; amend the supervision bullet of
the listener spec.

## Out of scope

YAML configuration, per-service environment or restart settings, quoted arguments, supervising
`issuer`/`step-ca`, and recording static-service outcomes in `agent-state.json`.
