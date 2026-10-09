// issuer's real certificate issuance: mint a one-time token via the same
// certmint package client-manager uses, then sign the caller's own CSR
// directly against the CA -- never generating a keypair here, since the
// private key must never leave the node that requested it.
package main

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"time"

	"github.com/smallstep/certificates/api"
	"github.com/smallstep/certificates/ca"

	"github.com/alex-sviridov/miniprotector/common/certmint"
)

func mintAndSign(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest, opts certmint.Options, ttlSec int) ([]byte, error) {
	// The token authorizes the hostname only: step-ca requires the CSR's SANs to
	// match it exactly, and the node's CSR names only itself. The full SAN list
	// is set below through the operating template, which only issuer can reach.
	token, err := certmint.Mint(hostname, nil, opts)
	if err != nil {
		return nil, fmt.Errorf("mint token: %w", err)
	}

	type san struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	}
	allSANs := []san{{"dns", hostname}}
	for _, alias := range sans {
		allSANs = append(allSANs, san{"dns", alias})
	}
	templateData, err := json.Marshal(struct {
		SANs       []san             `json:"sans"`
		Attributes map[string]string `json:"attributes,omitempty"`
	}{
		SANs:       allSANs,
		Attributes: attributes,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal template data: %w", err)
	}

	client, err := ca.NewClient(opts.CAURL, ca.WithRootFile(opts.RootFile))
	if err != nil {
		return nil, fmt.Errorf("create CA client: %w", err)
	}

	notAfter := api.NewTimeDuration(time.Now().Add(time.Duration(ttlSec) * time.Second))

	signResp, err := client.Sign(&api.SignRequest{
		CsrPEM:       api.NewCertificateRequest(csr),
		OTT:          token,
		TemplateData: templateData,
		NotAfter:     notAfter,
	})
	if err != nil {
		return nil, fmt.Errorf("sign certificate: %w", err)
	}

	// signResp.CertChainPEM already starts with the leaf (it's literally
	// signResp.ServerPEM, see smallstep/certificates api.SignResponse) --
	// prepending ServerPEM separately would duplicate the leaf as the
	// chain's second entry, which grpc-go's TLS stack silently tolerates
	// but a strict net/http.Server client-cert verifier (log-gateway)
	// rejects outright with "certificate signed by unknown authority".
	var chainPEM []byte
	for _, c := range signResp.CertChainPEM {
		chainPEM = append(chainPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})...)
	}
	return chainPEM, nil
}
