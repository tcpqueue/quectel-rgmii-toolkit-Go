package main

import "math"

const missingMetric int16 = -32768

var telemetryStatuses = [...]string{"pending", "ok", "timeout", "dns_error", "permission_error", "unavailable"}

// Fixed-point tenths retain the API precision without per-point heap objects.
type compactPing struct {
	Time        int64
	RTT, Jitter int16
	Status      uint8
	Sent        bool
}
type compactSignal struct {
	Time   int64
	Values [5]int16
	Status uint8
}
type pingHistory struct {
	historyRing[compactPing]
	lastIP string
}
type signalHistory struct{ historyRing[compactSignal] }

func compactMetric(value *float64) int16 {
	if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) {
		return missingMetric
	}
	rounded := math.Round(*value * 10)
	if rounded <= -32768 || rounded > 32767 {
		return missingMetric
	}
	return int16(rounded)
}
func expandMetric(value int16) *float64 {
	if value == missingMetric {
		return nil
	}
	result := float64(value) / 10
	return &result
}
func compactStatus(value string) uint8 {
	for i, status := range telemetryStatuses {
		if status == value {
			return uint8(i)
		}
	}
	return 5
}
func (p compactPing) expand() pingSample {
	return pingSample{Time: p.Time, RTT: expandMetric(p.RTT), Jitter: expandMetric(p.Jitter), Status: telemetryStatuses[p.Status], Sent: p.Sent}
}
func (h *pingHistory) add(p pingSample) {
	h.historyRing.add(compactPing{p.Time, compactMetric(p.RTT), compactMetric(p.Jitter), compactStatus(p.Status), p.Sent})
	h.lastIP = p.IP
}
func (h *pingHistory) snapshot() []pingSample {
	result := make([]pingSample, 0, h.count)
	for i := 0; i < h.count; i++ {
		result = append(result, h.values[(h.next-h.count+i+len(h.values))%len(h.values)].expand())
	}
	if len(result) > 0 {
		result[len(result)-1].IP = h.lastIP
	}
	return result
}
func (h *signalHistory) add(p signalSample) {
	h.historyRing.add(compactSignal{p.Time, [5]int16{compactMetric(p.RSRPLTE), compactMetric(p.RSRPNR), compactMetric(p.SINRLTE), compactMetric(p.SINRNR), compactMetric(p.Temperature)}, compactStatus(p.Status)})
}
func (h *signalHistory) snapshot() []signalSample {
	result := make([]signalSample, 0, h.count)
	for i := 0; i < h.count; i++ {
		p := h.values[(h.next-h.count+i+len(h.values))%len(h.values)]
		result = append(result, signalSample{p.Time, expandMetric(p.Values[0]), expandMetric(p.Values[1]), expandMetric(p.Values[2]), expandMetric(p.Values[3]), expandMetric(p.Values[4]), telemetryStatuses[p.Status]})
	}
	return result
}
