# Miniprotector — Claude Instructions

## Documentation Rules

### gRPC Protocol Changes

Before committing any new or modified gRPC protocol (new `.proto` file, changes to an existing `.proto` file, or regenerated `*_grpc.pb.go` / `*.pb.go` files):

- Create or update the corresponding file in `docs/protocols/` to reflect the current proto definition, message fields, filter semantics, CLI→RPC mapping, and design decisions.
- Cross-link the protocol doc from `README.md` (Documentation section) and from the relevant `docs/components/` files (See Also section).

### Feature Changes

Before committing any feature change (new command, new flag, changed behavior, new component):

- Update `docs/components/<component>.md` for each affected component.
- Update `README.md` if the change affects the quick-start examples, component list, or documentation index.
- Update `docs/ARCHITECTURE.md` if the change affects the system topology, data flow, or mermaid diagram.

### Changelog

Before merging any feature branch to `main`, add an entry to `CHANGELOG.md` (most recent first):
a dated heading and a short paragraph summarizing what changed and why — not a file-by-file diff.
