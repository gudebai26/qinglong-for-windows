package grpcapi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"qinglong-go/internal/service"
)

func TestCronItemNormalization(t *testing.T) {
	empty := ""
	zero := int64(0)
	pid := int64(0)

	item := cronItem(&service.CrontabResponse{
		ID:      7,
		Command: "task x.js",
		Schedule: func() *string {
			value := "0 0 1 * *"
			return &value
		}(),
		Labels:         nil,
		ExtraSchedules: nil,
		SubID:          &zero,
		Pid:            &pid,
		TaskBefore:     &empty,
		TaskAfter:      nil,
	})

	if item.Id == nil || *item.Id != 7 {
		t.Fatalf("id = %v", item.Id)
	}
	if item.Labels == nil || len(item.Labels) != 0 {
		t.Fatalf("labels must be an empty slice, got %#v", item.Labels)
	}
	if item.ExtraSchedules == nil || len(item.ExtraSchedules) != 0 {
		t.Fatalf("extra_schedules must be an empty slice, got %#v", item.ExtraSchedules)
	}
	if item.SubId != nil {
		t.Fatalf("sub_id 0 must be dropped, got %v", *item.SubId)
	}
	if item.Pid != nil {
		t.Fatalf("pid 0 must be dropped, got %v", *item.Pid)
	}
	if item.TaskBefore != nil {
		t.Fatalf("empty task_before must be dropped, got %q", *item.TaskBefore)
	}
	if item.TaskAfter != nil {
		t.Fatalf("nil task_after must stay unset, got %q", *item.TaskAfter)
	}
}

func TestCronItemKeepsLabelsAndExtraSchedules(t *testing.T) {
	item := cronItem(&service.CrontabResponse{
		ID:      1,
		Command: "task x.js",
		Labels:  []any{"a", "b"},
		ExtraSchedules: []any{
			map[string]any{"schedule": "1 2 * * *"},
			map[string]any{"nope": "ignored"},
		},
	})
	if len(item.Labels) != 2 || item.Labels[0] != "a" {
		t.Fatalf("labels = %#v", item.Labels)
	}
	if len(item.ExtraSchedules) != 1 || item.ExtraSchedules[0].GetSchedule() != "1 2 * * *" {
		t.Fatalf("extra_schedules = %#v", item.ExtraSchedules)
	}
}

func TestEnvItemRemarksFallback(t *testing.T) {
	item := envItem(service.EnvResponse{ID: 3}, true)
	if item.Remarks == nil || *item.Remarks != "" {
		t.Fatalf("remarks fallback = %v", item.Remarks)
	}
	raw := envItem(service.EnvResponse{ID: 3}, false)
	if raw.Remarks != nil {
		t.Fatalf("createEnv must keep a null remark, got %q", *raw.Remarks)
	}
}

func TestGenerateCertsProducesUsableMTLSMaterial(t *testing.T) {
	dir := t.TempDir()
	if err := generateCerts(dir); err != nil {
		t.Fatalf("generateCerts: %v", err)
	}
	for _, name := range []string{caCertName, caKeyName, serverCertName, serverKeyName, clientCertName, clientKeyName} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}

	config, err := loadTLS(dir)
	if err != nil {
		t.Fatalf("loadTLS: %v", err)
	}
	if config.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatalf("client auth = %v", config.ClientAuth)
	}

	serverPEM, err := os.ReadFile(filepath.Join(dir, serverCertName))
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(serverPEM)
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if certificate.Subject.CommonName != "grpc-server" {
		t.Fatalf("CN = %q", certificate.Subject.CommonName)
	}
	if days := certificate.NotAfter.Sub(certificate.NotBefore).Hours() / 24; days < 3600 {
		t.Fatalf("validity = %.0f days, want about 10 years", days)
	}
	names := certificate.DNSNames
	if len(names) != 1 || names[0] != "localhost" {
		t.Fatalf("DNS SANs = %v", names)
	}
	ips := []string{}
	for _, ip := range certificate.IPAddresses {
		ips = append(ips, ip.String())
	}
	if len(ips) != 2 || ips[0] != "127.0.0.1" || ips[1] != "::1" {
		t.Fatalf("IP SANs = %v", ips)
	}

	// The client pair must be signed by the same CA, or mTLS would fail.
	clientPEM, err := os.ReadFile(filepath.Join(dir, clientCertName))
	if err != nil {
		t.Fatal(err)
	}
	clientBlock, _ := pem.Decode(clientPEM)
	clientCert, err := x509.ParseCertificate(clientBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	caPEM, err := os.ReadFile(filepath.Join(dir, caCertName))
	if err != nil {
		t.Fatal(err)
	}
	pool.AppendCertsFromPEM(caPEM)
	if _, err := clientCert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Fatalf("client certificate does not verify: %v", err)
	}
}

func TestEnsureTLSRegeneratesBrokenMaterial(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A truncated certificate must be treated as missing, not as fatal.
	if err := os.WriteFile(filepath.Join(dir, serverCertName), []byte("broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	config, err := ensureTLS(dir)
	if err != nil {
		t.Fatalf("ensureTLS: %v", err)
	}
	if len(config.Certificates) != 1 {
		t.Fatalf("certificates = %d", len(config.Certificates))
	}
}

func TestIsLoopbackHost(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1": true,
		"localhost": true,
		"::1":       true,
		"[::1]":     true,
		"0.0.0.0":   false,
		"::":        false,
		"10.0.0.5":  false,
	}
	for host, want := range cases {
		if got := isLoopbackHost(host); got != want {
			t.Fatalf("isLoopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestServerStartAndGracefulStop(t *testing.T) {
	dir := t.TempDir()
	// NewServer treats 0 as "use the default port", so grab a free one first.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

	server := NewServer(Options{
		Port:     port,
		BindHost: "127.0.0.1",
		CertDir:  filepath.Join(dir, "grpc"),
	})
	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if server.Addr() == "" {
		t.Fatal("server did not report an address")
	}
	if !server.usingTLS() {
		t.Fatal("expected mTLS credentials")
	}
	server.Stop(2 * time.Second)
	server.Stop(2 * time.Second) // second stop must be a no-op
}
