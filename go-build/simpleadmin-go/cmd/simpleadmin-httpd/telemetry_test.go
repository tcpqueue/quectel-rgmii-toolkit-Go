package main

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTelemetryRetentionAndJitter(t *testing.T) {
	m := newTelemetryMonitor(filepath.Join(t.TempDir(), "monitor.json"), true)
	now := time.Now()
	for i := 0; i < 400; i++ {
		m.recordPing(0, pingSample{Time: now.Add(time.Duration(i-399) * time.Second).UnixMilli(), RTT: metric(float64(i)), Sent: true, Status: "ok"})
		m.signal.add(signalSample{Time: now.Add(time.Duration(i-399) * 5 * time.Second).UnixMilli()})
	}
	snapshot := m.snapshot(now)
	if len(snapshot.Ping) != 300 || len(snapshot.Signal) != 60 {
		t.Fatalf("unexpected history lengths %d/%d", len(snapshot.Ping), len(snapshot.Signal))
	}
	if *snapshot.Summary.Jitter != 1 || *snapshot.Summary.Loss != 0 {
		t.Fatal(snapshot.Summary)
	}
	snapshot.Ping[0].Status = "changed"
	if m.snapshot(now).Ping[0].Status == "changed" {
		t.Fatal("snapshot aliases history")
	}
	if got := m.snapshot(now.Add(5 * time.Minute)); len(got.Ping) != 0 || len(got.Signal) != 0 {
		t.Fatal("expired points retained")
	}
	m.recordPing(0, pingSample{Time: now.Add(time.Second).UnixMilli(), Sent: true, Status: "timeout"})
	m.recordPing(0, pingSample{Time: now.Add(2 * time.Second).UnixMilli(), RTT: metric(12), Sent: true, Status: "ok"})
	if m.snapshot(now.Add(2 * time.Second)).Ping[299].Jitter != nil {
		t.Fatal("jitter crosses failure")
	}
	stats := summarizePing([]pingSample{{Sent: true, RTT: metric(10)}, {Sent: true}, {Status: "dns_error"}})
	if stats.Sent != 2 || stats.Errors != 1 || *stats.Loss != 50 || *stats.Average != 10 {
		t.Fatal(stats)
	}
}

func TestTelemetryTargetAndGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitor.json")
	m := newTelemetryMonitor(path, true)
	m.recordPing(0, pingSample{Time: time.Now().UnixMilli()})
	m.signal.add(signalSample{Time: time.Now().UnixMilli()})
	if err := m.setTarget(" WWW.BAIDU.COM. "); err != nil || m.ping.count != 1 {
		t.Fatal("same target cleared history", err)
	}
	if err := m.setTarget("1.1.1.1"); err != nil {
		t.Fatal(err)
	}
	m.recordPing(0, pingSample{})
	if m.ping.count != 0 || m.signal.count != 1 || m.generation != 1 {
		t.Fatal("target isolation failed")
	}
	restored := newTelemetryMonitor(path, true)
	if restored.target != "1.1.1.1" || restored.ping.count != 0 {
		t.Fatal("bad persisted config")
	}
	for _, raw := range []string{"", "https://baidu.com", "x;id", "x:80", "a/b", "-x.com", "a..b", "0.0.0.0", "224.0.0.1", "::", strings.Repeat("a", 64) + ".com"} {
		if _, err := normalizePingTarget(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	for _, raw := range []string{"www.baidu.com", "192.168.1.1", "::1", "2001:4860:4860::8888"} {
		if _, err := normalizePingTarget(raw); err != nil {
			t.Errorf("rejected %q: %v", raw, err)
		}
	}
}

func TestTelemetryHTTP(t *testing.T) {
	s := securityServer(t)
	s.telemetry = newTelemetryMonitor(filepath.Join(t.TempDir(), "monitor.json"), true)
	cookie := securitySession(t, s)
	for _, path := range []string{"/api/telemetry", "/api/telemetry/target"} {
		if got := securityRequest(s, nil, "GET", path, "").Code; got != 401 {
			t.Fatal(got)
		}
	}
	if w := securityRequest(s, cookie, "GET", "/api/telemetry", ""); w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code)
	}
	for _, body := range []string{`{"target":"x"} {}`, `{"target":"x","extra":1}`, `{"target":"https://x"}`, strings.Repeat("x", 1025)} {
		if w := securityRequest(s, cookie, "POST", "/api/telemetry/target", body); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	r := httptest.NewRequest("POST", "http://device/api/telemetry/target", strings.NewReader(`{"target":"example.com"}`))
	r.AddCookie(cookie)
	r.Header.Set("Origin", "https://other.example")
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := securityRequest(s, cookie, "POST", "/api/telemetry/target", `{"target":"example.com"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := securityRequest(s, cookie, "GET", "/api/telemetry/target", ""); w.Code != 405 {
		t.Fatal(w.Code)
	}
}

func TestTelemetryWorkersWithoutWebUI(t *testing.T) {
	m := newTelemetryMonitor(filepath.Join(t.TempDir(), "monitor.json"), true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pingDone, signalDone := make(chan struct{}), make(chan struct{})
	go func() { m.runPing(ctx); close(pingDone) }()
	go func() { m.runSignal(ctx); close(signalDone) }()
	deadline := time.After(3 * time.Second)
	for {
		s := m.snapshot(time.Now())
		if len(s.Ping) >= 2 && len(s.Signal) >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("background workers did not sample")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	for _, done := range []chan struct{}{pingDone, signalDone} {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("worker did not stop")
		}
	}
	if sample := parseSignalSample("ERROR"); sample.RSRPLTE != nil || sample.Temperature != nil {
		t.Fatal("failure produced zero values")
	}
}

func TestICMPLoopback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	sample := (&icmpProber{}).probe(ctx, "127.0.0.1")
	if sample.Status == "permission_error" {
		t.Skip("ICMP permission unavailable")
	}
	if sample.Status != "ok" || sample.RTT == nil || !sample.Sent {
		t.Fatalf("loopback probe: %+v", sample)
	}
}
