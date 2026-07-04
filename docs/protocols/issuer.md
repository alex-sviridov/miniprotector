# Issuer Protocol

Already-bootstrapped node → `issuer`'s `RequestOperatingCert` RPC, mTLS (`common/mtls`, same
transport every other gRPC call in this project uses).

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

## Behavior

- `csr_der` is a DER-encoded PKCS#10 certificate signing request the caller builds itself — its
  private key never leaves the caller.
- `cert_chain_pem` is the full certificate chain (leaf + any intermediates), PEM-encoded and
  concatenated in order, ready to write directly to `client.crt`.
- The issued certificate's validity is requested per `OperatingCertTTLSec` (`local.conf`), bounded
  by the provisioner's own claims on the CA side.
- Current `attribute` key/value pairs for the hostname are passed as the sign request's
  `TemplateData` — any OTT holder may set this field on step-ca's stock `/1.0/sign` (no extra
  permission gate), and it is merged into `.Insecure.User` for a custom certificate template to
  read, if one is configured.

## See Also

- [issuer](../components/issuer.md)
- [client-manager](../components/client-manager.md)
- [Design: Client Manager Phase 2](../superpowers/specs/2026-07-04-client-manager-phase2-design.md)
