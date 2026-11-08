package service

import (
	"encoding/json"
	"log/slog"
	"sync"
)

// SockMessage is the payload every websocket broadcast carries
// (back/data/sock.ts). Optional fields are dropped when unset, exactly like
// JSON.stringify drops undefined properties.
type SockMessage struct {
	Type       string  `json:"type,omitempty"`
	Message    string  `json:"message,omitempty"`
	References []int64 `json:"references,omitempty"`
	// Status is a number for dependence updates and a string such as
	// "completed" for mirror updates, so it stays an interface.
	Status any `json:"status,omitempty"`
}

// SockConn is the subset of a SockJS session the broadcaster needs.
type SockConn interface {
	// Send delivers one text frame.
	Send(message string) error
	// Close terminates the connection. The websocket layer maps this to a
	// clean "going away" (1001) close so a server-initiated shutdown is visible
	// to the client's onclose handler instead of stalling server.Shutdown.
	Close() error
}

// SockService keeps the connected clients and fans messages out to all of
// them (back/services/sock.ts).
type SockService struct {
	logger *slog.Logger

	mu      sync.RWMutex
	clients map[SockConn]struct{}
}

// NewSockService creates an empty registry; connections are added by the
// websocket handler once their token has been validated.
func NewSockService(logger *slog.Logger) *SockService {
	if logger == nil {
		logger = slog.Default()
	}
	return &SockService{logger: logger, clients: map[SockConn]struct{}{}}
}

// Add registers a connection. A connection is never added twice because the
// handler only calls it after a successful authentication.
func (s *SockService) Add(conn SockConn) {
	if conn == nil {
		return
	}
	s.mu.Lock()
	s.clients[conn] = struct{}{}
	s.mu.Unlock()
}

// Remove unregisters a connection; safe to call from the close path twice.
func (s *SockService) Remove(conn SockConn) {
	s.mu.Lock()
	delete(s.clients, conn)
	s.mu.Unlock()
}

// Count reports the number of live connections (the session checker uses it to
// know when to stop).
func (s *SockService) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.clients)
}

// CloseAll terminates every live connection and forgets them. The panel calls
// it before http.Server.Shutdown so the long-lived SockJS sessions do not keep
// the listener open for the full shutdown timeout.
func (s *SockService) CloseAll() {
	s.mu.Lock()
	conns := make([]SockConn, 0, len(s.clients))
	for conn := range s.clients {
		conns = append(conns, conn)
	}
	s.clients = map[SockConn]struct{}{}
	s.mu.Unlock()

	for _, conn := range conns {
		_ = conn.Close()
	}
}

// Send broadcasts one message to every connected client. A message that cannot
// be encoded is logged and dropped: a broken broadcast must never fail the
// business operation that triggered it.
func (s *SockService) Send(msg SockMessage) {
	if msg.Type == "" && msg.Message == "" {
		return
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		s.logger.Warn("[sock] encode message failed", "type", msg.Type, "error", err)
		return
	}
	s.SendRaw(string(payload))
}

// SendRaw broadcasts an already encoded payload.
func (s *SockService) SendRaw(payload string) {
	s.mu.RLock()
	clients := make([]SockConn, 0, len(s.clients))
	for conn := range s.clients {
		clients = append(clients, conn)
	}
	s.mu.RUnlock()

	for _, conn := range clients {
		if err := conn.Send(payload); err != nil {
			// The session is already closing; the handler removes it.
			s.logger.Debug("[sock] send failed", "error", err)
		}
	}
}
