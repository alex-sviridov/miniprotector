#!/bin/sh
set -e
if [ ! -f /home/step/config/ca.json ]; then
  step ca init --deployment-type=standalone \
    --name="Enterprise Backup Cluster CA" \
    --dns="ca.backup.internal,localhost,step-ca" \
    --address=":9000" \
    --provisioner="admin@backup.internal" \
    --password-file=/home/step/secrets/password
  step ca provisioner update admin@backup.internal --x509-template=/home/step/templates/leaf.tpl
fi
exec step-ca /home/step/config/ca.json --password-file=/home/step/secrets/password
