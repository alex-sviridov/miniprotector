#!/bin/sh
set -e

if [ -f /data/certs/bootstrap.crt ]; then
	./certclient renew
else
	./certclient bootstrap --token "$MP_CERT_TOKEN"
fi

./agent serve &

timeout=60
while [ ! -f /data/certs/client.crt ] && [ "$timeout" -gt 0 ]; do
	sleep 1
	timeout=$((timeout - 1))
done
if [ ! -f /data/certs/client.crt ]; then
	echo "agent did not produce an operating certificate within 60s" >&2
	exit 1
fi

# listener is this host's only control-plane gRPC port (:9300); the service
# below listens on an internal socket. Restart-looped so a crash cannot strand
# the host (the service itself is exec'd/backgrounded, so nothing else would
# notice).
( while true; do ./listener --debug="${DEBUG:-false}" || true; sleep 1; done ) &

exec ./api-server --debug="${DEBUG:-false}"
