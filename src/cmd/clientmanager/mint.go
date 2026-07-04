// client-manager mints enrollment tokens directly, in-process, using the
// same common/certmint package certrequest used to. This replaces the
// certrequest-serve broker from phase 1 -- see
// docs/superpowers/specs/2026-07-04-client-manager-phase2-design.md for
// why that's safe now that client-manager runs on the CA host directly,
// rather than a separate, less-trusted one.
package main

import "github.com/alex-sviridov/miniprotector/common/certmint"

// minter mints an enrollment token for hostname/sans using the given
// provisioner credentials. certmint.Mint's own signature already matches
// this exactly, so production code passes it directly with no wrapper;
// tests inject a stub.
type minter func(hostname string, sans []string, opts certmint.Options) (string, error)
