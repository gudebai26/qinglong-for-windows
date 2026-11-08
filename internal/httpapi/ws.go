package httpapi

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/igm/sockjs-go/v3/sockjs"

	"qinglong-go/internal/auth"
)

// sockConn adapts a SockJS session to the broadcaster's SockConn interface. A
// SockJS session only exposes Close(status, reason), so Close() maps it to a
// clean "going away" (1001) close — the code the front-end's onclose handler
// recognises as a server-initiated shutdown.
type sockConn struct {
	session sockjs.Session
}

func (c sockConn) Send(message string) error {
	return c.session.Send(message)
}

func (c sockConn) Close() error {
	return c.session.Close(1001, "going away")
}

// wsSession is one authenticated SockJS connection together with the token it
// presented, so the periodic check can revoke it without a reconnect.
type wsSession struct {
	conn     sockjs.Session
	token    string
	platform string
}

// wsRegistry owns the authenticated sessions and the once-per-second token
// revalidation (back/loaders/sock.ts).
type wsRegistry struct {
	deps Deps

	mu       sync.Mutex
	sessions map[*wsSession]struct{}
	timer    *time.Ticker
	stop     chan struct{}
}

func newWSRegistry(deps Deps) *wsRegistry {
	return &wsRegistry{deps: deps, sessions: map[*wsSession]struct{}{}}
}

// add registers a session and makes sure the revalidation loop is running.
func (r *wsRegistry) add(session *wsSession) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[session] = struct{}{}
	if r.timer == nil {
		r.timer = time.NewTicker(time.Second)
		r.stop = make(chan struct{})
		go r.checkLoop(r.timer.C, r.stop)
	}
}

// remove unregisters a session and stops the loop when the last one goes away,
// which is what Node does in the connection close handler.
func (r *wsRegistry) remove(session *wsSession) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, session)
	if len(r.sessions) == 0 && r.timer != nil {
		r.timer.Stop()
		close(r.stop)
		r.timer = nil
		r.stop = nil
	}
}

func (r *wsRegistry) checkLoop(tick <-chan time.Time, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case <-tick:
			r.checkOnce()
		}
	}
}

// checkOnce validates every session against one snapshot of authInfo. A token
// that is no longer stored (logout, password change, expiry) closes the
// connection with 401.
func (r *wsRegistry) checkOnce() {
	r.mu.Lock()
	batch := make([]*wsSession, 0, len(r.sessions))
	for session := range r.sessions {
		batch = append(batch, session)
	}
	r.mu.Unlock()
	if len(batch) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := r.deps.User.AuthInfo(ctx)
	if err != nil {
		r.closeAll(batch, 401)
		return
	}

	// One validation per (token, platform) pair, like the Node cache.
	type tokenKey struct{ token, platform string }
	validated := make(map[tokenKey]bool, len(batch))
	for _, session := range batch {
		key := tokenKey{session.token, session.platform}
		valid, seen := validated[key]
		if !seen {
			valid = validStoredToken(info, session.token, session.platform, r.deps.Config.JWTSecret)
			validated[key] = valid
		}
		if !valid {
			_ = session.conn.Close(401, "401")
			r.remove(session)
		}
	}
}

func (r *wsRegistry) closeAll(batch []*wsSession, code uint32) {
	for _, session := range batch {
		_ = session.conn.Close(code, "401")
		r.remove(session)
	}
}

// wsHandler intercepts the SockJS endpoint before the JWT middleware, exactly
// like Node installs sockjs directly on the HTTP server (back/loaders/server.ts).
// The panel token arrives in the query string because a browser WebSocket
// cannot set an Authorization header.
func wsHandler(deps Deps) func(http.Handler) http.Handler {
	prefix := deps.Config.BaseURL + "/api/ws"
	registry := newWSRegistry(deps)

	handler := sockjs.NewHandler(prefix, sockjs.DefaultOptions, func(session sockjs.Session) {
		request := session.Request()
		if request == nil {
			_ = session.Close(404, "404")
			return
		}

		token := request.URL.Query().Get("token")
		if token == "" {
			// Node derives the token with a string replace and ends up with the
			// whole URL, which never validates; the observable result is 404.
			_ = session.Close(404, "404")
			return
		}
		platform := auth.Platform(request.UserAgent())
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		info, err := deps.User.AuthInfo(ctx)
		cancel()
		if err != nil || !validStoredToken(info, token, platform, deps.Config.JWTSecret) {
			_ = session.Close(404, "404")
			return
		}

		entry := &wsSession{conn: session, token: token, platform: platform}
		conn := sockConn{session: session}
		deps.Sock.Add(conn)
		registry.add(entry)

		defer func() {
			deps.Sock.Remove(conn)
			registry.remove(entry)
		}()

		// The panel only uses the socket for server push, but Node echoes any
		// frame the client sends, and the front-end relies on the connection
		// staying readable.
		for {
			message, err := session.Recv()
			if err != nil {
				return
			}
			if err := session.Send(message); err != nil {
				return
			}
		}
	})

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			if path == prefix || strings.HasPrefix(path, prefix+"/") {
				handler.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
