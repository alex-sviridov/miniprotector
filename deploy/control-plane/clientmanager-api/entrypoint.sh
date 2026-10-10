#!/bin/sh
set -e

# One-time bootstrap (first run, needs MP_CERT_TOKEN) or renew (every
# subsequent restart -- no expiry check, certclient always renews when an
# identity already exists) of the long-lived bootstrap credential. This stays
# here, not in agent: agent derives its hostname from the bootstrap credential
# at startup, and the token is one-time.
if [ -f /data/certs/bootstrap.crt ]; then
	./certclient renew
else
	./certclient bootstrap --token "$MP_CERT_TOKEN"
fi

# agent is this container's only process: it keeps both credentials fresh
# (bootstrap daily, operating every 15 min via issuer) and, once the first
# operating certificate exists, starts and supervises the processes listed as
# running_service= in local.conf (the listener and this node's service), so a
# crash is restarted and a dead agent restarts the container.
exec ./agent serve --debug="${DEBUG:-false}"
