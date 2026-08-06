package mqtt

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadTLSConfig_RequiresCertificateAndKeyTogether(t *testing.T) {
	_, err := LoadTLSConfig("", "certificate.pem", "")
	assert.Error(t, err)
	_, err = LoadTLSConfig("", "", "private.key")
	assert.Error(t, err)
}

func TestLoadTLSConfig_ExternalCertificatePair(t *testing.T) {
	certFile := os.Getenv("MQTT_TEST_CERT_FILE")
	keyFile := os.Getenv("MQTT_TEST_KEY_FILE")
	if certFile == "" || keyFile == "" {
		t.Skip("set MQTT_TEST_CERT_FILE and MQTT_TEST_KEY_FILE to validate an external pair")
	}
	cfg, err := LoadTLSConfig("", certFile, keyFile)
	require.NoError(t, err)
	require.Len(t, cfg.Certificates, 1)
	require.NotEmpty(t, cfg.Certificates[0].Certificate)
	leaf, err := x509.ParseCertificate(cfg.Certificates[0].Certificate[0])
	require.NoError(t, err)
	assert.True(t, time.Now().Before(leaf.NotAfter), "client certificate is expired")
	assert.True(t, time.Now().After(leaf.NotBefore), "client certificate is not valid yet")
}

func TestLoadTLSConfig_RejectsInvalidCA(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(path, []byte("not a certificate"), 0o600))
	_, err := LoadTLSConfig(path, "", "")
	assert.Error(t, err)
}

func TestLoadTLSConfig_SystemRootsWithoutClientCertificate(t *testing.T) {
	cfg, err := LoadTLSConfig("", "", "")
	require.NoError(t, err)
	assert.NotNil(t, cfg)
	assert.Equal(t, uint16(0x0303), cfg.MinVersion) // TLS 1.2
}
