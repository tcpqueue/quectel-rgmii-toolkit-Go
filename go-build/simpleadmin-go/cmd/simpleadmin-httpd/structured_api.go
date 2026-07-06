package main

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	atKeyDashboard       = "dashboard"
	atKeyDeviceInfo      = "device_info"
	atKeyNetworkBands    = "network_bands"
	atKeyNetworkSettings = "network_settings"
	atKeySettingsStatus  = "settings_status"
	atKeyModel           = "model"
	atKeySMSList         = "sms_list"
	atKeySIMStatus       = "sim_status"
	atKeyIMSI            = "imsi"
)

func pageATCommands(key string) []string {
	switch key {
	case atKeyDashboard:
		return []string{`AT+QSIMSTAT?;+CSQ;+QTEMP;+QUIMSLOT?;+QSPN;+QMAP="WWAN";+QENG="servingcell";+QCAINFO;+QGDNRCNT?;+QGDCNT?;+CGCONTRDP=1;+QRSRP`}
	case atKeyDeviceInfo:
		return []string{
			`AT+CGMI;+CGSN;+QGMR;+CIMI;+ICCID;+CNUM`,
			`AT+QSIMSTAT?;+CPIN?;+QMAP="WWAN"`,
			`AT+QMAP="LANIP"`,
		}
	case atKeyNetworkBands:
		return []string{`AT+QNWPREFCFG="lte_band";+QNWPREFCFG= "nsa_nr5g_band";+QNWPREFCFG= "nr5g_band"`}
	case atKeyNetworkSettings:
		return []string{
			`AT+QUIMSLOT?;+QNWPREFCFG="mode_pref";+QNWPREFCFG="nr5g_disable_mode";+CGDCONT?;+CGCONTRDP=1;+QNWLOCK="common/4g";+QNWLOCK="common/5g"`,
			`AT+QCAINFO`,
		}
	case atKeySettingsStatus:
		return []string{
			`AT+QMAP="MPDN_RULE";+QMAP="DHCPV6DNS";+QCFG="usbnet";+QMAP="DMZ";+QMAP="DHCPV4DNS"`,
			`AT+CGSN`,
			`AT+QMAP="LANIP"`,
		}
	case atKeyModel:
		return []string{`AT+CGMM`}
	case atKeySIMStatus:
		return []string{`AT+CPIN?`}
	case atKeySMSList:
		return []string{smsListATCommand()}
	case atKeyIMSI:
		return []string{`AT+CIMI`}
	default:
		return nil
	}
}

func pageATCommand(key string) string {
	return strings.Join(pageATCommands(key), "\n")
}

func (s *simpleAdminServer) fetchPageAT(key string, force bool, wait bool) string {
	commands := pageATCommands(key)
	if len(commands) == 0 {
		return ""
	}
	responses := make([]string, 0, len(commands))
	for _, command := range commands {
		resp := s.fetchATCommand(command, force, wait)
		if strings.TrimSpace(resp) != "" {
			responses = append(responses, resp)
		}
	}
	return strings.Join(responses, "\n")
}

func (s *simpleAdminServer) fetchATCommand(command string, force bool, wait bool) string {
	if command == "" {
		return ""
	}
	atCommandCache.Start(s.cfg.mockMode)
	return atCommandCache.Fetch(command, force, &wait)
}

func (s *simpleAdminServer) runPageAction(command string) string {
	atCommandCache.Start(s.cfg.mockMode)
	wait := true
	return atCommandCache.Fetch(command, true, &wait)
}

func (s *simpleAdminServer) runPageActionsOK(commands []string, delay time.Duration) (string, bool) {
	responses := make([]string, 0, len(commands))
	for i, command := range commands {
		resp := s.runPageAction(command)
		responses = append(responses, resp)
		if !atResponseOK(resp) {
			return strings.Join(responses, "\n"), false
		}
		if delay > 0 && i < len(commands)-1 {
			time.Sleep(delay)
		}
	}
	return strings.Join(responses, "\n"), true
}

func (s *simpleAdminServer) runDelayedPageAction(command string, delay time.Duration) {
	go func() {
		if delay > 0 {
			time.Sleep(delay)
		}
		s.runPageAction(command)
	}()
}

func (s *simpleAdminServer) runDelayedPageActionsOK(commands []string, startDelay time.Duration, stepDelay time.Duration) {
	go func() {
		if startDelay > 0 {
			time.Sleep(startDelay)
		}
		s.runPageActionsOK(commands, stepDelay)
	}()
}

func settingsRebootNoticeResponse(response string, rebootAfter time.Duration) map[string]any {
	rebootAfterSeconds := int(rebootAfter / time.Second)
	return map[string]any{
		"ok":                     true,
		"response":               response,
		"reboot":                 true,
		"rebooting":              true,
		"rebootAfterSeconds":     rebootAfterSeconds,
		"rebootCountdownSeconds": 40,
		"message":                "设备即将重启，请等待前端倒计时。",
	}
}

func (s *simpleAdminServer) handleDashboardData(w http.ResponseWriter, r *http.Request) {
	raw := s.fetchPageAT(atKeyDashboard, boolQuery(r, "force", false), true)
	data := parseDashboardAT(raw)
	if boolQuery(r, "debug", false) {
		data["raw"] = raw
	}
	data["internetConnection"] = "未连接"
	hasIP := stringValue(data["ipv4"]) != "-" || stringValue(data["ipv6"]) != "-"
	if hasIP {
		if s.cfg.mockMode || nativePing(r.Context()) {
			data["internetConnection"] = "已连接"
		}
	}
	data["uptimeParts"] = parseUptimeParts(nativeOrMockUptime(s.cfg.mockMode))
	for key, value := range systemResourceMetrics(s.cfg.mockMode) {
		data[key] = value
	}
	data["lastUpdate"] = time.Now().Format("2006/01/02 15:04:05")
	writeJSON(w, http.StatusOK, data)
}

func nativeOrMockUptime(mock bool) string {
	if mock {
		return mockUptimeText()
	}
	return nativeUptimeText()
}

func (s *simpleAdminServer) cachedStandaloneModel() string {
	s.modelMu.RLock()
	defer s.modelMu.RUnlock()
	return s.cachedModuleModel
}

func (s *simpleAdminServer) storeStandaloneModel(model string) {
	model = strings.TrimSpace(model)
	if model == "" || model == "-" {
		return
	}
	s.modelMu.Lock()
	s.cachedModuleModel = model
	s.modelMu.Unlock()
}

func (s *simpleAdminServer) fetchStandaloneModel(force bool) (string, bool) {
	if model := s.cachedStandaloneModel(); model != "" {
		return model, false
	}

	attempts := 1
	if force {
		attempts = 3
	}
	pending := false
	for i := 0; i < attempts; i++ {
		raw := s.fetchPageAT(atKeyModel, i > 0, true)
		model := parseModelAT(raw)
		if model != "-" {
			s.storeStandaloneModel(model)
			return model, false
		}
		pending = pending || strings.Contains(raw, atCachePendingText) || strings.TrimSpace(raw) == ""
		if i < attempts-1 {
			time.Sleep(700 * time.Millisecond)
		}
	}
	return "-", pending
}

func (s *simpleAdminServer) handleModuleModel(w http.ResponseWriter, r *http.Request) {
	force := boolQuery(r, "force", true)
	model, pending := s.fetchStandaloneModel(force)
	writeJSON(w, http.StatusOK, map[string]any{"model": model, "pending": pending})
}

func (s *simpleAdminServer) handleDeviceInfoData(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimSpace(requestValue(r, "action"))
	if action == "" || action == "get" {
		force := boolQuery(r, "force", false)
		raw := s.fetchPageAT(atKeyDeviceInfo, force, true)
		model, modelPending := s.fetchStandaloneModel(force)
		data := parseDeviceInfoAT(raw)
		if model != "-" {
			data["modelName"] = model
		}
		data["pending"] = strings.Contains(raw, atCachePendingText) || modelPending
		writeJSON(w, http.StatusOK, data)
		return
	}
	if action == "set_imei" {
		imei := stripNonDigits(requestValue(r, "imei"))
		if len(imei) != 15 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid imei"})
			return
		}
		resp := s.runPageAction(fmt.Sprintf(`AT+EGMR=1,7,"%s";+CFUN=1,1`, imei))
		writeJSON(w, http.StatusOK, map[string]any{"ok": atResponseOK(resp), "response": resp})
		return
	}
	writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "unsupported action"})
}

func (s *simpleAdminServer) handleNetworkData(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimSpace(requestValue(r, "action"))
	switch action {
	case "", "settings":
		raw := s.fetchPageAT(atKeyNetworkSettings, boolQuery(r, "force", false), true)
		writeJSON(w, http.StatusOK, parseNetworkSettingsAT(raw))
	case "model":
		model, pending := s.fetchStandaloneModel(true)
		writeJSON(w, http.StatusOK, map[string]any{"model": model, "pending": pending})
	case "bands":
		force := boolQuery(r, "force", false)
		wait := boolQuery(r, "wait", true)
		mode := strings.ToUpper(strings.TrimSpace(requestValue(r, "mode")))
		if mode != "" {
			command := networkBandQueryCommand(mode)
			if command == "" {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid mode"})
				return
			}
			raw := s.fetchATCommand(command, force, wait)
			writeJSON(w, http.StatusOK, networkBandsResponse(raw))
			return
		}
		raw := s.fetchPageAT(atKeyNetworkBands, force, wait)
		writeJSON(w, http.StatusOK, networkBandsResponse(raw))
	case "scan":
		mode := requestValue(r, "mode")
		command := scanModeCommand(mode)
		if command == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid scan mode"})
			return
		}
		raw := s.runPageAction(command)
		writeJSON(w, http.StatusOK, parseCellScanAT(raw))
	case "lock_scanned_cells":
		resp, err := s.lockScannedCells(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": atResponseOK(resp), "response": resp})
	case "lock_bands":
		mode := strings.ToUpper(strings.TrimSpace(requestValue(r, "mode")))
		values := strings.TrimSpace(requestValue(r, "values"))
		if values == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "missing values"})
			return
		}
		target := map[string]string{"LTE": "lte_band", "NSA": "nsa_nr5g_band", "SA": "nr5g_band"}[mode]
		if target == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid mode"})
			return
		}
		resp := s.runPageAction(fmt.Sprintf(`AT+QNWPREFCFG="%s",%s`, target, safeBandList(values)))
		writeJSON(w, http.StatusOK, map[string]any{"ok": atResponseOK(resp), "response": resp})
	case "reset_bands":
		lte := safeBandList(requestValue(r, "lte"))
		nsa := safeBandList(requestValue(r, "nsa"))
		sa := safeBandList(requestValue(r, "sa"))
		resp := s.runPageAction(fmt.Sprintf(`AT+QNWPREFCFG="lte_band",%s;+QNWPREFCFG= "nsa_nr5g_band",%s;+QNWPREFCFG= "nr5g_band",%s`, lte, nsa, sa))
		writeJSON(w, http.StatusOK, map[string]any{"ok": atResponseOK(resp), "response": resp})
	case "save_settings":
		resp, err := s.saveNetworkSettings(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": atResponseOK(resp), "response": resp})
	case "lock_lte_manual":
		resp, err := s.lockLTEManual(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": atResponseOK(resp), "response": resp})
	case "lock_nr_manual":
		pci := stripNonDigits(requestValue(r, "pci"))
		earfcn := stripNonDigits(requestValue(r, "earfcn"))
		scs := stripNonDigits(requestValue(r, "scs"))
		band := stripNonDigits(requestValue(r, "band"))
		if pci == "" || earfcn == "" || scs == "" || band == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "missing nr lock parameters"})
			return
		}
		resp := s.runPageAction(fmt.Sprintf(`AT+QNWLOCK="common/5g",%s,%s,%s,%s`, pci, earfcn, scs, band))
		writeJSON(w, http.StatusOK, map[string]any{"ok": atResponseOK(resp), "response": resp})
	case "unlock_lte":
		resp := s.runPageAction(`AT+QNWLOCK="common/4g",0`)
		writeJSON(w, http.StatusOK, map[string]any{"ok": atResponseOK(resp), "response": resp})
	case "unlock_nr":
		resp := s.runPageAction(`AT+QNWLOCK="common/5g",0`)
		writeJSON(w, http.StatusOK, map[string]any{"ok": atResponseOK(resp), "response": resp})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "unsupported action"})
	}
}

func (s *simpleAdminServer) handleSettingsData(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimSpace(requestValue(r, "action"))
	switch action {
	case "", "status":
		raw := s.fetchPageAT(atKeySettingsStatus, boolQuery(r, "force", false), true)
		writeJSON(w, http.StatusOK, parseSettingsStatusAT(raw))
	case "manual_at":
		// AT 终端是用户手动输入场景，保留显式 AT 调试能力；普通页面状态/动作均不再提交完整 AT。
		command := sanitizeATCommand(requestValue(r, "command"))
		if command == "" {
			command = "ATI"
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "response": s.runPageAction(command)})
	case "set_imei":
		imei := stripNonDigits(requestValue(r, "imei"))
		if len(imei) != 15 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid imei"})
			return
		}
		resp := s.runPageAction(fmt.Sprintf(`AT+EGMR=1,7,"%s";+CFUN=1,1`, imei))
		writeJSON(w, http.StatusOK, map[string]any{"ok": atResponseOK(resp), "response": resp})
	case "reboot":
		resp := s.runPageAction("AT+CFUN=1,1")
		writeJSON(w, http.StatusOK, map[string]any{"ok": atResponseOK(resp), "response": resp})
	case "reset_at":
		resp := s.runPageAction("AT&F")
		writeJSON(w, http.StatusOK, map[string]any{"ok": atResponseOK(resp), "response": resp})
	case "ip_passthrough":
		mode := strings.ToUpper(strings.TrimSpace(requestValue(r, "mode")))
		enabled := boolQuery(r, "enabled", true)
		if !enabled {
			startDelay := time.Second
			stepDelay := time.Second
			commands := []string{
				`AT+QMAP="MPDN_RULE",0`,
				`AT+QMAPWAC=1`,
				`AT+CFUN=1,1`,
			}
			writeJSON(w, http.StatusOK, settingsRebootNoticeResponse("后台将开始禁用 IP 透传并重启设备。", startDelay))
			s.runDelayedPageActionsOK(commands, startDelay, stepDelay)
			return
		}

		var command string
		switch mode {
		case "ETH":
			command = `AT+QMAP="MPDN_RULE",0,1,0,1,1,"FF:FF:FF:FF:FF:FF"`
		case "USB":
			command = `AT+QMAP="MPDN_RULE",0,1,0,3,1,"FF:FF:FF:FF:FF:FF"`
		default:
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid passthrough mode"})
			return
		}
		resp := s.runPageAction(command)
		writeJSON(w, http.StatusOK, map[string]any{"ok": atResponseOK(resp), "response": resp})
	case "dns_proxy":
		family := strings.TrimSpace(requestValue(r, "family"))
		if family != "4" && family != "6" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid dns family"})
			return
		}
		state := "disable"
		if boolQuery(r, "enabled", false) {
			state = "enable"
		}
		resp := s.runPageAction(fmt.Sprintf(`AT+QMAP="DHCPV%sDNS","%s"`, family, state))
		writeJSON(w, http.StatusOK, map[string]any{"ok": atResponseOK(resp), "response": resp})
	case "usbnet":
		mode := strings.ToUpper(strings.TrimSpace(requestValue(r, "mode")))
		code := map[string]string{"RMNET": "0", "ECM": "1", "MBIM": "2", "RNDIS": "3"}[mode]
		if code == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid usbnet mode"})
			return
		}
		resp := s.runPageAction(fmt.Sprintf(`AT+QCFG="usbnet",%s`, code))
		writeJSON(w, http.StatusOK, map[string]any{"ok": atResponseOK(resp), "response": resp})
	case "dmz":
		enabled := boolQuery(r, "enabled", false)
		command := `AT+QMAP="DMZ",0`
		if enabled {
			ip := cleanIP(requestValue(r, "ip"))
			if ip == "" {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid dmz ip"})
				return
			}
			command = fmt.Sprintf(`AT+QMAP="DMZ",1,4,%s`, ip)
		}
		resp := s.runPageAction(command)
		writeJSON(w, http.StatusOK, map[string]any{"ok": atResponseOK(resp), "response": resp})
	case "lanip":
		start := cleanIP(requestValue(r, "start"))
		end := cleanIP(requestValue(r, "end"))
		gw := cleanIP(requestValue(r, "gateway"))
		if start == "" || end == "" || gw == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid lan ip"})
			return
		}
		resp := s.runPageAction(fmt.Sprintf(`AT+QMAP="LANIP",%s,%s,%s`, start, end, gw))
		writeJSON(w, http.StatusOK, map[string]any{"ok": atResponseOK(resp), "response": resp})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "unsupported action"})
	}
}

func (s *simpleAdminServer) handleSMSData(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimSpace(requestValue(r, "action"))
	switch action {
	case "", "list":
		raw := s.fetchPageAT(atKeySMSList, boolQuery(r, "force", false), true)
		writeJSON(w, http.StatusOK, parseSMSListAT(raw))
	case "list_meta":
		raw := s.fetchPageAT(atKeySMSList, boolQuery(r, "force", false), true)
		writeJSON(w, http.StatusOK, smsListMetaOnly(parseSMSListAT(raw)))
	case "delete_all":
		resp := s.runPageAction("AT+CMGD=,4")
		writeJSON(w, http.StatusOK, map[string]any{"ok": atResponseOK(resp), "response": resp})
	case "delete_indices":
		values := requestValues(r, "indices")
		if len(values) == 1 && strings.Contains(values[0], ",") {
			values = strings.Split(values[0], ",")
		}
		parts := make([]string, 0, len(values))
		for _, value := range values {
			index := stripNonDigits(value)
			if index != "" {
				parts = append(parts, index)
			}
		}
		if len(parts) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "missing indices"})
			return
		}
		commands := make([]string, len(parts))
		for i, index := range parts {
			if i == 0 {
				commands[i] = "AT+CMGD=" + index
			} else {
				commands[i] = "+CMGD=" + index
			}
		}
		resp := s.runPageAction(strings.Join(commands, ";"))
		writeJSON(w, http.StatusOK, map[string]any{"ok": atResponseOK(resp), "response": resp})
	case "sim_status":
		raw := s.fetchPageAT(atKeySIMStatus, true, true)
		inserted := !strings.Contains(strings.ToUpper(raw), "SIM NOT INSERTED") && !strings.Contains(strings.ToUpper(raw), "+CME ERROR: 10")
		writeJSON(w, http.StatusOK, map[string]any{"inserted": inserted})
	case "send":
		number := strings.TrimSpace(requestValue(r, "number"))
		message := requestValue(r, "message")
		result := s.sendSMSBusiness(number, message)
		status := http.StatusOK
		if ok, _ := result["ok"].(bool); !ok {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, result)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "unsupported action"})
	}
}

func smsListMetaOnly(data map[string]any) map[string]any {
	meta := map[string]any{}
	if centers, ok := data["serviceCenters"]; ok {
		meta["serviceCenters"] = centers
	}
	rawMessages, _ := data["messages"].([]map[string]any)
	messages := make([]map[string]any, 0, len(rawMessages))
	for _, msg := range rawMessages {
		item := map[string]any{}
		for _, key := range []string{"sender", "date", "indices", "concatTotal", "concatRef", "concatSeq"} {
			if value, ok := msg[key]; ok {
				item[key] = value
			}
		}
		messages = append(messages, item)
	}
	meta["messages"] = messages
	return meta
}

func (s *simpleAdminServer) sendSMSBusiness(number string, message string) map[string]any {
	message = strings.TrimSpace(message)
	if strings.TrimSpace(number) == "" || message == "" {
		return map[string]any{"ok": false, "error": "missing number or message"}
	}
	number, err := normalizeSMSNumber(number, s.fetchPageAT(atKeyIMSI, true, true))
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	if number == "" {
		return map[string]any{"ok": false, "error": "missing number or message"}
	}

	uid := int(time.Now().UnixNano() % 255)
	if uid <= 0 {
		uid = 1
	}
	segments := buildSMSSubmitPDUs(number, message, uid)
	if len(segments) == 0 {
		return map[string]any{"ok": false, "error": "empty message"}
	}

	responses := make([]string, 0, len(segments))
	for i, segment := range segments {
		sendCmd := fmt.Sprintf("AT+CMGF=0;+CMGS=%d", segment.TPDUOctets)
		var resp string
		var err error
		if s.cfg.mockMode {
			resp = mockSMSSendResponse(number)
		} else {
			resp, err = runSMSTransaction(sendCmd, segment.PDUHex)
		}
		responses = append(responses, resp)
		if err != nil && strings.TrimSpace(resp) == "" {
			return map[string]any{"ok": false, "error": err.Error(), "segment": i + 1}
		}
		if !atResponseOK(resp) {
			return map[string]any{"ok": false, "error": smsSendErrorKind(resp), "segment": i + 1}
		}
	}
	return map[string]any{"ok": true, "segments": len(segments), "number": number}
}

func splitSMSRunes(message string, size int) []string {
	if size <= 0 {
		size = 67
	}
	runes := []rune(message)
	segments := []string{}
	for len(runes) > 0 {
		end := size
		if len(runes) < end {
			end = len(runes)
		}
		segments = append(segments, string(runes[:end]))
		runes = runes[end:]
	}
	return segments
}

func smsSendErrorKind(text string) string {
	up := strings.ToUpper(strings.TrimSpace(text))
	if m := regexp.MustCompile(`\+(CMS|CME) ERROR:\s*([0-9]+)`).FindStringSubmatch(up); len(m) > 2 {
		return m[1] + " " + m[2]
	}
	if strings.Contains(up, "ERROR") {
		return "ERROR"
	}
	return "UNKNOWN"
}

func parseSMSServiceCenters(raw string) ([]string, []string) {
	serviceCenters := []string{}
	lines := strings.Split(strings.ReplaceAll(raw, "\r", ""), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "+CSCA:") {
			parts := csvFields(strings.TrimPrefix(line, "+CSCA:"))
			if len(parts) > 0 {
				serviceCenters = append(serviceCenters, decodeMaybeUCS2(parts[0]))
			}
		}
	}
	return serviceCenters, nil
}

func parseDashboardAT(raw string) map[string]any {
	lines := atLines(raw)
	data := map[string]any{
		"sim": "未激活", "temperature": "N/A", "active_sim": "-", "network_provider": "-", "mccmnc": "-", "apn": "-",
		"network_mode": "-", "ipv4": "-", "ipv6": "-", "bands": "-", "bandwidth": "-", "csq": "-", "rssi": "-",
		"cellID": "-", "eNBID": "-", "tac": "-", "rsrqLTE": "-", "rsrqNR": "-", "rsrpLTE": "-", "rsrpNR": "-",
		"sinrLTE": "-", "sinrNR": "-", "prxqrsrp": "None", "drxqrsrp": "None", "rx2qrsrp": "None", "rx3qrsrp": "None",
		"earfcns": "-", "pcc_pci": "-", "scc_pci": "-", "signalAssessment": "-", "nr_rx_bytes": 0, "nr_tx_bytes": 0,
		"nr_rx_human": "-", "nr_tx_human": "-", "nr_dl_speed": "-", "nr_ul_speed": "-", "signalPercentage": 0,
		"rsrqLTEPercentage": 0, "rsrqNRPercentage": 0, "rsrpLTEPercentage": 0, "rsrpNRPercentage": 0, "sinrLTEPercentage": 0, "sinrNRPercentage": 0,
	}

	tempSum, tempCount := 0, 0
	qcaLines := []string{}
	qrsrpLines := []string{}
	simStatusKnown := false
	simInserted := false
	simExplicitAbsent := false
	for _, line := range lines {
		upperLine := strings.ToUpper(line)
		if strings.Contains(upperLine, "SIM NOT INSERTED") || strings.Contains(upperLine, "+CME ERROR: 10") || strings.Contains(upperLine, "+CPIN: NOT INSERTED") {
			simStatusKnown = true
			simInserted = false
		}
		switch {
		case strings.HasPrefix(line, "+QTEMP:"):
			parts := csvFields(strings.TrimPrefix(line, "+QTEMP:"))
			if len(parts) > 1 {
				v, err := strconv.Atoi(strings.Trim(parts[1], `"`))
				if err == nil && v != -273 && v != 0 {
					tempSum += v
					tempCount++
				}
			}
		case strings.HasPrefix(line, "+QSIMSTAT:"):
			parts := strings.Split(strings.TrimPrefix(line, "+QSIMSTAT:"), ",")
			if len(parts) > 1 {
				simStatusKnown = true
				simInserted = strings.TrimSpace(parts[1]) == "1"
				if simInserted {
					data["sim"] = "已激活"
				} else {
					data["sim"] = "未激活"
				}
			}
		case strings.HasPrefix(line, "+CSQ:"):
			data["csq"] = strings.TrimSpace(strings.Split(strings.TrimPrefix(line, "+CSQ:"), ",")[0])
		case strings.HasPrefix(line, "+QUIMSLOT:"):
			data["active_sim"] = strings.TrimSpace(strings.TrimPrefix(line, "+QUIMSLOT:"))
		case strings.HasPrefix(line, "+QSPN:"):
			parts := csvFields(strings.TrimPrefix(line, "+QSPN:"))
			if len(parts) >= 5 {
				rawName := firstNonEmpty(parts[2], parts[0], "-")
				if len(parts) > 3 && strings.TrimSpace(parts[3]) != "0" {
					rawName = decodeMaybeUCS2(rawName)
				}
				data["network_provider"] = rawName
				data["mccmnc"] = parts[4]
			}
		case strings.HasPrefix(line, "+CGCONTRDP:"):
			parts := csvFields(strings.TrimPrefix(line, "+CGCONTRDP:"))
			if len(parts) > 2 && parts[2] != "" {
				data["apn"] = parts[2]
			}
			if len(parts) > 3 && parts[3] != "0.0.0.0" && parts[3] != "" && stringValue(data["ipv4"]) == "-" {
				data["ipv4"] = parts[3]
				data["sim"] = "已激活"
			}
			if len(parts) > 4 && parts[4] != "" && !isAllZeroV6(parts[4]) && stringValue(data["ipv6"]) == "-" {
				data["ipv6"] = parts[4]
				data["sim"] = "已激活"
			}
		case strings.HasPrefix(line, "+QMAP:") && strings.Contains(line, `"WWAN"`) && strings.Contains(line, `"IPV4"`):
			parts := csvFields(strings.TrimPrefix(line, "+QMAP:"))
			if len(parts) > 4 && parts[4] != "" {
				data["ipv4"] = parts[4]
				data["sim"] = "已激活"
			}
		case strings.HasPrefix(line, "+QMAP:") && strings.Contains(line, `"WWAN"`) && strings.Contains(line, `"IPV6"`):
			parts := csvFields(strings.TrimPrefix(line, "+QMAP:"))
			if len(parts) > 4 && parts[4] != "" {
				data["ipv6"] = parts[4]
				data["sim"] = "已激活"
			}
		case strings.HasPrefix(line, "+QENG:"):
			applyQENGDashboard(line, data)
		case strings.HasPrefix(line, "+QCAINFO:"):
			qcaLines = append(qcaLines, line)
		case strings.HasPrefix(line, "+QRSRP:"):
			qrsrpLines = append(qrsrpLines, line)
		case strings.HasPrefix(line, "+QGDNRCNT:"):
			parts := strings.Split(strings.TrimSpace(strings.TrimPrefix(line, "+QGDNRCNT:")), ",")
			if len(parts) >= 2 {
				rx, _ := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
				tx, _ := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
				data["nr_rx_bytes"] = rx
				data["nr_tx_bytes"] = tx
				data["nr_rx_human"] = humanBytesGo(float64(rx))
				data["nr_tx_human"] = humanBytesGo(float64(tx))
			}
		}
	}
	if tempCount > 0 {
		data["temperature"] = strconv.Itoa((tempSum + tempCount/2) / tempCount)
	}
	if simExplicitAbsent || (simStatusKnown && !simInserted) {
		applyDashboardSIMAbsent(data)
		return data
	}
	applyQCAInfoDashboard(qcaLines, data)
	applyQRSRPDashboard(qrsrpLines, data)
	bestRsrp := maxInt(percentFromAny(data["rsrpLTEPercentage"]), percentFromAny(data["rsrpNRPercentage"]))
	bestRsrq := maxInt(percentFromAny(data["rsrqLTEPercentage"]), percentFromAny(data["rsrqNRPercentage"]))
	bestSinr := maxInt(percentFromAny(data["sinrLTEPercentage"]), percentFromAny(data["sinrNRPercentage"]))
	if bestRsrp > 0 || bestRsrq > 0 || bestSinr > 0 {
		sig := weightedSignalPercent(bestRsrp, bestRsrq, bestSinr)
		data["signalPercentage"] = sig
		data["signalAssessment"] = signalQualityGo(sig)
	}
	return data
}

func applyDashboardSIMAbsent(data map[string]any) {
	reset := map[string]any{
		"sim": "未激活", "active_sim": "-", "network_provider": "-", "mccmnc": "-", "apn": "-",
		"network_mode": "未插卡", "ipv4": "-", "ipv6": "-", "bands": "-", "bandwidth": "-", "csq": "-", "rssi": "-",
		"cellID": "-", "eNBID": "-", "tac": "-", "rsrqLTE": "-", "rsrqNR": "-", "rsrpLTE": "-", "rsrpNR": "-",
		"sinrLTE": "-", "sinrNR": "-", "prxqrsrp": "-", "drxqrsrp": "-", "rx2qrsrp": "-", "rx3qrsrp": "-",
		"earfcns": "-", "pcc_pci": "-", "scc_pci": "-", "signalAssessment": "未知", "nr_rx_bytes": int64(0), "nr_tx_bytes": int64(0),
		"nr_rx_human": "-", "nr_tx_human": "-", "nr_dl_speed": "-", "nr_ul_speed": "-", "signalPercentage": 0,
		"rsrqLTEPercentage": 0, "rsrqNRPercentage": 0, "rsrpLTEPercentage": 0, "rsrpNRPercentage": 0, "sinrLTEPercentage": 0, "sinrNRPercentage": 0,
	}
	for key, value := range reset {
		data[key] = value
	}
}

func parseDeviceInfoAT(raw string) map[string]any {
	lines := atLines(raw)
	data := map[string]any{"manufacturer": "-", "modelName": "-", "firmwareVersion": "-", "simStatus": "未知", "simInserted": false, "imsi": "-", "iccid": "-", "imei": "-", "lanIp": "-", "wwanIpv4": "-", "wwanIpv6": "-", "phoneNumber": "-"}
	plain := []string{}
	simStatusKnown := false
	simInserted := false
	simExplicitAbsent := false
	for _, line := range lines {
		up := strings.ToUpper(line)
		if strings.Contains(up, "SIM NOT INSERTED") || strings.Contains(up, "+CME ERROR: 10") || strings.Contains(up, "+CPIN: NOT INSERTED") {
			simStatusKnown = true
			simInserted = false
			simExplicitAbsent = true
		}
		switch {
		case strings.HasPrefix(up, "+QSIMSTAT:"):
			parts := strings.Split(strings.TrimPrefix(line, "+QSIMSTAT:"), ",")
			if len(parts) > 1 {
				simStatusKnown = true
				simInserted = strings.TrimSpace(parts[1]) == "1"
				if !simInserted {
					simExplicitAbsent = true
				}
			}
		case strings.HasPrefix(up, "+CPIN:"):
			simStatusKnown = true
			simInserted = strings.Contains(up, "READY")
			if !simInserted {
				simExplicitAbsent = true
			}
		case strings.HasPrefix(up, "+CGSN:"):
			if imei := extractIMEI(line); imei != "" {
				data["imei"] = imei
			}
		case strings.HasPrefix(up, "+ICCID:"):
			if iccid := strings.TrimSpace(strings.TrimPrefix(line, "+ICCID:")); iccid != "" {
				data["iccid"] = iccid
				simStatusKnown = true
				simInserted = true
			}
		case strings.HasPrefix(line, `+QMAP: "LANIP"`):
			parts := csvFields(strings.TrimPrefix(line, "+QMAP:"))
			if len(parts) > 3 {
				data["lanIp"] = parts[3]
			}
		case strings.HasPrefix(line, `+QMAP: "WWAN"`) && strings.Contains(line, `"IPV4"`):
			parts := csvFields(strings.TrimPrefix(line, "+QMAP:"))
			if len(parts) > 4 {
				data["wwanIpv4"] = parts[4]
			}
		case strings.HasPrefix(line, `+QMAP: "WWAN"`) && strings.Contains(line, `"IPV6"`):
			parts := csvFields(strings.TrimPrefix(line, "+QMAP:"))
			if len(parts) > 4 {
				data["wwanIpv6"] = parts[4]
			}
		case strings.HasPrefix(up, "+CNUM:"):
			parts := csvFields(strings.TrimPrefix(line, "+CNUM:"))
			for _, part := range parts {
				if strings.HasPrefix(part, "+") || len(stripNonDigits(part)) >= 5 {
					data["phoneNumber"] = part
					break
				}
			}
		case isPlainATValue(line):
			plain = append(plain, line)
		}
	}
	nonDigits := []string{}
	digits := []string{}
	for _, p := range plain {
		if regexp.MustCompile(`^\d{10,20}$`).MatchString(p) {
			digits = append(digits, p)
		} else {
			nonDigits = append(nonDigits, p)
		}
	}
	if len(nonDigits) > 0 {
		data["manufacturer"] = nonDigits[0]
	}
	if len(nonDigits) > 2 {
		data["modelName"] = nonDigits[1]
		data["firmwareVersion"] = nonDigits[2]
	} else if len(nonDigits) > 1 {
		data["firmwareVersion"] = nonDigits[1]
	}
	if data["imei"] == "-" {
		for _, d := range digits {
			if len(d) >= 14 && len(d) <= 17 {
				data["imei"] = d
				break
			}
		}
	}
	for _, d := range digits {
		if d != data["imei"] && len(d) >= 14 && len(d) <= 16 {
			data["imsi"] = d
			simStatusKnown = true
			simInserted = true
			break
		}
	}
	if simExplicitAbsent || (simStatusKnown && !simInserted) {
		data["simStatus"] = "未插卡"
		data["simInserted"] = false
		data["imsi"] = "-"
		data["iccid"] = "-"
		data["wwanIpv4"] = "-"
		data["wwanIpv6"] = "-"
		data["phoneNumber"] = "未插卡"
		return data
	}
	if simInserted {
		data["simStatus"] = "已插卡"
		data["simInserted"] = true
		if stringValue(data["phoneNumber"]) == "-" {
			data["phoneNumber"] = "无本机号码"
		}
	}
	return data
}

func networkBandQueryCommand(mode string) string {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case "LTE":
		return `AT+QNWPREFCFG="lte_band"`
	case "NSA":
		return `AT+QNWPREFCFG="nsa_nr5g_band"`
	case "SA":
		return `AT+QNWPREFCFG="nr5g_band"`
	default:
		return ""
	}
}

func networkBandsResponse(raw string) map[string]any {
	data := parseNetworkBandsAT(raw)
	if strings.Contains(raw, atCachePendingText) {
		data["pending"] = true
	}
	if atReadErrorText(raw) {
		data["error"] = strings.TrimSpace(raw)
	}
	return data
}

func parseNetworkBandsAT(raw string) map[string]any {
	locked := map[string]string{"lte_band": "", "nsa_nr5g_band": "", "nr5g_band": ""}
	re := regexp.MustCompile(`"([^"]+)"\s*,\s*([0-9:]+)`)
	for _, m := range re.FindAllStringSubmatch(raw, -1) {
		locked[m[1]] = m[2]
	}
	return map[string]any{"locked_lte_bands": locked["lte_band"], "locked_nsa_bands": locked["nsa_nr5g_band"], "locked_sa_bands": locked["nr5g_band"]}
}

func atReadErrorText(raw string) bool {
	text := strings.ToLower(strings.TrimSpace(raw))
	if text == "" || strings.Contains(raw, atCachePendingText) {
		return false
	}
	return strings.Contains(text, "timeout waiting") || strings.Contains(text, "failed") || strings.Contains(text, "error")
}

func parseNetworkSettingsAT(raw string) map[string]any {
	lines := atLines(raw)
	out := map[string]any{"sim": "-", "apn": "-", "cellLockStatus": "未锁定", "prefNetwork": "-", "nrModeControl": "未禁用", "nrModeControlNum": "0", "bands": "-", "pdpType": "-"}
	lock4g, lock5g := "0", "0"
	bandItems := []struct {
		typ, band string
		idx       int
	}{}
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "+QUIMSLOT:"):
			out["sim"] = strings.TrimSpace(strings.TrimPrefix(line, "+QUIMSLOT:"))
		case strings.HasPrefix(line, "+CGCONTRDP:"):
			parts := csvFields(strings.TrimPrefix(line, "+CGCONTRDP:"))
			if len(parts) > 2 && parts[2] != "" {
				out["apn"] = parts[2]
			}
		case strings.HasPrefix(line, "+CGDCONT:") && strings.Contains(line, "1,"):
			parts := csvFields(strings.TrimPrefix(line, "+CGDCONT:"))
			if len(parts) > 1 {
				out["pdpType"] = parts[1]
			}
			if len(parts) > 2 && stringValue(out["apn"]) == "-" {
				out["apn"] = parts[2]
			}
		case strings.HasPrefix(line, `+QNWLOCK: "common/4g"`):
			if m := regexp.MustCompile(`,\s*([0-9]+)`).FindStringSubmatch(line); len(m) > 1 {
				lock4g = m[1]
			}
		case strings.HasPrefix(line, `+QNWLOCK: "common/5g"`):
			if m := regexp.MustCompile(`,\s*([0-9]+)`).FindStringSubmatch(line); len(m) > 1 {
				lock5g = m[1]
			}
		case strings.HasPrefix(line, `+QNWPREFCFG: "mode_pref"`):
			parts := csvFields(strings.TrimPrefix(line, "+QNWPREFCFG:"))
			if len(parts) > 1 {
				out["prefNetwork"] = parts[1]
			}
		case strings.HasPrefix(line, `+QNWPREFCFG: "nr5g_disable_mode"`):
			parts := csvFields(strings.TrimPrefix(line, "+QNWPREFCFG:"))
			if len(parts) > 1 {
				out["nrModeControlNum"] = parts[1]
			}
		case strings.HasPrefix(line, "+QCAINFO:"):
			parts := csvFields(strings.TrimPrefix(line, "+QCAINFO:"))
			if len(parts) > 3 {
				bandItems = append(bandItems, struct {
					typ, band string
					idx       int
				}{strings.ToUpper(parts[0]), parts[3], i})
			}
		}
	}
	if lock4g != "0" && lock5g != "0" {
		out["cellLockStatus"] = "已锁定4G和5G"
	} else if lock4g != "0" {
		out["cellLockStatus"] = "已锁定4G"
	} else if lock5g != "0" {
		out["cellLockStatus"] = "已锁定5G"
	}
	switch out["nrModeControlNum"] {
	case "1":
		out["nrModeControl"] = "禁用SA"
	case "2":
		out["nrModeControl"] = "禁用NSA"
	}
	sort.SliceStable(bandItems, func(i, j int) bool { return bandOrder(bandItems[i].typ) < bandOrder(bandItems[j].typ) })
	bands := []string{}
	for _, item := range bandItems {
		if item.band != "" {
			bands = append(bands, item.band)
		}
	}
	if len(bands) > 0 {
		out["bands"] = strings.Join(bands, " / ")
	}
	return out
}

func parseSettingsStatusAT(raw string) map[string]any {
	lines := atLines(raw)
	out := map[string]any{"ipPassStatus": false, "DNSV6ProxyStatus": false, "DNSV4ProxyStatus": false, "currentUsbNetMode": "未知", "dmzMode": "0", "dmzIP": "", "lanIpStart": "", "lanIpEnd": "", "lanGwIp": "", "imei": "-"}
	for _, line := range lines {
		switch {
		case isQMAPRecord(line, "MPDN_rule"):
			parts := csvFields(strings.TrimPrefix(line, "+QMAP:"))
			if len(parts) > 2 && parts[2] == "1" {
				out["ipPassStatus"] = true
			}
		case isQMAPRecord(line, "DHCPV6DNS"):
			out["DNSV6ProxyStatus"] = strings.Contains(strings.ToLower(line), `"enable"`)
		case isQMAPRecord(line, "DHCPV4DNS"):
			out["DNSV4ProxyStatus"] = strings.Contains(strings.ToLower(line), `"enable"`)
		case strings.Contains(line, `"usbnet"`):
			if m := regexp.MustCompile(`"usbnet"\s*,\s*(\d)`).FindStringSubmatch(line); len(m) > 1 {
				out["currentUsbNetMode"] = map[string]string{"0": "RMNET", "1": "ECM", "2": "MBIM", "3": "RNDIS"}[m[1]]
			}
		case isQMAPRecord(line, "DMZ"):
			parts := csvFields(strings.TrimPrefix(line, "+QMAP:"))
			if len(parts) > 1 {
				out["dmzMode"] = parts[1]
			}
			if len(parts) > 3 && parts[1] == "1" {
				out["dmzIP"] = parts[3]
			}
		case isQMAPRecord(line, "LANIP"):
			parts := csvFields(strings.TrimPrefix(line, "+QMAP:"))
			if len(parts) > 3 {
				out["lanIpStart"] = lastIPPart(parts[1])
				out["lanIpEnd"] = lastIPPart(parts[2])
				out["lanGwIp"] = parts[3]
			}
		case strings.HasPrefix(strings.ToUpper(line), "+CGSN:"):
			if imei := extractIMEI(line); imei != "" {
				out["imei"] = imei
			}
		case isPlainATValue(line):
			if out["imei"] == "-" {
				if imei := extractIMEI(line); imei != "" {
					out["imei"] = imei
				}
			}
		}
	}
	return out
}

func parseCellScanAT(raw string) map[string]any {
	result := map[string]any{"nr5g_cells_parsed": []map[string]string{}, "lte_cells_parsed": []map[string]string{}}
	nr := []map[string]string{}
	lte := []map[string]string{}
	for _, line := range atLines(raw) {
		if !strings.Contains(line, "+QSCAN:") {
			continue
		}
		typ := "LTE"
		if strings.Contains(strings.ToUpper(line), "NR5G") {
			typ = "NR5G"
		}
		parts := csvFields(strings.TrimPrefix(line, "+QSCAN:"))
		if len(parts) < 13 {
			continue
		}
		cell := map[string]string{"type": typ, "provider": networkName(parts[1], parts[2]), "band": parts[12], "freq": parts[3], "pci": parts[4], "rsrp": parts[5]}
		if typ == "NR5G" {
			nr = append(nr, cell)
		} else {
			lte = append(lte, cell)
		}
	}
	result["nr5g_cells_parsed"] = nr
	result["lte_cells_parsed"] = lte
	return result
}

func parseSMSListAT(raw string) map[string]any {
	entries := []map[string]any{}
	lines := strings.Split(strings.ReplaceAll(raw, "\r", ""), "\n")
	serviceCenters := []string{}
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "+CSCA:") {
			parts := csvFields(strings.TrimPrefix(line, "+CSCA:"))
			if len(parts) > 0 {
				serviceCenters = append(serviceCenters, decodeMaybeUCS2(parts[0]))
			}
		}
		if !strings.HasPrefix(line, "+CMGL:") {
			continue
		}
		parts := csvFields(strings.TrimPrefix(line, "+CMGL:"))
		if len(parts) < 1 {
			continue
		}
		idx, _ := strconv.Atoi(parts[0])
		sender := ""
		if len(parts) > 2 {
			sender = decodeMaybeUCS2(parts[2])
		}
		date := ""
		if len(parts) > 4 {
			date = parts[4]
		}
		if date == "" && len(parts) > 3 {
			date = parts[3]
		}
		bodyLines := []string{}
		for j := i + 1; j < len(lines); j++ {
			next := strings.TrimSpace(lines[j])
			if strings.HasPrefix(next, "+CMGL:") {
				break
			}
			if next == "" || next == "OK" || strings.HasPrefix(next, "+") || strings.HasPrefix(strings.ToUpper(next), "AT+") {
				continue
			}
			bodyLines = append(bodyLines, next)
			i = j
		}
		if len(bodyLines) > 0 && isHexLine(bodyLines[0]) {
			if pdu, ok := parseSMSDeliverPDU(bodyLines[0]); ok {
				if pdu.ServiceCenter != "" {
					serviceCenters = append(serviceCenters, pdu.ServiceCenter)
				}
				entry := map[string]any{"sender": pdu.Sender, "date": pdu.Date, "text": pdu.Text, "indices": []int{idx}}
				if pdu.ConcatRef != "" {
					entry["concatRef"] = pdu.Sender + ":" + pdu.ConcatRef
					entry["concatTotal"] = pdu.ConcatTotal
					entry["concatSeq"] = pdu.ConcatSeq
				}
				entries = append(entries, entry)
				continue
			}
		}
		text := decodeSMSBodyLines(bodyLines)
		entries = append(entries, map[string]any{"sender": sender, "date": date, "text": text, "indices": []int{idx}})
	}
	messages := mergeSMSFragments(entries)
	for _, message := range messages {
		if text, ok := message["text"].(string); ok {
			normalized := normalizeSMSLineBreaks(text)
			message["text"] = normalized
			message["textLines"] = splitSMSDisplayLines(normalized)
		}
	}
	return map[string]any{"messages": messages, "serviceCenters": serviceCenters}
}

func decodeSMSBodyLines(lines []string) string {
	compact := strings.Join(lines, "")
	decoded := decodeMaybeUCS2(compact)
	if decoded != compact {
		return normalizeSMSLineBreaks(decoded)
	}
	return normalizeSMSLineBreaks(strings.Join(lines, "\n"))
}

func normalizeSMSLineBreaks(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.ReplaceAll(text, "\v", "\n")
	text = strings.ReplaceAll(text, "\f", "\n")
	text = strings.ReplaceAll(text, "\u0085", "\n")
	text = strings.ReplaceAll(text, "\u2028", "\n")
	text = strings.ReplaceAll(text, "\u2029", "\n")
	text = strings.ReplaceAll(text, `\r\n`, "\n")
	text = strings.ReplaceAll(text, `\n`, "\n")
	text = strings.ReplaceAll(text, `\r`, "\n")
	return text
}

func splitSMSDisplayLines(text string) []string {
	return strings.Split(normalizeSMSLineBreaks(text), "\n")
}

func mergeSMSFragments(entries []map[string]any) []map[string]any {
	merged := []map[string]any{}
	concatGroups := map[string][]map[string]any{}
	concatFirstIndex := map[string]int{}
	for i, entry := range entries {
		ref := strings.TrimSpace(fmt.Sprint(entry["concatRef"]))
		if ref == "" {
			continue
		}
		if _, ok := concatGroups[ref]; !ok {
			concatFirstIndex[ref] = i
		}
		concatGroups[ref] = append(concatGroups[ref], entry)
	}

	group := []map[string]any{}
	flush := func() {
		if len(group) == 0 {
			return
		}
		merged = append(merged, buildSMSFragmentGroup(group))
		group = nil
	}
	for i, entry := range entries {
		ref := strings.TrimSpace(fmt.Sprint(entry["concatRef"]))
		if ref != "" {
			if concatFirstIndex[ref] == i {
				flush()
				merged = append(merged, buildSMSFragmentGroup(concatGroups[ref]))
			}
			continue
		}
		if len(group) == 0 || isSameSMSFragmentGroup(group[len(group)-1], entry) {
			group = append(group, entry)
			continue
		}
		flush()
		group = append(group, entry)
	}
	flush()
	return merged
}

func buildSMSFragmentGroup(group []map[string]any) map[string]any {
	if len(group) == 1 {
		return group[0]
	}
	ordered := orderSMSFragmentsByIndex(group)
	texts := make([]string, 0, len(ordered))
	indices := []int{}
	for _, item := range ordered {
		texts = append(texts, fmt.Sprint(item["text"]))
		indices = append(indices, toIntSlice(item["indices"])...)
	}
	sort.Ints(indices)
	message := map[string]any{
		"sender":  group[0]["sender"],
		"date":    group[0]["date"],
		"text":    strings.Join(texts, ""),
		"indices": indices,
	}
	if total := toInt(fmt.Sprint(group[0]["concatTotal"])); total > 1 {
		message["concatTotal"] = total
	}
	if concatRef := strings.TrimSpace(fmt.Sprint(group[0]["concatRef"])); concatRef != "" {
		message["concatRef"] = concatRef
	}
	return message
}

func isSameSMSFragmentGroup(a, b map[string]any) bool {
	senderA := strings.TrimSpace(fmt.Sprint(a["sender"]))
	senderB := strings.TrimSpace(fmt.Sprint(b["sender"]))
	if senderA == "" || senderB == "" || senderA != senderB {
		return false
	}
	concatA := strings.TrimSpace(fmt.Sprint(a["concatRef"]))
	concatB := strings.TrimSpace(fmt.Sprint(b["concatRef"]))
	if concatA != "" || concatB != "" {
		return concatA != "" && concatA == concatB
	}
	if !isSMSDateWithinSeconds(fmt.Sprint(a["date"]), fmt.Sprint(b["date"]), 5) {
		return false
	}
	prevIdx := lastSMSIndex(a["indices"])
	nextIdx := firstSMSIndex(b["indices"])
	return prevIdx >= 0 && nextIdx == prevIdx+1
}

func orderSMSFragmentsByIndex(group []map[string]any) []map[string]any {
	ordered := append([]map[string]any(nil), group...)
	sort.SliceStable(ordered, func(i, j int) bool {
		seqI := toInt(fmt.Sprint(ordered[i]["concatSeq"]))
		seqJ := toInt(fmt.Sprint(ordered[j]["concatSeq"]))
		if seqI > 0 || seqJ > 0 {
			return seqI < seqJ
		}
		return firstSMSIndex(ordered[i]["indices"]) < firstSMSIndex(ordered[j]["indices"])
	})
	return ordered
}

func isSMSDateWithinSeconds(a, b string, seconds int) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	ta, okA := parseSMSDateTime(a)
	tb, okB := parseSMSDateTime(b)
	if !okA || !okB {
		return false
	}
	diff := ta.Sub(tb)
	if diff < 0 {
		diff = -diff
	}
	return diff <= time.Duration(seconds)*time.Second
}

func parseSMSDateTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if len(value) < len("06/01/02,15:04:05") {
		return time.Time{}, false
	}
	base := value[:len("06/01/02,15:04:05")]
	parsed, err := time.ParseInLocation("06/01/02,15:04:05", base, time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

func appendIntSlices(a, b any) []int {
	result := toIntSlice(a)
	return append(result, toIntSlice(b)...)
}

func firstSMSIndex(value any) int {
	values := toIntSlice(value)
	if len(values) == 0 {
		return -1
	}
	return values[0]
}

func lastSMSIndex(value any) int {
	values := toIntSlice(value)
	if len(values) == 0 {
		return -1
	}
	return values[len(values)-1]
}

func toIntSlice(value any) []int {
	switch v := value.(type) {
	case []int:
		out := make([]int, len(v))
		copy(out, v)
		return out
	case []any:
		out := make([]int, 0, len(v))
		for _, item := range v {
			out = append(out, toInt(fmt.Sprint(item)))
		}
		return out
	default:
		return nil
	}
}

func parseModelAT(raw string) string {
	modelLineRE := regexp.MustCompile(`^[A-Za-z0-9._ -]+$`)
	for _, line := range atLines(raw) {
		value := strings.TrimSpace(line)
		up := strings.ToUpper(value)
		if strings.HasPrefix(up, "+CGMM:") {
			value = strings.TrimSpace(strings.TrimPrefix(value, value[:strings.Index(value, ":")+1]))
			value = strings.Trim(value, `"`)
			value = strings.TrimSpace(value)
			if value != "" && modelLineRE.MatchString(value) {
				return value
			}
			continue
		}
		if up == "OK" || up == "ERROR" || strings.HasPrefix(up, "AT+") || strings.HasPrefix(value, "+") {
			continue
		}
		value = strings.Trim(value, `"`)
		value = strings.TrimSpace(value)
		if value != "" && modelLineRE.MatchString(value) {
			return value
		}
	}
	return "-"
}

func scanModeCommand(mode string) string {
	switch mode {
	case "Full Scan":
		return "AT+QSCAN=3,1"
	case "LTE Only":
		return "AT+QSCAN=1,1"
	case "NR5G Only":
		return "AT+QSCAN=2,1"
	default:
		return ""
	}
}

func (s *simpleAdminServer) lockScannedCells(r *http.Request) (string, error) {
	mode := requestValue(r, "mode")
	if mode == "NR5G Only" {
		pci := stripNonDigits(requestValue(r, "pci"))
		earfcn := stripNonDigits(requestValue(r, "earfcn"))
		scs := stripNonDigits(requestValue(r, "scs"))
		band := stripNonDigits(requestValue(r, "band"))
		if scs == "" {
			scs = "30"
		}
		if pci == "" || earfcn == "" || band == "" {
			return "", fmt.Errorf("missing nr cell parameters")
		}
		return s.runPageAction(fmt.Sprintf(`AT+QNWLOCK="common/5g",%s,%s,%s,%s`, pci, earfcn, scs, band)), nil
	}
	if mode == "LTE Only" {
		earfcns := requestValues(r, "earfcn")
		pcis := requestValues(r, "pci")
		if len(earfcns) == 1 && strings.Contains(earfcns[0], ",") {
			earfcns = strings.Split(earfcns[0], ",")
		}
		if len(pcis) == 1 && strings.Contains(pcis[0], ",") {
			pcis = strings.Split(pcis[0], ",")
		}
		if len(earfcns) == 0 || len(earfcns) != len(pcis) || len(earfcns) > 10 {
			return "", fmt.Errorf("invalid lte cell parameters")
		}
		parts := []string{strconv.Itoa(len(earfcns))}
		for i := range earfcns {
			parts = append(parts, stripNonDigits(earfcns[i]), stripNonDigits(pcis[i]))
		}
		return s.runPageAction(`AT+QNWLOCK="common/4g",` + strings.Join(parts, ",")), nil
	}
	return "", fmt.Errorf("invalid scan mode")
}

func (s *simpleAdminServer) saveNetworkSettings(r *http.Request) (string, error) {
	commands := []string{}
	pdp := strings.ToUpper(strings.TrimSpace(requestValue(r, "pdpType")))
	apn := strings.TrimSpace(requestValue(r, "apn"))
	if pdp != "" || apn != "" {
		if pdp == "" {
			pdp = "IPV4V6"
		}
		if !map[string]bool{"IP": true, "IPV6": true, "IPV4V6": true}[pdp] {
			return "", fmt.Errorf("invalid pdp type")
		}
		commands = append(commands, fmt.Sprintf(`+CGDCONT=1,"%s","%s"`, pdp, sanitizeAPN(apn)))
	}
	mode := strings.TrimSpace(requestValue(r, "modePref"))
	if mode != "" {
		commands = append(commands, fmt.Sprintf(`+QNWPREFCFG="mode_pref",%s`, sanitizeModePref(mode)))
	}
	nrMode := stripNonDigits(requestValue(r, "nrDisableMode"))
	if nrMode != "" {
		commands = append(commands, fmt.Sprintf(`+QNWPREFCFG="nr5g_disable_mode",%s`, nrMode))
	}
	if len(commands) == 0 {
		return "", fmt.Errorf("no changes")
	}
	return s.runPageAction("AT" + strings.Join(commands, ";")), nil
}

func (s *simpleAdminServer) lockLTEManual(r *http.Request) (string, error) {
	cellNum := stripNonDigits(requestValue(r, "cellNum"))
	values := requestValues(r, "pairs")
	if len(values) == 1 && strings.Contains(values[0], ";") {
		values = strings.Split(values[0], ";")
	}
	pairs := []string{}
	for _, p := range values {
		fields := strings.Split(p, ",")
		if len(fields) != 2 {
			continue
		}
		earfcn := stripNonDigits(fields[0])
		pci := stripNonDigits(fields[1])
		if earfcn != "" && pci != "" {
			pairs = append(pairs, earfcn, pci)
		}
	}
	if cellNum == "" {
		cellNum = strconv.Itoa(len(pairs) / 2)
	}
	if len(pairs) == 0 {
		return "", fmt.Errorf("missing lte lock pairs")
	}
	return s.runPageAction(fmt.Sprintf(`AT+QNWLOCK="common/4g",%s,%s`, cellNum, strings.Join(pairs, ","))), nil
}

func atLines(raw string) []string {
	out := []string{}
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r", ""), "\n") {
		s := strings.TrimSpace(line)
		if s != "" && s != "OK" {
			out = append(out, s)
		}
	}
	return out
}

func isQMAPRecord(line, name string) bool {
	if !strings.HasPrefix(strings.TrimSpace(line), "+QMAP:") {
		return false
	}
	parts := csvFields(strings.TrimPrefix(line, "+QMAP:"))
	return len(parts) > 0 && strings.EqualFold(parts[0], name)
}

func csvFields(value string) []string {
	re := regexp.MustCompile(`"[^"]*"|[^,]+`)
	matches := re.FindAllString(value, -1)
	fields := make([]string, 0, len(matches))
	for _, item := range matches {
		fields = append(fields, strings.Trim(strings.TrimSpace(item), `"`))
	}
	return fields
}

func isPlainATValue(s string) bool {
	up := strings.ToUpper(strings.TrimSpace(s))
	return s != "" && up != "OK" && up != "ERROR" && !strings.HasPrefix(up, "AT+") && !strings.HasPrefix(s, "+")
}

func extractIMEI(s string) string {
	if m := regexp.MustCompile(`(\d{14,17})`).FindStringSubmatch(s); len(m) > 1 {
		return m[1]
	}
	return ""
}

func atResponseOK(text string) bool {
	up := strings.ToUpper(text)
	return strings.Contains(up, "OK") && !strings.Contains(up, "ERROR")
}

func stringValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
func isAllZeroV6(v string) bool {
	return regexp.MustCompile(`^0(\.0){15}$`).MatchString(v) || v == "0:0:0:0:0:0:0:0"
}
func safeBandList(v string) string { return regexp.MustCompile(`[^0-9:]`).ReplaceAllString(v, "") }
func cleanIP(v string) string {
	if regexp.MustCompile(`^\d{1,3}(\.\d{1,3}){3}$`).MatchString(v) {
		return v
	}
	return ""
}
func lastIPPart(v string) string {
	parts := strings.Split(v, ".")
	if len(parts) == 4 {
		return parts[3]
	}
	return ""
}
func sanitizeAPN(v string) string {
	return regexp.MustCompile(`[^A-Za-z0-9._-]`).ReplaceAllString(v, "")
}
func sanitizeModePref(v string) string {
	return regexp.MustCompile(`[^A-Za-z0-9_+:]`).ReplaceAllString(v, "")
}
func bandOrder(typ string) int {
	if typ == "PCC" {
		return 0
	}
	if typ == "SCC" {
		return 1
	}
	return 2
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func percentFromAny(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case float64:
		return int(x)
	case string:
		n, _ := strconv.Atoi(x)
		return n
	default:
		return 0
	}
}

func applyQENGDashboard(line string, data map[string]any) {
	p := csvFields(strings.TrimPrefix(line, "+QENG:"))
	if len(p) == 0 {
		return
	}
	kind := strings.ToUpper(p[0])
	if kind == "SERVINGCELL" {
		applyServingCellDashboard(p, data)
		return
	}
	switch kind {
	case "LTE":
		data["network_mode"] = "LTE"
		applyLTEDashboard(p, data)
	case "NR5G-NSA":
		data["network_mode"] = "NR5G-NSA"
		applyNRNSADashboard(p, data)
	case "NR5G-SA":
		data["network_mode"] = "NR5G-SA"
		applyNRSAStandaloneDashboard(p, data)
	}
}

func applyServingCellDashboard(p []string, data map[string]any) {
	if len(p) < 3 {
		return
	}
	rat := strings.ToUpper(p[2])
	duplex := ""
	if len(p) > 3 && p[3] != "" {
		duplex = " " + p[3]
	}
	switch rat {
	case "NR5G-SA":
		data["network_mode"] = "NR5G-SA" + duplex
		applyNRSACombinedDashboard(p, data)
	case "LTE":
		data["network_mode"] = "LTE" + duplex
		applyLTEServingCellDashboard(p, data)
	}
}

func applyLTEDashboard(p []string, data map[string]any) {
	if len(p) > 4 {
		setCIDFields(p[4], data)
	}
	if len(p) > 5 && p[5] != "" {
		data["pcc_pci"] = p[5]
	}
	if len(p) > 6 && p[6] != "" {
		appendDashboardList(data, "earfcns", p[6])
	}
	if len(p) > 10 && p[10] != "" {
		data["tac"] = hexWithDecimal(p[10])
	}
	if len(p) > 11 {
		data["rsrpLTE"] = p[11]
		data["rsrpLTEPercentage"] = calcRSRP(toInt(p[11]))
	}
	if len(p) > 12 {
		data["rsrqLTE"] = p[12]
		data["rsrqLTEPercentage"] = calcRSRQ(toInt(p[12]))
	}
	if len(p) > 13 {
		data["rssi"] = p[13]
	}
	if len(p) > 14 {
		data["sinrLTE"] = p[14]
		data["sinrLTEPercentage"] = calcSINR(toInt(p[14]))
	}
}

func applyLTEServingCellDashboard(p []string, data map[string]any) {
	if len(p) > 6 {
		setCIDFields(p[6], data)
	}
	if len(p) > 7 && p[7] != "" {
		data["pcc_pci"] = p[7]
	}
	if len(p) > 9 && p[9] != "" {
		appendDashboardList(data, "earfcns", p[9])
	}
	if len(p) > 12 && p[12] != "" {
		data["tac"] = hexWithDecimal(p[12])
	}
	if len(p) > 13 {
		data["rsrpLTE"] = p[13]
		data["rsrpLTEPercentage"] = calcRSRP(toInt(p[13]))
	}
	if len(p) > 14 {
		data["rsrqLTE"] = p[14]
		data["rsrqLTEPercentage"] = calcRSRQ(toInt(p[14]))
	}
	if len(p) > 15 {
		data["rssi"] = p[15]
	}
	if len(p) > 16 {
		data["sinrLTE"] = p[16]
		data["sinrLTEPercentage"] = calcSINR(toInt(p[16]))
	}
}

func applyNRNSADashboard(p []string, data map[string]any) {
	if len(p) > 3 && p[3] != "" {
		data["pcc_pci"] = p[3]
	}
	if len(p) > 4 {
		data["rsrpNR"] = p[4]
		data["rsrpNRPercentage"] = calcRSRP(toInt(p[4]))
	}
	if len(p) > 5 {
		data["sinrNR"] = p[5]
		data["sinrNRPercentage"] = calcSINR(toInt(p[5]))
	}
	if len(p) > 6 {
		data["rsrqNR"] = p[6]
		data["rsrqNRPercentage"] = calcRSRQ(toInt(p[6]))
	}
	if len(p) > 7 && p[7] != "" {
		appendDashboardList(data, "earfcns", p[7])
	}
}

func applyNRSAStandaloneDashboard(p []string, data map[string]any) {
	if len(p) > 3 && p[3] != "" {
		data["pcc_pci"] = p[3]
	}
	if len(p) > 4 {
		data["rsrpNR"] = p[4]
		data["rsrpNRPercentage"] = calcRSRP(toInt(p[4]))
	}
	if len(p) > 5 {
		data["sinrNR"] = p[5]
		data["sinrNRPercentage"] = calcSINR(toInt(p[5]))
	}
	if len(p) > 6 {
		data["rsrqNR"] = p[6]
		data["rsrqNRPercentage"] = calcRSRQ(toInt(p[6]))
	}
	if len(p) > 7 && p[7] != "" {
		appendDashboardList(data, "earfcns", p[7])
	}
}

func applyNRSACombinedDashboard(p []string, data map[string]any) {
	if len(p) > 6 {
		setCIDFields(p[6], data)
	}
	if len(p) > 7 && p[7] != "" {
		data["pcc_pci"] = p[7]
	}
	if len(p) > 8 && p[8] != "" {
		data["tac"] = hexWithDecimal(p[8])
	}
	if len(p) > 9 && p[9] != "" {
		appendDashboardList(data, "earfcns", p[9])
	}
	if len(p) > 12 {
		data["rsrpNR"] = p[12]
		data["rsrpNRPercentage"] = calcRSRP(toInt(p[12]))
	}
	if len(p) > 13 {
		data["rsrqNR"] = p[13]
		data["rsrqNRPercentage"] = calcRSRQ(toInt(p[13]))
	}
	if len(p) > 14 {
		data["sinrNR"] = p[14]
		data["sinrNRPercentage"] = calcSINR(toInt(p[14]))
	}
}

func setCIDFields(cid string, data map[string]any) {
	cid = strings.TrimSpace(cid)
	if cid == "" || cid == "-" {
		return
	}
	if len(cid) > 2 {
		short := cid[len(cid)-2:]
		data["eNBID"] = cid[:len(cid)-2]
		data["cellID"] = fmt.Sprintf("%s(%d), %s(%d)", short, hexToInt(short), cid, hexToInt(cid))
		return
	}
	data["cellID"] = cid
}

func hexWithDecimal(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || v == "-" {
		return "-"
	}
	return fmt.Sprintf("%s (%d)", v, hexToInt(v))
}

func hexToInt(v string) int64 {
	n, err := strconv.ParseInt(v, 16, 64)
	if err != nil {
		return 0
	}
	return n
}

func appendDashboardList(data map[string]any, key string, value string) {
	value = strings.TrimSpace(value)
	if value == "" || value == "-" {
		return
	}
	cur := stringValue(data[key])
	if cur == "" || cur == "-" {
		data[key] = value
		return
	}
	items := strings.Split(cur, " / ")
	for _, item := range items {
		if item == value {
			return
		}
	}
	data[key] = cur + " / " + value
}

func applyQCAInfoDashboard(lines []string, data map[string]any) {
	bands, bws, earfcns, scc := []string{}, []string{}, []string{}, []string{}
	for _, line := range lines {
		p := csvFields(strings.TrimPrefix(line, "+QCAINFO:"))
		if len(p) < 4 {
			continue
		}
		typ := strings.ToUpper(p[0])
		bandRaw := strings.ToUpper(p[3])
		isNR := strings.Contains(bandRaw, "NR")
		if p[1] != "" {
			earfcns = append(earfcns, p[1])
		}
		band := shortBandName(p[3])
		if band != "" {
			bands = append(bands, band)
		}
		bw := bwMHz(p[2], isNR)
		if bw != "" && bw != "-" {
			bws = append(bws, bw)
		}
		pci := qcaPCI(p, typ, isNR)
		if typ == "PCC" && pci != "" && pci != "-" {
			data["pcc_pci"] = pci
		}
		if typ == "SCC" && pci != "" && pci != "-" {
			scc = append(scc, pci)
		}
	}
	if len(bands) > 0 {
		data["bands"] = strings.Join(bands, " / ")
	}
	if len(bws) > 0 {
		data["bandwidth"] = strings.Join(bws, " / ")
	}
	if len(earfcns) > 0 {
		data["earfcns"] = strings.Join(earfcns, " / ")
	}
	if len(scc) > 0 {
		data["scc_pci"] = strings.Join(scc, " + ")
	}
}

func qcaPCI(p []string, typ string, isNR bool) string {
	if len(p) < 5 {
		return "-"
	}
	if isNR {
		if strings.EqualFold(typ, "SCC") && len(p) > 5 {
			return p[5]
		}
		return p[4]
	}
	if len(p) > 5 {
		return p[5]
	}
	return p[4]
}

func applyQRSRPDashboard(lines []string, data map[string]any) {
	type vals struct{ prx, drx, rx2, rx3 string }
	lte, nr := vals{"None", "None", "None", "None"}, vals{"None", "None", "None", "None"}
	hasLTE, hasNR := false, false
	for _, line := range lines {
		p := csvFields(strings.TrimPrefix(line, "+QRSRP:"))
		if len(p) < 4 {
			continue
		}
		v := vals{p[0], p[1], p[2], p[3]}
		if strings.Contains(line, "LTE") {
			lte = v
			hasLTE = true
		} else if strings.Contains(line, "NR5G") {
			nr = v
			hasNR = true
		}
	}
	if hasLTE && hasNR {
		data["prxqrsrp"] = lte.prx + "/" + nr.prx
		data["drxqrsrp"] = lte.drx + "/" + nr.drx
		data["rx2qrsrp"] = lte.rx2 + "/" + nr.rx2
		data["rx3qrsrp"] = lte.rx3 + "/" + nr.rx3
	} else if hasLTE {
		data["prxqrsrp"] = lte.prx
		data["drxqrsrp"] = lte.drx
		data["rx2qrsrp"] = lte.rx2
		data["rx3qrsrp"] = lte.rx3
	} else if hasNR {
		data["prxqrsrp"] = nr.prx
		data["drxqrsrp"] = nr.drx
		data["rx2qrsrp"] = nr.rx2
		data["rx3qrsrp"] = nr.rx3
	}
}

func toInt(s string) int { n, _ := strconv.Atoi(strings.TrimSpace(s)); return n }
func clamp(v int) int {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
func calcRSRP(v int) int {
	if v <= -120 {
		return 0
	}
	return clamp((v + 120) * 100 / 40)
}
func calcRSRQ(v int) int {
	if v <= -20 {
		return 0
	}
	return clamp((v + 20) * 100 / 12)
}
func calcSINR(v int) int {
	if v <= 0 {
		return 0
	}
	return clamp(v * 100 / 20)
}
func weightedSignalPercent(rsrpPct, rsrqPct, sinrPct int) int {
	return clamp((rsrpPct*50 + rsrqPct*25 + sinrPct*25 + 50) / 100)
}
func signalQualityGo(p int) string {
	if p >= 80 {
		return "优秀"
	}
	if p >= 60 {
		return "良好"
	}
	if p >= 40 {
		return "一般"
	}
	if p >= 0 {
		return "差"
	}
	return "无信号"
}
func shortBandName(s string) string {
	text := strings.ToUpper(strings.TrimSpace(s))
	if m := regexp.MustCompile(`NR5G\s+BAND\s+(\d+)`).FindStringSubmatch(text); len(m) > 1 {
		return "N" + m[1]
	}
	if m := regexp.MustCompile(`NR\s+BAND\s+(\d+)`).FindStringSubmatch(text); len(m) > 1 {
		return "N" + m[1]
	}
	if m := regexp.MustCompile(`LTE\s+BAND\s+(\d+)`).FindStringSubmatch(text); len(m) > 1 {
		return "B" + m[1]
	}
	return s
}

func bwMHz(code string, nr bool) string {
	n := toInt(code)
	if nr {
		if n >= 0 && n <= 5 {
			return fmt.Sprintf("%dMHz", (n+1)*5)
		}
		if n >= 6 && n <= 12 {
			return fmt.Sprintf("%dMHz", (n-2)*10)
		}
		return "-"
	}
	m := map[int]float64{0: 1.4, 1: 3, 2: 5, 3: 10, 4: 15, 5: 20, 6: 1.4, 15: 3, 25: 5, 50: 10, 75: 15, 100: 20}
	if v, ok := m[n]; ok {
		return strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%.1f", v), "0"), ".") + "MHz"
	}
	return "-"
}
func humanBytesGo(bytes float64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	i := 0
	for bytes >= 1024 && i < len(units)-1 {
		bytes /= 1024
		i++
	}
	if i > 0 && bytes < 10 {
		return fmt.Sprintf("%.1f %s", bytes, units[i])
	}
	return fmt.Sprintf("%.0f %s", bytes, units[i])
}
func parseUptimeParts(text string) map[string]int {
	days, hours, minutes := 0, 0, 0
	if m := regexp.MustCompile(`(\d+)\s+day`).FindStringSubmatch(text); len(m) > 1 {
		days = toInt(m[1])
	}
	if m := regexp.MustCompile(`(\d+)\s+hour`).FindStringSubmatch(text); len(m) > 1 {
		hours = toInt(m[1])
	}
	if m := regexp.MustCompile(`(\d+)\s+min`).FindStringSubmatch(text); len(m) > 1 {
		minutes = toInt(m[1])
	}
	return map[string]int{"days": days, "hours": hours, "minutes": minutes}
}

func networkName(mcc, mnc string) string {
	names := map[string]string{"46000": "中国移动", "46001": "中国联通", "46003": "中国电信", "46009": "中国联通", "46011": "中国电信", "46015": "中国广电", "46020": "中国铁通"}
	if v := names[mcc+mnc]; v != "" {
		return v
	}
	return strings.TrimSpace(mcc + " " + mnc)
}

func decodeMaybeUCS2(value string) string {
	clean := strings.ReplaceAll(strings.TrimSpace(value), " ", "")
	if clean == "" || len(clean)%4 != 0 || !regexp.MustCompile(`^[0-9A-Fa-f]+$`).MatchString(clean) {
		return value
	}
	var b strings.Builder
	for i := 0; i < len(clean); i += 4 {
		n, err := strconv.ParseInt(clean[i:i+4], 16, 32)
		if err != nil {
			return value
		}
		b.WriteRune(rune(n))
	}
	return b.String()
}
