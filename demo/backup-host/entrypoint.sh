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
# sequence: agent's own operating-refresh always completes before its
# policy-update (same tick), so nothing agent-spawned can ever race agent's
# own cert setup, and bwfs/catalogsync are independent ensure-running tasks
# agent reconciles on its own -- see docs/superpowers/specs/
# 2026-07-31-agent-catalogsync-supervision-design.md.
exec ./agent serve
