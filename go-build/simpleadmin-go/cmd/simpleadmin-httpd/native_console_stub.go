//go:build !linux
// +build !linux

package main

import (
	"crypto/sha1"
	"encoding/base64"
	"net/http"
)

const nativeConsoleHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Terminal</title>
<style>
body{font-family:Arial,sans-serif;background:#111;color:#eee;padding:24px;line-height:1.5}
.card{max-width:760px;margin:auto;background:#1d1d1d;border:1px solid #333;border-radius:12px;padding:20px}
code{background:#000;padding:2px 6px;border-radius:4px}
</style>
</head>
<body>
<div class="card">
<h1>Terminal Windows local test</h1>
<p>The native PTY shell console is only enabled on the Linux module build.</p>
<p>This Windows test server is intended for checking Vue pages, login, routing and mock <code>/api/*</code> responses.</p>
</div>
</body>
</html>`

func (s *simpleAdminServer) handleNativeConsole(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/console" && r.URL.Path != "/console/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(nativeConsoleHTML))
}

func (s *simpleAdminServer) handleNativeConsoleWebSocket(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "native PTY console is only available on Linux module builds", http.StatusNotImplemented)
}

func websocketAcceptKey(key string) string {
	h := sha1.New()
	_, _ = h.Write([]byte(key))
	_, _ = h.Write([]byte("258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}
