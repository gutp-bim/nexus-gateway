// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package mqtt

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeSelfSigned writes a self-signed cert + key PEM pair to dir and returns their paths.
func writeSelfSigned(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "gw-test"},
		NotBefore:    time.Unix(1_600_000_000, 0),
		NotAfter:     time.Unix(1_900_000_000, 0),
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	require.NoError(t, err)

	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	require.NoError(t, os.WriteFile(certPath, certPEM, 0o600))
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	require.NoError(t, os.WriteFile(keyPath, keyPEM, 0o600))
	return certPath, keyPath
}

func TestBuildTLSConfig_EmptyIsSecureDefault(t *testing.T) {
	cfg, err := buildTLSConfig(Config{})
	require.NoError(t, err)
	assert.EqualValues(t, tls.VersionTLS12, cfg.MinVersion)
	assert.False(t, cfg.InsecureSkipVerify)
	assert.Nil(t, cfg.RootCAs, "no CA file → system roots (nil pool)")
	assert.Empty(t, cfg.Certificates)
}

func TestBuildTLSConfig_InsecureSkipVerify(t *testing.T) {
	cfg, err := buildTLSConfig(Config{TLSInsecureSkipVerify: true})
	require.NoError(t, err)
	assert.True(t, cfg.InsecureSkipVerify)
}

func TestBuildTLSConfig_CABundle(t *testing.T) {
	dir := t.TempDir()
	caPath, _ := writeSelfSigned(t, dir)
	cfg, err := buildTLSConfig(Config{TLSCAFile: caPath})
	require.NoError(t, err)
	require.NotNil(t, cfg.RootCAs)
}

func TestBuildTLSConfig_ClientCertPair(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeSelfSigned(t, dir)
	cfg, err := buildTLSConfig(Config{TLSCertFile: certPath, TLSKeyFile: keyPath})
	require.NoError(t, err)
	require.Len(t, cfg.Certificates, 1)
}

func TestBuildTLSConfig_HalfCertPairIsNamedError(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeSelfSigned(t, dir)

	_, err := buildTLSConfig(Config{TLSCertFile: certPath})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TLS_KEY_FILE")

	_, err = buildTLSConfig(Config{TLSKeyFile: keyPath})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TLS_CERT_FILE")
}

func TestBuildTLSConfig_BadCAFileErrors(t *testing.T) {
	_, err := buildTLSConfig(Config{TLSCAFile: "/no/such/ca.pem"})
	require.Error(t, err)

	dir := t.TempDir()
	junk := filepath.Join(dir, "junk.pem")
	require.NoError(t, os.WriteFile(junk, []byte("not a pem"), 0o600))
	_, err = buildTLSConfig(Config{TLSCAFile: junk})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CA")
}
