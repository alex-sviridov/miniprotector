# Issuer Protocol

Already-bootstrapped node (authenticated with its long-lived bootstrap credential,
`bootstrap.crt`/`bootstrap.key`) → `issuer`'s `RequestOperatingCert` and `DescribeSANs` RPCs, mTLS
(`common/mtls`, same transport every other gRPC call in this project uses). `certclient
operating-refresh` is the sole client of this protocol; see [certclient](../components/certclient.md).

## RPC

```proto
service IssuerService {
  rpc RequestOperatingCert(RequestOperatingCertRequest) returns (RequestOperatingCertResponse);
  rpc DescribeSANs(DescribeSANsRequest) returns (DescribeSANsResponse);
}

message RequestOperatingCertRequest {
  bytes csr_der = 1;
}

message RequestOperatingCertResponse {
  bytes cert_chain_pem = 1;
}

message DescribeSANsRequest {}

message DescribeSANsResponse {
  repeated string sans = 1;
}
```

## Authorization

The caller's hostname is always derived from its verified mTLS peer identity (`mtls.PeerHostname`)
— never a field on the request. `issuer` looks that hostname up in the same database
`client-manager` writes to: for `RequestOperatingCert`, unknown or revoked hostnames are refused
outright. `DescribeSANs` only requires a known hostname — it has no revoked check, since it reveals
nothing the caller isn't already entitled to know about itself and mints/signs nothing.

Beneath this RPC-level check, the transport itself now also enforces credential tier: `issuer`'s
listener (`mtls.LoadIssuerServerCredentials`) accepts only bootstrap/issuer-caller certificates,
rejecting an operating certificate before any RPC-level logic runs. See
[Security Model](../SECURITY.md#the-two-tier-credential-model).

## Behavior

- `csr_der` is a DER-encoded PKCS#10 certificate signing request the caller builds itself — its
  private key never leaves the caller. Its `DNSNames` must be exactly `[hostname] + sans` (see
  "Why `DescribeSANs` exists" below) — a mismatch causes the CA to reject the request outright.
- `cert_chain_pem` is the full certificate chain (leaf + any intermediates), PEM-encoded and
  concatenated in order, ready to write directly to `client.crt`.
- The issued certificate's validity is requested per `OperatingCertTTLSec` (`local.conf`), bounded
  by the provisioner's own claims on the CA side.
- Current `attribute` key/value pairs for the hostname are passed as the sign request's
  `TemplateData` — any OTT holder may set this field on step-ca's stock `/1.0/sign` (no extra
  permission gate), and it is merged into `.Insecure.User` for a custom certificate template to
  read, if one is configured.
- `DescribeSANsResponse.sans` is the caller's own current SAN alias list, read live from the same
  database `RequestOperatingCert` consults — never including the hostname itself, which the caller
  always supplies separately as the CSR's `Subject.CommonName` and as the first `DNSNames` entry.

## Why `DescribeSANs` exists

step-ca's OTT/JWK provisioner validates a presented CSR's requested DNS SANs against the signing
token's authorized set with an **exact match**, not a subset (confirmed against
`smallstep/certificates@v0.30.2`'s `authority/provisioner/sign_options.go` `dnsNamesValidator`,
enforced in `authority/tls.go`). A CSR with no DNSNames is silently accepted but yields a SAN-less
certificate; a CSR with the wrong DNSNames is rejected outright. Since only `client-manager`'s
database (which the calling node cannot read) knows a hostname's current SAN alias list,
`certclient operating-refresh` calls `DescribeSANs` first and uses its result verbatim as the CSR's
`DNSNames`, before calling `RequestOperatingCert`.

## See Also

- [issuer](../components/issuer.md)
- [certclient](../components/certclient.md) — `operating-refresh` subcommand is the client of this protocol
- [client-manager](../components/client-manager.md)
- [Security Model](../SECURITY.md)
- [Design: Client Manager Phase 2](../superpowers/specs/2026-07-04-client-manager-phase2-design.md)
