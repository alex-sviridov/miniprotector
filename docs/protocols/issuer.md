# Issuer Protocol

Already-bootstrapped node (authenticated with its long-lived bootstrap credential,
`bootstrap.crt`/`bootstrap.key`) → `issuer`'s `RequestOperatingCert` RPC, mTLS
(`common/mtls`, same transport every other gRPC call in this project uses). `certclient
operating-refresh` is the sole client of this protocol; see [certclient](../components/certclient.md).

## RPC

```proto
service IssuerService {
  rpc RequestOperatingCert(RequestOperatingCertRequest) returns (RequestOperatingCertResponse);
}

message RequestOperatingCertRequest {
  bytes csr_der = 1;
}

message RequestOperatingCertResponse {
  bytes cert_chain_pem = 1;
}

```

## Authorization

The caller's hostname is always derived from its verified mTLS peer identity (`mtls.PeerHostname`)
— never a field on the request. `issuer` looks that hostname up in the same database
`client-manager` writes to: unknown or revoked hostnames are refused outright.

Beneath this RPC-level check, the transport itself now also enforces credential tier: `issuer`'s
listener (`mtls.LoadIssuerServerCredentials`) accepts only bootstrap/issuer-caller certificates,
rejecting an operating certificate before any RPC-level logic runs. See
[Security Model](../SECURITY.md#the-two-tier-credential-model).

## Behavior

- `csr_der` is a DER-encoded PKCS#10 certificate signing request the caller builds itself — its
  private key never leaves the caller. It must name only the caller's own hostname: `CommonName`
  and a single `DNSNames` entry, both the hostname. Any SAN aliases are `issuer`'s decision, not the
  node's (see "Where SANs come from" below).
- `cert_chain_pem` is the full certificate chain (leaf + any intermediates), PEM-encoded and
  concatenated in order, ready to write directly to `client.crt`.
- The issued certificate's validity is requested per `OperatingCertTTLSec` (`local.conf`), bounded
  by the provisioner's own claims on the CA side.
- `issuer` passes the hostname's SAN list (hostname first, then its aliases) and its current
  `attribute` key/value pairs as the sign request's `TemplateData`. step-ca treats that field as
  caller input, so this is safe only because tokens for the operating provisioner are minted by
  `issuer` alone — see [Security Model](../SECURITY.md#why-two-provisioners).

## Where SANs come from

step-ca's OTT/JWK provisioner validates a CSR's requested DNS SANs against the signing token's
authorized set with an **exact match** (`smallstep/certificates@v0.30.2`,
`authority/provisioner/sign_options.go` `dnsNamesValidator`). `issuer` therefore mints the token for
the hostname alone, and the node's CSR names only itself, so the check always passes. The
certificate's real SAN list comes from `operating.tpl`, which reads it from `issuer`'s
`TemplateData` and ignores the CSR's SANs. A node never needs to learn its own aliases, and an
alias change reaches it on its next refresh.

## See Also

- [issuer](../components/issuer.md)
- [certclient](../components/certclient.md) — `operating-refresh` subcommand is the client of this protocol
- [client-manager](../components/client-manager.md)
- [Security Model](../SECURITY.md)
- [Design: Client Manager Phase 2](../superpowers/specs/2026-07-04-client-manager-phase2-design.md)
