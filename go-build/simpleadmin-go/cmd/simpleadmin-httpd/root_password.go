package main

import (
	"net"
	"net/http"
	"strings"
)

func (s *simpleAdminServer) rootPasswordMatches(password string) bool {
	if s.cfg.mockMode {
		expected := s.mockRootPassword
		if expected == "" {
			expected = "admin"
		}
		return constantTimeEqual(password, expected)
	}
	return verifySystemRootPassword(password)
}

func (s *simpleAdminServer) handleSetRootPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	if r.Header.Get("Origin") != "" && !apiWebSocketOriginAllowed(r) {
		writeJSON(w, 403, map[string]string{"error": "origin forbidden"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if r.ParseForm() != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid form"})
		return
	}
	current, next, confirm := r.Form.Get("current_password"), r.Form.Get("new_password"), r.Form.Get("confirm_password")
	if validateNewPassword(next) != nil || strings.ContainsRune(next, 0) || next != confirm {
		writeJSON(w, 400, map[string]string{"error": "密码不能为空、不能包含换行，且两次新密码必须一致（最多128字节）"})
		return
	}
	s.authMu.Lock()
	defer s.authMu.Unlock()
	if !s.rootPasswordMatches(current) {
		writeJSON(w, 403, map[string]string{"error": "当前 root 密码不正确"})
		return
	}
	var err error
	if s.cfg.mockMode {
		s.mockRootPassword = next
	} else {
		err = changeSystemRootPassword(current, next, false)
	}
	if err == nil || persistenceCommitted(err) {
		connection, _ := r.Context().Value(apiConnectionContextKey{}).(net.Conn)
		s.revokeAllSessions(connection)
		clearSessionCookie(w, r)
	}
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "root 密码保存或恢复只读失败，请检查设备状态"})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "redirect": "/login.html"})
}
