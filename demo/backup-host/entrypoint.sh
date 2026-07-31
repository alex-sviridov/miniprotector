#!/bin/sh
set -e

# One-time bootstrap (first run, needs MP_CERT_TOKEN) or renew (every
# subsequent restart) of the long-lived bootstrap credential -- same
# pattern as deploy/control-plane/catalog/entrypoint.sh.
if [ -f /data/certs/bootstrap.crt ]; then
    ./certclient renew
else
    ./certclient bootstrap --token "$MP_CERT_TOKEN"
fi

# agent owns everything from here: its own cert renewal, and -- for a node
# targeted by a "storage" policy (see demo/policy-server/policies/storage/)
# -- starting and supervising bwfs and catalogsync itself once its reconcile
# loop picks that policy up. There's nothing left for this script to
# sequence: a storage task can only ever exist in policies-cache.json after
# a policy-update succeeded, which itself requires a prior successful
# operating-refresh to have produced client.crt -- so nothing agent-spawned
# can start before agent's own cert setup has already happened at least once
# (on a warm restart with a persisted cache, that earlier success may be
# from a previous run, not this tick's). bwfs/catalogsync are independent
# ensure-running tasks agent reconciles on its own -- see docs/superpowers/specs/
# 2026-07-31-agent-catalogsync-supervision-design.md.
exec ./agent serve
