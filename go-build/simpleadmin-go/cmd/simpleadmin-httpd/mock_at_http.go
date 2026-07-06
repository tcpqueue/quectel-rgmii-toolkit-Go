package main

import (
	"net/http"
	"strings"
)

func (s *simpleAdminServer) handleMockATPayload(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.mockMode {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"ok":    false,
			"error": "mock mode only",
		})
		return
	}

	action := strings.ToLower(strings.TrimSpace(requestValue(r, "action")))
	if action == "" {
		action = "show"
	}

	switch action {
	case "set", "save":
		kind := requestValue(r, "kind")
		payload := requestValue(r, "payload")
		if !setMockATPayload(kind, payload) {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": "unknown mock AT payload kind",
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     true,
			"kind":   normalizeMockATPayloadKind(kind),
			"status": mockATPayloadStatusMap(),
		})
	case "clear", "reset":
		kind := requestValue(r, "kind")
		if !clearMockATPayload(kind) {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"ok":    false,
				"error": "unknown mock AT payload kind",
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     true,
			"status": mockATPayloadStatusMap(),
		})
	case "parse":
		data := parseDashboardAT(currentMockDashboardATResponse(pageATCommand(atKeyDashboard)))
		data["ok"] = true
		data["status"] = mockATPayloadStatusMap()
		if boolQuery(r, "debug", false) {
			data["raw"] = currentMockDashboardATResponse(pageATCommand(atKeyDashboard))
		}
		writeJSON(w, http.StatusOK, data)
	case "show", "status":
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     true,
			"status": mockATPayloadStatusMap(),
		})
	case "help":
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true,
			"commands": []string{
				"SimpleAdmin.MockAT.at(`完整首页 AT 返回`)",
				"SimpleAdmin.MockAT.qca(`QCAINFO 返回`)",
				"SimpleAdmin.MockAT.qeng(`QENG 返回`)",
				"SimpleAdmin.MockAT.parse()",
				"SimpleAdmin.MockAT.show()",
				"SimpleAdmin.MockAT.clear()",
			},
		})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": "unsupported mock AT action",
		})
	}
}
