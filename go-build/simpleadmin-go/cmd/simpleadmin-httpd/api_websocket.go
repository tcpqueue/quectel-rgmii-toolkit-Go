package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"
)

const apiWebSocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

type apiWebSocketConn struct {
	conn net.Conn
	br   *bufio.Reader
	mu   sync.Mutex
}

type apiWebSocketRequest struct {
	ID      string            `json:"id"`
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

type apiWebSocketResponse struct {
	ID      string              `json:"id"`
	Status  int                 `json:"status"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    string              `json:"body"`
	Error   string              `json:"error,omitempty"`
}

type apiWebSocketEventMessage struct {
	Type  string            `json:"type"`
	Event string            `json:"event"`
	Data  map[string]string `json:"data,omitempty"`
}

var apiWebSocketEventHub = struct {
	mu      sync.Mutex
	clients map[*apiWebSocketConn]struct{}
}{clients: make(map[*apiWebSocketConn]struct{})}

func registerAPIWebSocketClient(ws *apiWebSocketConn) {
	apiWebSocketEventHub.mu.Lock()
	apiWebSocketEventHub.clients[ws] = struct{}{}
	apiWebSocketEventHub.mu.Unlock()
}

func unregisterAPIWebSocketClient(ws *apiWebSocketConn) {
	apiWebSocketEventHub.mu.Lock()
	delete(apiWebSocketEventHub.clients, ws)
	apiWebSocketEventHub.mu.Unlock()
}

func broadcastAPIWebSocketEvent(event string, data map[string]string) {
	message := apiWebSocketEventMessage{Type: "event", Event: event, Data: data}
	apiWebSocketEventHub.mu.Lock()
	clients := make([]*apiWebSocketConn, 0, len(apiWebSocketEventHub.clients))
	for ws := range apiWebSocketEventHub.clients {
		clients = append(clients, ws)
	}
	apiWebSocketEventHub.mu.Unlock()

	for _, ws := range clients {
		if err := ws.writeJSON(message); err != nil {
			unregisterAPIWebSocketClient(ws)
		}
	}
}

func (s *simpleAdminServer) handleAPIWebSocket(w http.ResponseWriter, r *http.Request) {
	if !apiWebSocketOriginAllowed(r) {
		http.Error(w, "websocket origin forbidden", http.StatusForbidden)
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || r.Header.Get("Sec-WebSocket-Key") == "" {
		http.Error(w, "websocket upgrade required", http.StatusBadRequest)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "websocket hijack unavailable", http.StatusInternalServerError)
		return
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return
	}
	ws := &apiWebSocketConn{conn: conn, br: rw.Reader}
	_ = conn.SetDeadline(time.Now().Add(webSocketWriteTimeout))
	accept := apiWebSocketAcceptKey(r.Header.Get("Sec-WebSocket-Key"))
	_, _ = rw.Writer.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
	_, _ = rw.Writer.WriteString("Upgrade: websocket\r\n")
	_, _ = rw.Writer.WriteString("Connection: Upgrade\r\n")
	_, _ = rw.Writer.WriteString("Sec-WebSocket-Accept: " + accept + "\r\n\r\n")
	if err := rw.Writer.Flush(); err != nil {
		_ = conn.Close()
		return
	}
	registerAPIWebSocketClient(ws)
	defer unregisterAPIWebSocketClient(ws)
	defer conn.Close()
	_ = conn.SetDeadline(time.Time{})
	r = r.WithContext(context.WithValue(r.Context(), apiConnectionContextKey{}, conn))
	untrack, ok := s.trackSessionConnection(r, conn, func() error { return ws.writeFrame(0x9, nil) })
	if !ok {
		return
	}
	defer untrack()

	for {
		opcode, payload, err := ws.readClientFrame()
		if err != nil {
			return
		}
		switch opcode {
		case 0x1, 0x2:
			resp := s.dispatchAPIWebSocketRequest(r, payload)
			if err := ws.writeJSON(resp); err != nil {
				return
			}
			if resp.Status == http.StatusUnauthorized || !s.isRequestAuthenticated(r) {
				return
			}
		case 0x8:
			_ = ws.writeClose()
			return
		case 0x9:
			if err := ws.writeFrame(0xA, payload); err != nil {
				return
			}
		case 0xA:
		default:
		}
	}
}

func (s *simpleAdminServer) dispatchAPIWebSocketRequest(base *http.Request, payload []byte) apiWebSocketResponse {
	var req apiWebSocketRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return apiWebSocketResponse{Status: http.StatusBadRequest, Body: "", Error: "invalid json request: " + err.Error()}
	}
	resp := apiWebSocketResponse{ID: req.ID, Status: http.StatusOK}
	if !s.isRequestAuthenticated(base) {
		resp.Status = http.StatusUnauthorized
		resp.Body = `{"ok":false,"error":"login required"}`
		return resp
	}

	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = http.MethodGet
	}
	path := strings.TrimSpace(req.Path)
	if path == "" {
		resp.Status = http.StatusBadRequest
		resp.Error = "missing api path"
		return resp
	}
	u, err := url.ParseRequestURI(path)
	if err != nil || u.Path == "" {
		resp.Status = http.StatusBadRequest
		resp.Error = "invalid api path"
		return resp
	}
	if u.Path == "/api/ws" || u.Path == "/api/console/ws" || !strings.HasPrefix(u.Path, "/api/") {
		resp.Status = http.StatusNotFound
		resp.Error = "unsupported websocket api endpoint: " + u.Path
		return resp
	}

	handler := s.nativeAPIHandlers()[u.Path]
	if handler == nil {
		resp.Status = http.StatusNotFound
		resp.Error = "unsupported websocket api endpoint: " + u.Path
		return resp
	}

	body := strings.NewReader(req.Body)
	httpReq := httptest.NewRequest(method, u.RequestURI(), body)
	httpReq = httpReq.WithContext(base.Context())
	httpReq.Host = base.Host
	httpReq.RemoteAddr = base.RemoteAddr
	httpReq.TLS = base.TLS
	for name, values := range base.Header {
		if strings.EqualFold(name, "Connection") || strings.EqualFold(name, "Upgrade") || strings.EqualFold(name, "Sec-WebSocket-Key") || strings.EqualFold(name, "Sec-WebSocket-Version") {
			continue
		}
		for _, value := range values {
			httpReq.Header.Add(name, value)
		}
	}
	for name, value := range req.Headers {
		if strings.EqualFold(name, "Content-Type") {
			httpReq.Header.Set(name, value)
		}
	}
	if req.Body != "" && httpReq.Header.Get("Content-Type") == "" {
		httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	}
	if req.Body != "" {
		httpReq.ContentLength = int64(len(req.Body))
	}

	rr := httptest.NewRecorder()
	handler(rr, httpReq)
	result := rr.Result()
	defer result.Body.Close()
	data, _ := io.ReadAll(result.Body)
	resp.Status = result.StatusCode
	resp.Headers = result.Header
	resp.Body = string(data)
	return resp
}

func apiWebSocketOriginAllowed(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	expectedScheme := "http"
	if r.TLS != nil {
		expectedScheme = "https"
	}
	if u.Scheme != expectedScheme {
		return false
	}
	return apiNormalizeOriginHost(u.Host) == apiNormalizeOriginHost(r.Host)
}

func apiNormalizeOriginHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	if strings.HasSuffix(host, ":80") {
		return strings.TrimSuffix(host, ":80")
	}
	if strings.HasSuffix(host, ":443") {
		return strings.TrimSuffix(host, ":443")
	}
	return host
}

func apiWebSocketAcceptKey(key string) string {
	sum := sha1.Sum([]byte(strings.TrimSpace(key) + apiWebSocketGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func (ws *apiWebSocketConn) readClientFrame() (byte, []byte, error) {
	if err := ws.conn.SetReadDeadline(time.Now().Add(webSocketReadTimeout)); err != nil {
		return 0, nil, err
	}
	for {
		header := make([]byte, 2)
		if _, err := io.ReadFull(ws.br, header); err != nil {
			return 0, nil, err
		}
		opcode := header[0] & 0x0f
		masked := header[1]&0x80 != 0
		length := uint64(header[1] & 0x7f)
		switch length {
		case 126:
			ext := make([]byte, 2)
			if _, err := io.ReadFull(ws.br, ext); err != nil {
				return 0, nil, err
			}
			length = uint64(binary.BigEndian.Uint16(ext))
		case 127:
			ext := make([]byte, 8)
			if _, err := io.ReadFull(ws.br, ext); err != nil {
				return 0, nil, err
			}
			length = binary.BigEndian.Uint64(ext)
		}
		if length > 1<<20 {
			return 0, nil, fmt.Errorf("websocket frame too large: %d", length)
		}
		var mask [4]byte
		if masked {
			if _, err := io.ReadFull(ws.br, mask[:]); err != nil {
				return 0, nil, err
			}
		}
		payload := make([]byte, length)
		if length > 0 {
			if _, err := io.ReadFull(ws.br, payload); err != nil {
				return 0, nil, err
			}
		}
		if masked {
			for i := range payload {
				payload[i] ^= mask[i%4]
			}
		}
		return opcode, payload, nil
	}
}

func (ws *apiWebSocketConn) writeJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		data = []byte(`{"status":500,"body":"","error":"json marshal failed"}`)
	}
	return ws.writeFrame(0x1, data)
}

func (ws *apiWebSocketConn) writeClose() error {
	return ws.writeFrame(0x8, nil)
}

func (ws *apiWebSocketConn) writeFrame(opcode byte, payload []byte) error {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if err := ws.conn.SetWriteDeadline(time.Now().Add(webSocketWriteTimeout)); err != nil {
		return err
	}

	header := bytes.NewBuffer([]byte{0x80 | opcode})
	length := len(payload)
	switch {
	case length < 126:
		header.WriteByte(byte(length))
	case length <= 0xffff:
		header.WriteByte(126)
		_ = binary.Write(header, binary.BigEndian, uint16(length))
	default:
		header.WriteByte(127)
		_ = binary.Write(header, binary.BigEndian, uint64(length))
	}
	if _, err := ws.conn.Write(header.Bytes()); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err := ws.conn.Write(payload)
	return err
}
