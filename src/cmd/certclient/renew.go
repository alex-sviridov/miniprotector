package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/smallstep/certificates/api"
	"github.com/smallstep/certificates/ca"
)

// renewer is satisfied by *ca.Client. Isolating it lets renew be unit
// tested without a live CA connection.
type renewer interface {
	Renew(tr http.RoundTripper) (*api.SignResponse, error)
}

// renew re-authenticates with the existing identity in certsDir and
// overwrites client.crt with a freshly renewed certificate for the same
// key pair. ca.crt and client.key are left untouched — step-ca's renewal
// semantics re-sign the same key, and root rotation is out of scope here
// (a fresh bootstrap handles that rare case).
func renew(client renewer, certsDir string) error {
	cert, err := tls.LoadX509KeyPair(
		filepath.Join(certsDir, "client.crt"),
		filepath.Join(certsDir, "client.key"),
	)
	if err != nil {
		return fmt.Errorf("load existing identity: %w", err)
	}

	caPEM, err := os.ReadFile(filepath.Join(certsDir, "ca.crt"))
	if err != nil {
		return fmt.Errorf("read ca.crt: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return fmt.Errorf("parse ca.crt: no valid certificates found")
	}

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			Certificates: []tls.Certificate{cert},
			RootCAs:      pool,
		},
	}

	sign, err := client.Renew(tr)
	if err != nil {
		return fmt.Errorf("renew request: %w", err)
	}

	return writeRenewedCert(certsDir, sign)
}

func writeRenewedCert(certsDir string, sign *api.SignResponse) error {
	leaf, err := ca.Certificate(sign)
	if err != nil {
		return fmt.Errorf("extract leaf certificate: %w", err)
	}
	intermediate, err := ca.IntermediateCertificate(sign)
	if err != nil {
		return fmt.Errorf("extract intermediate certificate: %w", err)
	}

	chain := append(
		pemCert(leaf),
		pemCert(intermediate)...,
	)
	if err := os.WriteFile(filepath.Join(certsDir, "client.crt"), chain, 0o644); err != nil {
		return fmt.Errorf("write client.crt: %w", err)
	}
	return nil
}
