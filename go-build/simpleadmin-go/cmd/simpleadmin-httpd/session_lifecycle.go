package main

import (
	"context"
	"net"
	"net/http"
	"time"
)

const (
	maxSessions           = 128
	webSocketReadTimeout  = 90 * time.Second
	webSocketWriteTimeout = 10 * time.Second
	webSocketPingInterval = 30 * time.Second
)

type apiConnectionContextKey struct{}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr: addr, Handler: handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		// AT scans and multipart SMS can take minutes; WebSockets set their own deadlines.
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  60 * time.Second,
	}
}

// Caller holds sessionMu. Closing a connection also releases its blocked reader.
func (s *simpleAdminServer) revokeSessionLocked(token string, except net.Conn) {
	delete(s.sessions, token)
	for conn, owner := range s.sessionConnections {
		if owner == token && conn != except {
			_ = conn.Close()
			delete(s.sessionConnections, conn)
		}
	}
}

func (s *simpleAdminServer) pruneSessionsLocked(now time.Time) {
	for token, expiry := range s.sessions {
		if !expiry.After(now) {
			s.revokeSessionLocked(token, nil)
		}
	}
}

func (s *simpleAdminServer) revokeAllSessions(except net.Conn) {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	for token := range s.sessions {
		s.revokeSessionLocked(token, except)
	}
}

func (s *simpleAdminServer) cleanupSessions(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.sessionMu.Lock()
			s.pruneSessionsLocked(now)
			s.sessionMu.Unlock()
		}
	}
}

func (s *simpleAdminServer) trackSessionConnection(r *http.Request, conn net.Conn, ping func() error) (func(), bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil, false
	}
	s.sessionMu.Lock()
	s.pruneSessionsLocked(time.Now())
	if _, ok := s.sessions[cookie.Value]; !ok {
		s.sessionMu.Unlock()
		return nil, false
	}
	if s.sessionConnections == nil {
		s.sessionConnections = make(map[net.Conn]string)
	}
	s.sessionConnections[conn] = cookie.Value
	s.sessionMu.Unlock()
	stopped := make(chan struct{})
	go func() {
		ticker := time.NewTicker(webSocketPingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stopped:
				return
			case <-ticker.C:
				s.sessionMu.Lock()
				valid := s.sessions[cookie.Value].After(time.Now())
				s.sessionMu.Unlock()
				if !valid || ping() != nil {
					_ = conn.Close()
					return
				}
			}
		}
	}()
	return func() {
		close(stopped)
		s.sessionMu.Lock()
		delete(s.sessionConnections, conn)
		s.sessionMu.Unlock()
	}, true
}
