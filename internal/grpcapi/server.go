// Package grpcapi serves the panel's ql API over gRPC.
//
// The JavaScript preload (shell/preload/client.js) talks to this server for
// every QLAPI.* call a task makes, so the schema, the error conventions and
// even the "code in the response body" style have to match
// back/schedule/api.ts exactly. The protobuf definitions are the Node panel's
// own back/protos/api.proto, copied verbatim into proto/ and generated into
// this package by buf.
package grpcapi

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"qinglong-go/internal/service"
)

// Defaults match the Node panel except for the bind host: the Node server
// listens on "::" (every interface) while the Go panel keeps the ql API on the
// loopback interface unless BIND_HOST_GRPC says otherwise.
const (
	DefaultPort     = 5500
	DefaultBindHost = "127.0.0.1"
)

// Services groups the domain services the RPC handlers drive.
type Services struct {
	Envs   *service.EnvService
	Crons  *service.CronService
	Notify *service.NotifyService
}

// Options configures the server.
type Options struct {
	Port     int
	BindHost string
	// CertDir is data/config/grpc.
	CertDir  string
	Logger   *slog.Logger
	Services Services
}

// Server owns the gRPC listener and its graceful shutdown.
type Server struct {
	opts   Options
	logger *slog.Logger

	mu       sync.Mutex
	grpc     *grpc.Server
	listener net.Listener
	addr     string
	tls      bool
}

// NewServer prepares a server. Nothing is bound until Start.
func NewServer(opts Options) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if opts.Port <= 0 {
		opts.Port = DefaultPort
	}
	if strings.TrimSpace(opts.BindHost) == "" {
		opts.BindHost = DefaultBindHost
	}
	return &Server{opts: opts, logger: logger}
}

// Addr is the address the server bound to, empty before Start.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

// Start binds the port and serves in the background. A bind failure is
// returned, never fatal: the panel keeps working without the ql API.
func (s *Server) Start(ctx context.Context) error {
	credentialsOption, err := s.transportCredentials()
	if err != nil {
		return err
	}

	address := net.JoinHostPort(s.opts.BindHost, strconv.Itoa(s.opts.Port))
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", address, err)
	}

	server := grpc.NewServer(credentialsOption)
	RegisterApiServer(server, newAPI(s.opts.Services, s.logger))

	s.mu.Lock()
	s.grpc = server
	s.listener = listener
	s.addr = listener.Addr().String()
	s.mu.Unlock()

	go func() {
		if err := server.Serve(listener); err != nil {
			select {
			case <-ctx.Done():
				// Shutdown closes the listener; Serve reports that as an error.
			default:
				s.logger.Error("[grpc] serve failed", "error", err)
			}
		}
	}()

	s.logger.Debug("[boot] gRPC service started",
		"addr", s.Addr(), "mtls", s.usingTLS(), "cert_dir", s.opts.CertDir)
	return nil
}

// Stop drains in-flight RPCs and hard-stops after the timeout, like Node's
// tryShutdown + a forced stop.
func (s *Server) Stop(timeout time.Duration) {
	s.mu.Lock()
	server := s.grpc
	s.mu.Unlock()
	if server == nil {
		return
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	done := make(chan struct{})
	go func() {
		server.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
		s.logger.Debug("[boot] gRPC service stopped")
	case <-time.After(timeout):
		s.logger.Warn("[boot] gRPC graceful stop timed out, forcing", "timeout", timeout)
		server.Stop()
		<-done
	}
}

// transportCredentials prefers mTLS and only falls back to plaintext on a
// loopback bind. A non-loopback bind without certificates would expose an
// unauthenticated task API, so it fails instead.
func (s *Server) transportCredentials() (grpc.ServerOption, error) {
	tlsConfig, err := ensureTLS(s.opts.CertDir)
	if err == nil {
		s.tls = true
		return grpc.Creds(credentials.NewTLS(tlsConfig)), nil
	}
	if !isLoopbackHost(s.opts.BindHost) {
		return nil, fmt.Errorf("mTLS unavailable and %s is not a loopback host: %w", s.opts.BindHost, err)
	}
	s.logger.Warn("[grpc] mTLS unavailable, serving plaintext on loopback",
		"error", err, "cert_dir", s.opts.CertDir)
	return grpc.Creds(insecure.NewCredentials()), nil
}

func (s *Server) usingTLS() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tls
}

// isLoopbackHost reports whether the bind host can only be reached locally.
func isLoopbackHost(host string) bool {
	switch host {
	case "localhost":
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
