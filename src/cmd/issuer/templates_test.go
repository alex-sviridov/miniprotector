package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.step.sm/crypto/x509util"
)

const (
	templatesDir  = "../../../deploy/control-plane/ca/templates"
	issuerCallerO = "1.3.6.1.4.1.61183.1.3"
	attributeO    = "1.3.6.1.4.1.61183.1.1"
)

// renderTemplate renders a CA template the way step-ca does for a /sign
// request: the request's templateData is exposed as .Insecure.User and is
// entirely caller-controlled.
func renderTemplate(t *testing.T, file string, userData map[string]any) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: "node1"},
		DNSNames: []string{"node1"},
	}, key)
	require.NoError(t, err)
	csr, err := x509.ParseCertificateRequest(der)
	require.NoError(t, err)

	data := x509util.CreateTemplateData("node1", []string{"node1"})
	data.SetCertificateRequest(csr)
	if userData != nil {
		data.SetUserData(userData)
	}
	crt, err := x509util.NewCertificate(csr, x509util.WithTemplateFile(filepath.Join(templatesDir, file), data))
	require.NoError(t, err)
	return crt.GetCertificate()
}

func hasUnknownEKU(c *x509.Certificate, oid string) bool {
	for _, o := range c.UnknownExtKeyUsage {
		if o.String() == oid {
			return true
		}
	}
	return false
}

func hasExtension(c *x509.Certificate, oid string) bool {
	for _, e := range c.ExtraExtensions {
		if e.Id.String() == oid {
			return true
		}
	}
	return false
}

// A holder of a bootstrap enrollment token calls step-ca's /sign directly
// and chooses the templateData. Whatever they send, the bootstrap
// provisioner's template must still emit a bootstrap-tier certificate with
// no attributes.
func TestBootstrapTemplateIgnoresForgedTemplateData(t *testing.T) {
	forged := map[string]any{
		"tier":       "operating",
		"attributes": map[string]string{"authz-role": "control-plane"},
	}
	for name, ud := range map[string]map[string]any{"forged": forged, "none": nil} {
		t.Run(name, func(t *testing.T) {
			c := renderTemplate(t, "bootstrap.tpl", ud)
			assert.True(t, hasUnknownEKU(c, issuerCallerO), "must carry the issuer-caller EKU")
			assert.Equal(t, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, c.ExtKeyUsage)
			assert.False(t, hasExtension(c, attributeO), "must never embed attributes")
		})
	}
}

// The operating template is only reachable with a token minted by issuer, so
// it does embed attributes -- and never the bootstrap marker.
func TestOperatingTemplateEmbedsAttributes(t *testing.T) {
	c := renderTemplate(t, "operating.tpl", map[string]any{
		"attributes": map[string]string{"authz-role": "store"},
	})
	assert.False(t, hasUnknownEKU(c, issuerCallerO))
	assert.ElementsMatch(t, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, c.ExtKeyUsage)
	assert.True(t, hasExtension(c, attributeO))
}
