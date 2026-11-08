package grpcapi

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// File names inside data/config/grpc. They are the same five files
// back/config/grpcCerts.ts writes, because the JavaScript preload loads
// ca.crt/client.crt/client.key from this directory.
const (
	caCertName     = "ca.crt"
	caKeyName      = "ca.key"
	serverCertName = "server.crt"
	serverKeyName  = "server.key"
	clientCertName = "client.crt"
	clientKeyName  = "client.key"
)

// certValidity is the 10 years grpcCerts.ts passes to `openssl -days`.
const certValidity = 10 * 365 * 24 * time.Hour

// ensureTLS returns a server TLS config backed by the mTLS material in dir,
// generating the whole set when any file is missing or unreadable.
//
// The JavaScript client falls back to createInsecure() when it cannot read the
// certificates, so a broken directory must never stop the panel: the caller
// decides what to do with the error.
func ensureTLS(dir string) (*tls.Config, error) {
	if config, err := loadTLS(dir); err == nil {
		return config, nil
	}
	if err := generateCerts(dir); err != nil {
		return nil, fmt.Errorf("generate gRPC certificates: %w", err)
	}
	return loadTLS(dir)
}

// loadTLS reads ca.crt/server.crt/server.key and verifies the client pair is
// usable too, so a half-written directory is treated as missing.
func loadTLS(dir string) (*tls.Config, error) {
	serverPair, err := tls.LoadX509KeyPair(
		filepath.Join(dir, serverCertName),
		filepath.Join(dir, serverKeyName),
	)
	if err != nil {
		return nil, fmt.Errorf("load server key pair: %w", err)
	}

	caPEM, err := os.ReadFile(filepath.Join(dir, caCertName))
	if err != nil {
		return nil, fmt.Errorf("read ca certificate: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("parse %s: no certificate found", caCertName)
	}

	// The script-side client needs its own pair; regenerate everything when it
	// is absent instead of leaving tasks without a working ql API.
	if _, err := tls.LoadX509KeyPair(
		filepath.Join(dir, clientCertName),
		filepath.Join(dir, clientKeyName),
	); err != nil {
		return nil, fmt.Errorf("load client key pair: %w", err)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{serverPair},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// generateCerts writes a fresh CA, server and client certificate set. The
// parameters mirror grpcCerts.ts: RSA 2048, SAN localhost/127.0.0.1/::1, ten
// years of validity.
func generateCerts(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create certificate directory: %w", err)
	}

	now := time.Now()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("generate CA key: %w", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: newSerial(),
		Subject: pkix.Name{
			CommonName:   "qinglong-ca",
			Organization: []string{"qinglong"},
			Country:      []string{"CN"},
		},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(certValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return fmt.Errorf("create CA certificate: %w", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return fmt.Errorf("parse CA certificate: %w", err)
	}

	serverCertPEM, serverKeyPEM, err := issue(caCert, caKey, "grpc-server", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, now, func(template *x509.Certificate) {
		template.DNSNames = []string{"localhost"}
		template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	})
	if err != nil {
		return err
	}
	clientCertPEM, clientKeyPEM, err := issue(caCert, caKey, "grpc-client", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, now, nil)
	if err != nil {
		return err
	}

	files := []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{caCertName, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o644},
		{caKeyName, encodeKey(caKey), 0o600},
		{serverCertName, serverCertPEM, 0o644},
		{serverKeyName, serverKeyPEM, 0o600},
		{clientCertName, clientCertPEM, 0o644},
		{clientKeyName, clientKeyPEM, 0o600},
	}
	for _, file := range files {
		if err := writeFileAtomic(filepath.Join(dir, file.name), file.data, file.mode); err != nil {
			return err
		}
	}
	return nil
}

// issue signs one leaf certificate with the CA and returns its PEM pair.
func issue(ca *x509.Certificate, caKey *rsa.PrivateKey, commonName string, usages []x509.ExtKeyUsage, now time.Time, customize func(*x509.Certificate)) ([]byte, []byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, fmt.Errorf("generate %s key: %w", commonName, err)
	}
	template := &x509.Certificate{
		SerialNumber:          newSerial(),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(certValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           usages,
		BasicConstraintsValid: true,
	}
	if customize != nil {
		customize(template)
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create %s certificate: %w", commonName, err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), encodeKey(key), nil
}

// encodeKey emits PKCS#1 like `openssl genrsa`, which is what the Node panel
// writes and what @grpc/grpc-js expects.
func encodeKey(key *rsa.PrivateKey) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

func newSerial() *big.Int {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return big.NewInt(time.Now().UnixNano())
	}
	return serial
}

// writeFileAtomic replaces path in one step so a client reading the directory
// while the panel boots never sees a truncated certificate.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".tmp-cert-*")
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", path, err)
	}
	name := temp.Name()
	defer os.Remove(name)
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
