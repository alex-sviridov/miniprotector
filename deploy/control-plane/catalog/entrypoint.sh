#!/bin/sh
set -e

mkdir -p "$STORAGE_PATH"

# Bootstraps a new mTLS identity on first run (requires MP_CERT_TOKEN), or
# renews the existing one on every subsequent container restart — no
# expiry check, so certclient always renews when an identity is already
# present. Picking up a renewal made independently while the container
# keeps running (e.g. a scheduled certclient run against the same
# MP_CONFIG_PATH) doesn't require this step to run again — see the
# certificate hot-reload fix in common/mtls.
./certclient

exec ./catalog "$STORAGE_PATH" --debug="${DEBUG:-false}"
