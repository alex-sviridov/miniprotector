# Enterprise Backup Cluster CA

A `step-ca` instance issuing mTLS identities for miniprotector nodes.

## First-time setup

Generate the provisioner password once, before the first `docker compose up` (it can't be
automated away without either committing a secret or inventing a new secret-distribution
mechanism):

```bash
mkdir -p data/secrets
openssl rand -base64 32 > data/secrets/password
```

## Running

```bash
docker compose up -d
```

Idempotent: the entrypoint only runs `step ca init` if `data/config/ca.json` doesn't already
exist, so re-running `docker compose up -d` after the first time just (re)starts the server.

## Enrolling a node

On (or near) the CA host, using the `ca/data/certs/root_ca.crt` and `ca/data/secrets/password`
this compose setup produces:

```bash
certrequest node-east-01 --san node-east-01.internal --ca-url https://localhost:9000
```

This prints a one-time enrollment token. Relay it to the target node out-of-band (SSH, etc.) as
the `MP_CERT_TOKEN` environment variable, then on that node:

```bash
MP_CERT_TOKEN=<token> certclient
```

Re-running `certclient` on a node that already has an identity renews it instead (no token
needed — renewal is authenticated with the existing certificate).

## Viewing an issued certificate

```bash
openssl x509 -in <certs-dir>/client.crt -text -noout
```

## See Also

- [certrequest](../docs/components/certrequest.md)
- [certclient](../docs/components/certclient.md)
- [Architecture](../docs/ARCHITECTURE.md)
