package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultPingTarget      = "www.baidu.com"
	telemetryWindow        = 5 * time.Minute
	pingInterval           = time.Second
	signalInterval         = 5 * time.Second
	telemetrySignalCommand = `AT+QTEMP;+QENG="servingcell"`
)

type historyRing[T any] struct {
	values []T
	next   int
	count  int
}

func (h *historyRing[T]) add(value T) {
	h.values[h.next] = value
	h.next = (h.next + 1) % len(h.values)
	if h.count < len(h.values) {
		h.count++
	}
}

func (h *historyRing[T]) snapshot() []T {
	values := make([]T, 0, h.count)
	for i := 0; i < h.count; i++ {
		values = append(values, h.values[(h.next-h.count+i+len(h.values))%len(h.values)])
	}
	return values
}

type pingSample struct {
	Time   int64    `json:"time"`
	RTT    *float64 `json:"rtt"`
	Jitter *float64 `json:"jitter"`
	Sent   bool     `json:"sent"`
	Status string   `json:"status"`
	IP     string   `json:"ip,omitempty"`
}

type signalSample struct {
	Time        int64    `json:"time"`
	RSRPLTE     *float64 `json:"rsrpLTE"`
	RSRPNR      *float64 `json:"rsrpNR"`
	SINRLTE     *float64 `json:"sinrLTE"`
	SINRNR      *float64 `json:"sinrNR"`
	Temperature *float64 `json:"temperature"`
	Status      string   `json:"status"`
}

type pingSummary struct {
	Sent     int      `json:"sent"`
	Received int      `json:"received"`
	Errors   int      `json:"errors"`
	Loss     *float64 `json:"loss"`
	Average  *float64 `json:"average"`
	Minimum  *float64 `json:"minimum"`
	Maximum  *float64 `json:"maximum"`
	Jitter   *float64 `json:"jitter"`
}

type telemetrySnapshot struct {
	Target     string         `json:"target"`
	Generation uint64         `json:"generation"`
	ServerTime int64          `json:"serverTime"`
	Mock       bool           `json:"mock"`
	Ping       []pingSample   `json:"ping"`
	Signal     []signalSample `json:"signal"`
	Summary    pingSummary    `json:"summary"`
}

type telemetryMonitor struct {
	mu         sync.RWMutex
	target     string
	generation uint64
	configPath string
	mock       bool
	ping       pingHistory
	signal     signalHistory
	probe      func(context.Context, string) pingSample
	readSignal func() signalSample
}

func newTelemetryMonitor(configPath string, mock bool) *telemetryMonitor {
	m := &telemetryMonitor{
		target: defaultPingTarget, configPath: configPath, mock: mock,
		ping:   pingHistory{historyRing: historyRing[compactPing]{values: make([]compactPing, 300)}},
		signal: signalHistory{historyRing: historyRing[compactSignal]{values: make([]compactSignal, 60)}},
	}
	var config struct {
		Target string `json:"target"`
	}
	if data, err := os.ReadFile(configPath); err == nil && json.Unmarshal(data, &config) == nil {
		if target, err := normalizePingTarget(config.Target); err == nil {
			m.target = target
		}
	}
	prober := &icmpProber{}
	m.probe = prober.probe
	m.readSignal = func() signalSample {
		wait := true
		return parseSignalSample(atCommandCache.Fetch(telemetrySignalCommand, false, &wait))
	}
	if mock {
		m.probe = func(ctx context.Context, target string) pingSample {
			x := float64(time.Now().UnixMilli()) / 1000
			rtt := roundMetric(24 + 9*math.Sin(x/8) + 4*math.Sin(x/2))
			return pingSample{RTT: &rtt, Sent: true, Status: "ok", IP: "192.0.2.1"}
		}
		m.readSignal = func() signalSample {
			x := float64(time.Now().UnixMilli()) / 1000
			return signalSample{RSRPLTE: metric(-89 + 4*math.Sin(x/21)), RSRPNR: metric(-95 + 3*math.Sin(x/18)), SINRLTE: metric(18 + 3*math.Sin(x/13)), SINRNR: metric(22 + 4*math.Sin(x/20)), Temperature: metric(43 + 2*math.Sin(x/50)), Status: "ok"}
		}
	}
	return m
}

func (m *telemetryMonitor) start(ctx context.Context) {
	go m.runPing(ctx)
	go m.runSignal(ctx)
}

func (m *telemetryMonitor) runPing(ctx context.Context) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		m.mu.RLock()
		target, generation := m.target, m.generation
		m.mu.RUnlock()
		started := time.Now()
		probeContext, cancel := context.WithTimeout(ctx, 900*time.Millisecond)
		sample := m.probe(probeContext, target)
		cancel()
		if ctx.Err() != nil {
			return
		}
		sample.Time = started.UnixMilli()
		m.recordPing(generation, sample)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *telemetryMonitor) runSignal(ctx context.Context) {
	ticker := time.NewTicker(signalInterval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		started := time.Now()
		sample := m.readSignal()
		sample.Time = started.UnixMilli()
		if ctx.Err() != nil {
			return
		}
		m.mu.Lock()
		m.signal.add(sample)
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *telemetryMonitor) recordPing(generation uint64, sample pingSample) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if generation != m.generation {
		return
	}
	if m.ping.count > 0 && sample.RTT != nil {
		prev := m.ping.values[(m.ping.next+len(m.ping.values)-1)%len(m.ping.values)].expand()
		if prev.RTT != nil && sample.Time > prev.Time && sample.Time-prev.Time <= 1500 {
			sample.Jitter = metric(math.Abs(*sample.RTT - *prev.RTT))
		}
	}
	m.ping.add(sample)
}

func (m *telemetryMonitor) snapshot(now time.Time) telemetrySnapshot {
	m.mu.RLock()
	snapshot := telemetrySnapshot{Target: m.target, Generation: m.generation, Mock: m.mock, ServerTime: now.UnixMilli(), Ping: m.ping.snapshot(), Signal: m.signal.snapshot()}
	m.mu.RUnlock()
	cutoff := now.Add(-telemetryWindow).UnixMilli()
	ping := snapshot.Ping[:0]
	for _, sample := range snapshot.Ping {
		if sample.Time > cutoff {
			ping = append(ping, sample)
		}
	}
	snapshot.Ping = ping
	signal := snapshot.Signal[:0]
	for _, sample := range snapshot.Signal {
		if sample.Time > cutoff {
			signal = append(signal, sample)
		}
	}
	snapshot.Signal = signal
	snapshot.Summary = summarizePing(ping)
	return snapshot
}

func summarizePing(samples []pingSample) pingSummary {
	result := pingSummary{}
	var sum, jitter float64
	var jitterCount int
	for _, sample := range samples {
		if sample.Sent {
			result.Sent++
		} else {
			result.Errors++
		}
		if sample.RTT != nil {
			result.Received++
			sum += *sample.RTT
			if result.Minimum == nil || *sample.RTT < *result.Minimum {
				result.Minimum = metric(*sample.RTT)
			}
			if result.Maximum == nil || *sample.RTT > *result.Maximum {
				result.Maximum = metric(*sample.RTT)
			}
		}
		if sample.Jitter != nil {
			jitter += *sample.Jitter
			jitterCount++
		}
	}
	if result.Sent > 0 {
		result.Loss = metric(100 * float64(result.Sent-result.Received) / float64(result.Sent))
	}
	if result.Received > 0 {
		result.Average = metric(sum / float64(result.Received))
	}
	if jitterCount > 0 {
		result.Jitter = metric(jitter / float64(jitterCount))
	}
	return result
}

func metric(value float64) *float64     { value = roundMetric(value); return &value }
func roundMetric(value float64) float64 { return math.Round(value*10) / 10 }

func parseSignalSample(raw string) signalSample {
	sample := signalSample{Status: "unavailable"}
	if strings.Contains(raw, atCachePendingText) || strings.Contains(raw, "ERROR") {
		return sample
	}
	data := parseDashboardAT(raw)
	read := func(key string, min, max float64) *float64 {
		value, err := strconv.ParseFloat(stringValue(data[key]), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < min || value > max {
			return nil
		}
		return metric(value)
	}
	sample.RSRPLTE, sample.RSRPNR = read("rsrpLTE", -160, -20), read("rsrpNR", -160, -20)
	sample.SINRLTE, sample.SINRNR = read("sinrLTE", -30, 60), read("sinrNR", -30, 60)
	sample.Temperature = read("temperature", -40, 150)
	if sample.Temperature != nil || sample.RSRPLTE != nil || sample.RSRPNR != nil || sample.SINRLTE != nil || sample.SINRNR != nil {
		sample.Status = "ok"
	}
	return sample
}

func normalizePingTarget(raw string) (string, error) {
	target := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
	if ip := net.ParseIP(target); ip != nil {
		if ip.IsUnspecified() || ip.IsMulticast() || ip.Equal(net.IPv4bcast) {
			return "", errors.New("target must be a unicast address")
		}
		return ip.String(), nil
	}
	if target == "" || len(target) > 253 {
		return "", errors.New("invalid hostname")
	}
	for _, label := range strings.Split(target, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("invalid hostname")
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
				return "", errors.New("enter a hostname or IP address without a URL, port or path")
			}
		}
	}
	return target, nil
}

func (m *telemetryMonitor) setTarget(raw string) error {
	target, err := normalizePingTarget(raw)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.target == target {
		return nil
	}
	data, err := json.Marshal(struct {
		Target string `json:"target"`
	}{target})
	if err != nil {
		return err
	}
	err = writePersistentFile(m.configPath, append(data, '\n'), 0600)
	if err != nil && !persistenceCommitted(err) {
		return err
	}
	m.target = target
	m.generation++
	m.ping.next, m.ping.count, m.ping.lastIP = 0, 0, ""
	return err
}

func (s *simpleAdminServer) handleTelemetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	if s.telemetry == nil {
		writeJSON(w, 503, map[string]string{"error": "monitor unavailable"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, s.telemetry.snapshot(time.Now()))
}

func (s *simpleAdminServer) handleTelemetryTarget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	if r.Header.Get("Origin") != "" && !apiWebSocketOriginAllowed(r) {
		writeJSON(w, 403, map[string]string{"error": "origin forbidden"})
		return
	}
	if s.telemetry == nil {
		writeJSON(w, 503, map[string]string{"error": "monitor unavailable"})
		return
	}
	var config struct {
		Target string `json:"target"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid target"})
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeJSON(w, 400, map[string]string{"error": "invalid target"})
		return
	}
	if _, err := normalizePingTarget(config.Target); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	if err := s.telemetry.setTarget(config.Target); err != nil {
		writeJSON(w, 500, map[string]string{"error": "could not save target"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, s.telemetry.snapshot(time.Now()))
}
