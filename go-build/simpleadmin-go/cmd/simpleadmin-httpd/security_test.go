package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func securityServer(t *testing.T) *simpleAdminServer {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "auth")
	if err := os.WriteFile(path, []byte("admin:admin\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return &simpleAdminServer{cfg: serverConfig{authFile: path, staticDir: dir, mockMode: true}}
}

func securitySession(t *testing.T, s *simpleAdminServer) *http.Cookie {
	t.Helper()
	token, err := s.createSession(time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: sessionCookieName, Value: token}
}

func securityRequest(s *simpleAdminServer, cookie *http.Cookie, method, path, form string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	return w
}

func TestPasswordChangeRevokesSessionsAndRequiresNewPassword(t *testing.T) {
	s := securityServer(t)
	first, second := securitySession(t, s), securitySession(t, s)
	w := securityRequest(s, first, "POST", "/api/set_password", "current_password=admin&new_password=changed123&confirm_password=changed123")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if s.validateSession(first.Value) || s.validateSession(second.Value) {
		t.Fatal("old sessions still valid")
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge != -1 {
		t.Fatal("session cookie not cleared")
	}
	if got := securityRequest(s, nil, "POST", "/api/login", "username=admin&password=admin").Code; got != 401 {
		t.Fatal(got)
	}
	if got := securityRequest(s, nil, "POST", "/api/login", "username=admin&password=changed123").Code; got != 200 {
		t.Fatal(got)
	}
}

func TestPasswordChangeFailuresPreserveSession(t *testing.T) {
	for _, form := range []string{
		"current_password=wrong&new_password=new",
		"current_password=admin&new_password=new&confirm_password=different",
		"current_password=admin&new_password=",
		"current_password=admin&new_password=" + strings.Repeat("x", 5000),
	} {
		s := securityServer(t)
		cookie := securitySession(t, s)
		w := securityRequest(s, cookie, "POST", "/api/set_password", form)
		if w.Code == 200 || !s.validateSession(cookie.Value) {
			t.Fatal(w.Code, "invalid change altered session")
		}
		auth, err := loadAuthConfig(s.cfg.authFile)
		if err != nil || auth.Password != "admin" {
			t.Fatal("password changed", err)
		}
	}
	s := securityServer(t)
	if got := securityRequest(s, nil, "POST", "/api/set_password", "current_password=admin&new_password=new").Code; got != 401 {
		t.Fatal(got)
	}
}

func TestWebSocketDispatchRejectsRevokedAndExpiredSessions(t *testing.T) {
	for _, expired := range []bool{false, true} {
		s := securityServer(t)
		cookie := securitySession(t, s)
		base := httptest.NewRequest("GET", "/api/ws", nil)
		base.AddCookie(cookie)
		if expired {
			s.sessions[cookie.Value] = time.Now().Add(-time.Second)
		} else {
			s.destroyRequestSession(base)
		}
		resp := s.dispatchAPIWebSocketRequest(base, []byte(`{"id":"check","path":"/api/get_language"}`))
		if resp.Status != 401 || resp.ID != "check" {
			t.Fatalf("response=%+v", resp)
		}
	}
}

func TestWebSocketPasswordChangeReturnsResponseBeforeRevocation(t *testing.T) {
	s := securityServer(t)
	r := httptest.NewRequest("GET", "http://device/api/ws", nil)
	r.Header.Set("Origin", "http://device")
	r.AddCookie(securitySession(t, s))
	resp := s.dispatchAPIWebSocketRequest(r, []byte(`{"id":"password","method":"POST","path":"/api/set_password","body":"current_password=admin&new_password=new123","headers":{"Origin":"http://other"}}`))
	if resp.Status != 200 || resp.ID != "password" || s.isRequestAuthenticated(r) {
		t.Fatalf("response=%+v", resp)
	}
}

func openSecuritySocket(t *testing.T, serverURL, path string, cookie *http.Cookie) net.Conn {
	t.Helper()
	addr := strings.TrimPrefix(serverURL, "http://")
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nOrigin: %s\r\nCookie: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n", path, addr, serverURL, cookie.String())
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 101 {
		t.Fatal(response.StatusCode)
	}
	return conn
}

func TestLogoutClosesEstablishedWebSocket(t *testing.T) {
	s := securityServer(t)
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	cookie := securitySession(t, s)
	conn := openSecuritySocket(t, ts.URL, "/api/ws", cookie)
	// Registration and logout can race; both orderings must close the connection.
	w := securityRequest(s, cookie, "POST", "/api/logout", "")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	conn.SetReadDeadline(time.Now().Add(time.Second))
	_, err := conn.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("connection remained open")
	}
	if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("revocation did not close connection")
	}
}

func TestSessionCleanupAndCapacity(t *testing.T) {
	s := securityServer(t)
	expired, live := securitySession(t, s), securitySession(t, s)
	s.sessions[expired.Value] = time.Now().Add(-time.Second)
	securitySession(t, s)
	if _, exists := s.sessions[expired.Value]; exists {
		t.Fatal("expired session retained")
	}
	if !s.validateSession(live.Value) {
		t.Fatal("live session removed")
	}
	for i := 0; i < maxSessions*2; i++ {
		securitySession(t, s)
	}
	if len(s.sessions) != maxSessions {
		t.Fatal("unbounded sessions", len(s.sessions))
	}
	s.sessionMu.Lock()
	s.pruneSessionsLocked(time.Now().Add(sessionDuration * 2))
	s.sessionMu.Unlock()
	if len(s.sessions) != 0 {
		t.Fatal("scheduled cleanup retained expired sessions")
	}
}

func TestATQueueRejectsOverflowAndCanRetry(t *testing.T) {
	m := &atCommandCacheManager{entries: make(map[string]*atCacheEntry), queue: make(chan string, 1), mockMode: true}
	first, err := m.enqueue("AT+CSQ", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.enqueue("ATI", true); err == nil {
		t.Fatal("overflow accepted")
	}
	if _, ok := m.entries["ATI"]; ok {
		t.Fatal("rejected task retained")
	}
	if got := m.Fetch("ATI", true, nil); got != atCacheBusyText {
		t.Fatal(got)
	}
	duplicate, err := m.enqueue("AT+CSQ", true)
	if err != nil || first != duplicate {
		t.Fatal("duplicate did not share completion")
	}
	m.run(<-m.queue)
	select {
	case <-first:
	default:
		t.Fatal("completion not signalled")
	}
	if _, err := m.enqueue("ATI", true); err != nil {
		t.Fatal("retry rejected", err)
	}
	m.run(<-m.queue)
}

func TestATQueueBusyDoesNotReturnPreviousSuccess(t *testing.T) {
	m := &atCommandCacheManager{entries: make(map[string]*atCacheEntry), queue: make(chan string, 1), mockMode: true}
	command := "AT+CFUN=1,1"
	m.entries[command] = &atCacheEntry{command: command, response: "OK", updatedAt: time.Now()}
	m.enqueue("AT+CSQ", true)
	if got := m.Fetch(command, true, nil); got != atCacheBusyText {
		t.Fatal(got)
	}
	if m.entries[command].running {
		t.Fatal("rejected action marked running")
	}
}

func TestATCacheEntryLimit(t *testing.T) {
	m := &atCommandCacheManager{entries: make(map[string]*atCacheEntry), queue: make(chan string, 1), mockMode: true}
	for i := 0; i < atCacheMaxEntries*2; i++ {
		if _, err := m.enqueue(fmt.Sprintf("AT+TEST%d?", i), true); err != nil {
			t.Fatal(err)
		}
		m.run(<-m.queue)
	}
	if len(m.entries) != atCacheMaxEntries {
		t.Fatal(len(m.entries))
	}
}

type shortDeadlineConn struct{ net.Conn }

func (c shortDeadlineConn) SetWriteDeadline(time.Time) error {
	return c.Conn.SetWriteDeadline(time.Now().Add(20 * time.Millisecond))
}

func TestWebSocketWriteDeadlineReleasesSlowPeer(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	ws := &apiWebSocketConn{conn: shortDeadlineConn{server}}
	err := ws.writeJSON(map[string]string{"value": "test"})
	if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		t.Fatal("expected write timeout", err)
	}
}

func TestHTTPServerRejectsSlowHeaders(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := newHTTPServer("", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode("ok") }))
	if server.ReadHeaderTimeout == 0 || server.ReadTimeout == 0 || server.WriteTimeout == 0 || server.IdleTimeout == 0 {
		t.Fatal("missing deadlines")
	}
	server.ReadHeaderTimeout = 20 * time.Millisecond
	done := make(chan struct{})
	go func() { defer close(done); server.Serve(listener) }()
	defer func() { server.Close(); <-done }()
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	io.WriteString(conn, "GET / HTTP/1.1\r\nHost:")
	_, err = io.ReadAll(conn)
	if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("slow headers were not rejected")
	}
}
