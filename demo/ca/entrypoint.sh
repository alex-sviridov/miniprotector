#!/bin/sh
set -e

# Demo is fully self-contained (no host bind-mounts of secrets), so unlike
# deploy/control-plane's Makefile-driven host-side password generation, the
# provisioner password is generated into the ca-data volume on first boot.
# head/base64 are confirmed present in the smallstep/step-ca Alpine base
# (openssl the CLI is not guaranteed to be).
mkdir -p /home/step/secrets
if [ ! -f /home/step/secrets/password ]; then
    head -c32 /dev/urandom | base64 > /home/step/secrets/password
fi

if [ ! -f /home/step/config/ca.json ]; then
    step ca init --deployment-type=standalone \
        --name="Miniprotector Demo CA" \
        --dns="ca,localhost" \
        --address=":9000" \
        --provisioner="admin@backup.internal" \
        --password-file=/home/step/secrets/password
fi

# Runs unconditionally, every boot (not just first init) -- an
# already-initialized CA must still pick up template changes on upgrade,
# the exact gap b212082 fixed for deploy/control-plane's own entrypoint.
# Two provisioners, because step-ca trusts the caller's templateData (it is
# exposed to templates as .Insecure.User):
#   admin@backup.internal     enrollment tokens (client-manager); bootstrap.tpl
#                             ignores caller data, so a token holder can only
#                             ever obtain a bootstrap-tier certificate.
#   operating@backup.internal tokens minted only by issuer, which alone holds
#                             operating_password; operating.tpl embeds the
#                             attributes issuer supplies.
# Re-applied on every boot so a restarted CA keeps the templates in sync.
if [ ! -f /home/step/secrets/operating_password ]; then
  head -c32 /dev/urandom | base64 > /home/step/secrets/operating_password
fi
if ! grep -q '"name": "operating@backup.internal"' /home/step/config/ca.json; then
  step ca provisioner add operating@backup.internal --type=JWK --create --password-file=/home/step/secrets/operating_password
fi
step ca provisioner update admin@backup.internal --x509-template=/home/step/templates/bootstrap.tpl --x509-max-dur=2200h
step ca provisioner update operating@backup.internal --x509-template=/home/step/templates/operating.tpl --x509-max-dur=24h

exec step-ca /home/step/config/ca.json --password-file=/home/step/secrets/password
