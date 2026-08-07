package mqtt

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// LoadTLSConfig loads optional custom roots and an optional mTLS client
// certificate. Client certificate and key must always be configured together.
func LoadTLSConfig(caFile, certFile, keyFile string) (*tls.Config, error) {
	if (certFile == "") != (keyFile == "") {
		return nil, fmt.Errorf("mqtt TLS: certificate and private key must be configured together")
	}

	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("mqtt TLS: read CA: %w", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("mqtt TLS: CA file contains no certificates")
		}
		cfg.RootCAs = roots
	}
	if certFile != "" {
		certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("mqtt TLS: load client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{certificate}
	}
	return cfg, nil
}
