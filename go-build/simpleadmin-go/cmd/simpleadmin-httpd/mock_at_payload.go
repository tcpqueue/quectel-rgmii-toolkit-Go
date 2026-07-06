package main

import (
	"encoding/json"
	"strings"
	"sync"
)

const (
	mockATPayloadKindDashboard = "dashboard"
	mockATPayloadKindQCAInfo   = "qcainfo"
	mockATPayloadKindQENG      = "qeng"
)

type mockATPayloadState struct {
	mu        sync.RWMutex
	dashboard string
	qcainfo   string
	qeng      string
}

var mockATPayloads mockATPayloadState

func setMockATPayload(kind, payload string) bool {
	kind = normalizeMockATPayloadKind(kind)
	if kind == "" {
		return false
	}
	payload = normalizeMockATPayloadText(payload)
	mockATPayloads.mu.Lock()
	switch kind {
	case mockATPayloadKindDashboard:
		mockATPayloads.dashboard = payload
	case mockATPayloadKindQCAInfo:
		mockATPayloads.qcainfo = payload
	case mockATPayloadKindQENG:
		mockATPayloads.qeng = payload
	}
	mockATPayloads.mu.Unlock()
	atCommandCache.invalidateReadCache()
	return true
}

func clearMockATPayload(kind string) bool {
	kind = normalizeMockATPayloadKind(kind)
	mockATPayloads.mu.Lock()
	defer mockATPayloads.mu.Unlock()
	switch kind {
	case "":
		mockATPayloads.dashboard = ""
		mockATPayloads.qcainfo = ""
		mockATPayloads.qeng = ""
	case mockATPayloadKindDashboard:
		mockATPayloads.dashboard = ""
	case mockATPayloadKindQCAInfo:
		mockATPayloads.qcainfo = ""
	case mockATPayloadKindQENG:
		mockATPayloads.qeng = ""
	default:
		return false
	}
	atCommandCache.invalidateReadCache()
	return true
}

func getMockATPayload(kind string) (string, bool) {
	kind = normalizeMockATPayloadKind(kind)
	mockATPayloads.mu.RLock()
	defer mockATPayloads.mu.RUnlock()
	var payload string
	switch kind {
	case mockATPayloadKindDashboard:
		payload = mockATPayloads.dashboard
	case mockATPayloadKindQCAInfo:
		payload = mockATPayloads.qcainfo
	case mockATPayloadKindQENG:
		payload = mockATPayloads.qeng
	default:
		return "", false
	}
	return payload, strings.TrimSpace(payload) != ""
}

func mockATPayloadStatusText() string {
	mockATPayloads.mu.RLock()
	defer mockATPayloads.mu.RUnlock()
	return "dashboard=" + mockPayloadOnOff(mockATPayloads.dashboard) +
		", qcainfo=" + mockPayloadOnOff(mockATPayloads.qcainfo) +
		", qeng=" + mockPayloadOnOff(mockATPayloads.qeng)
}

func mockATPayloadStatusMap() map[string]any {
	mockATPayloads.mu.RLock()
	defer mockATPayloads.mu.RUnlock()
	return map[string]any{
		"dashboard": mockPayloadOnOff(mockATPayloads.dashboard),
		"qcainfo":   mockPayloadOnOff(mockATPayloads.qcainfo),
		"qeng":      mockPayloadOnOff(mockATPayloads.qeng),
	}
}

func mockPayloadOnOff(payload string) string {
	if strings.TrimSpace(payload) == "" {
		return "default"
	}
	return "manual"
}

func normalizeMockATPayloadKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "at", "all", "full", "dashboard", "at-test", "at_test_payload":
		return mockATPayloadKindDashboard
	case "qca", "qcainfo", "qca-test", "qcainfo-test", "qcainfo_test_payload":
		return mockATPayloadKindQCAInfo
	case "qeng", "qeng-test", "qeng_test_payload":
		return mockATPayloadKindQENG
	default:
		return ""
	}
}

func normalizeMockATPayloadText(payload string) string {
	payload = strings.ReplaceAll(payload, "\r\n", "\n")
	payload = strings.ReplaceAll(payload, "\r", "\n")
	return strings.TrimSpace(payload)
}

func currentMockDashboardATResponse(command string) string {
	base := defaultMockDashboardATResponse(command)
	if payload, ok := getMockATPayload(mockATPayloadKindDashboard); ok {
		return ensureMockATEnvelope(command, payload)
	}
	if payload, ok := getMockATPayload(mockATPayloadKindQCAInfo); ok {
		base = replaceMockATLines(base, "+QCAINFO:", payload)
	}
	if payload, ok := getMockATPayload(mockATPayloadKindQENG); ok {
		base = replaceMockATLines(base, "+QENG:", payload)
	}
	return ensureMockATEnvelope(command, base)
}

func currentMockQCAInfoATResponse(command string) string {
	if payload, ok := getMockATPayload(mockATPayloadKindQCAInfo); ok {
		return ensureMockATEnvelope(command, payload)
	}
	return ensureMockATEnvelope(command, defaultMockQCAInfoPayload())
}

func currentMockQENGATResponse(command string) string {
	if payload, ok := getMockATPayload(mockATPayloadKindQENG); ok {
		return ensureMockATEnvelope(command, payload)
	}
	return ensureMockATEnvelope(command, defaultMockQENGPayload())
}

func currentMockDashboardParseJSON() string {
	data := parseDashboardAT(currentMockDashboardATResponse(pageATCommand(atKeyDashboard)))
	buf, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err.Error()
	}
	return string(buf)
}

func ensureMockATEnvelope(command, payload string) string {
	payload = normalizeMockATPayloadText(payload)
	if payload == "" {
		payload = "OK"
	}
	upper := strings.ToUpper(payload)
	if !strings.HasPrefix(strings.TrimSpace(upper), "AT") && strings.TrimSpace(command) != "" {
		payload = strings.TrimSpace(command) + "\r\n" + payload
	}
	if !strings.Contains(upper, "\nOK") && !strings.HasSuffix(strings.TrimSpace(upper), "OK") && !strings.Contains(upper, "ERROR") {
		payload += "\r\nOK"
	}
	return strings.ReplaceAll(payload, "\n", "\r\n") + "\r\n"
}

func replaceMockATLines(raw, prefix, payload string) string {
	payload = normalizeMockATPayloadText(payload)
	if payload == "" {
		return raw
	}
	prefixUpper := strings.ToUpper(prefix)
	lines := strings.Split(strings.ReplaceAll(raw, "\r", ""), "\n")
	out := make([]string, 0, len(lines)+8)
	inserted := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToUpper(trimmed), prefixUpper) {
			if !inserted {
				out = append(out, strings.Split(payload, "\n")...)
				inserted = true
			}
			continue
		}
		if !inserted && strings.EqualFold(trimmed, "OK") {
			out = append(out, strings.Split(payload, "\n")...)
			inserted = true
		}
		out = append(out, line)
	}
	if !inserted {
		out = append(out, strings.Split(payload, "\n")...)
	}
	return strings.Join(out, "\n")
}

func defaultMockDashboardATResponse(command string) string {
	return strings.TrimSpace(command) + `
+QSIMSTAT: 0,1

+CSQ: 28,99

+QTEMP:"modem-lte-sub6-pa1","31"
+QTEMP:"aoss-0-usr","35"
+QTEMP:"cpuss-0-usr","35"
+QTEMP:"mdmq6-0-usr","35"
+QTEMP:"mdmss-0-usr","36"
+QTEMP:"modem-ambient-usr","31"

+QUIMSLOT: 1

+QSPN: "mobily","mobily","mobily",0,"42003"

+QMAP: "WWAN",1,1,"IPV4","10.219.172.53"
+QMAP: "WWAN",1,1,"IPV6","2a02:9b0:4070:fb:25a7:ccee:35bc:8062"

+QENG: "servingcell","NOCONN"
+QENG: "LTE","FDD",420,03,29E5B01,445,1850,3,5,5,439E,-88,-7,-61,15,10,200,-
+QENG: "NR5G-NSA",420,03,542,-98,18,-10,660768,77,12,1

+QCAINFO: "PCC",1850,100,"LTE BAND 3",1,445,-88,-8,-61,10
+QCAINFO: "SCC",300,100,"LTE BAND 1",1,445,-96,-12,-74,6,0,-,-
+QCAINFO: "SCC",660768,12,"NR5G BAND 77",542

+QGDNRCNT: 1961485,17217894

+QGDCNT: 1979589,17236170

+CGCONTRDP: 1,5,"WEB2","10.219.172.53","42.2.9.176.64.112.0.251.24.115.78.183.236.93.220.56", "254.128.0.0.0.0.0.0.0.0.0.0.0.0.0.1","86.51.34.24"

+QRSRP: -91,-88,-140,-140,LTE
+QRSRP: -120,-98,-110,-106,NR5G

OK`
}

func defaultMockQCAInfoPayload() string {
	return `+QCAINFO: "PCC",1850,100,"LTE BAND 3",1,445,-88,-8,-61,10
+QCAINFO: "SCC",300,100,"LTE BAND 1",1,445,-96,-12,-74,6,0,-,-
+QCAINFO: "SCC",660768,12,"NR5G BAND 77",542`
}

func defaultMockQENGPayload() string {
	return `+QENG: "servingcell","NOCONN"
+QENG: "LTE","FDD",420,03,29E5B01,445,1850,3,5,5,439E,-88,-7,-61,15,10,200,-
+QENG: "NR5G-NSA",420,03,542,-98,18,-10,660768,77,12,1`
}
