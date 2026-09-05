package main

import (
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	atCachePendingText       = "AT command is running in background; cached data is not ready yet."
	atCacheReadMaxAge        = 3 * time.Second
	atCacheSemiStaticMaxAge  = 30 * time.Second
	atCacheConfigMaxAge      = 60 * time.Second
	atCacheStaticMaxAge      = 10 * time.Minute
	atCachePeriodicInterval  = 15 * time.Second
	atCacheFirstRefreshDelay = 500 * time.Millisecond
	atCacheBootGracePeriod   = 35 * time.Second
	atCacheMaxEntries        = 256
	atCacheBusyText          = "ERROR: AT queue is busy; retry later."
)

type atCacheEntry struct {
	command   string
	response  string
	errorText string
	updatedAt time.Time
	running   bool
	done      chan struct{}
}

type atCommandCacheManager struct {
	mu       sync.Mutex
	entries  map[string]*atCacheEntry
	queue    chan string
	started  bool
	mockMode bool
	readyAt  time.Time
}

var atCommandCache = &atCommandCacheManager{
	entries: make(map[string]*atCacheEntry),
	queue:   make(chan string, 64),
}

func (m *atCommandCacheManager) Start(mockMode bool) {
	m.mu.Lock()
	m.mockMode = mockMode
	if m.started {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.readyAt = time.Now().Add(atCacheStartupDelay(mockMode))
	readyAt := m.readyAt
	m.mu.Unlock()

	if delay := time.Until(readyAt); delay > 0 {
		log.Printf("AT 缓存启动保护: 系统刚开机，%.0f 秒后再执行读类 AT 指令", delay.Seconds())
	}

	go m.worker()
	go m.periodicRefresh()
	go m.initialRefresh()
}

func (m *atCommandCacheManager) worker() {
	for command := range m.queue {
		m.waitUntilReadyFor(command)
		m.run(command)
	}
}

func (m *atCommandCacheManager) periodicRefresh() {
	ticker := time.NewTicker(atCachePeriodicInterval)
	defer ticker.Stop()
	for range ticker.C {
		if m.startupDelayRemaining() > 0 {
			continue
		}
		for _, command := range m.cachedReadCommandsNeedingRefresh() {
			m.enqueue(command, false)
		}
	}
}

func (m *atCommandCacheManager) initialRefresh() {
	time.Sleep(atCacheFirstRefreshDelay)
	m.waitUntilReadyFor("")
	m.enqueueInitialReadCommands()
}

func (m *atCommandCacheManager) Fetch(command string, force bool, waitOverride *bool) string {
	command = sanitizeATCommand(command)
	if command == "" {
		return ""
	}

	has, response, errorText, stale, running := m.snapshot(command)
	startupDelayedRead := m.startupDelayRemaining() > 0 && !isATActionCommand(command) && !isATImmediateReadCommand(command)
	mustRun := force || isATActionCommand(command) || !has || stale
	var done <-chan struct{}
	if mustRun {
		var err error
		done, err = m.enqueue(command, force || isATActionCommand(command))
		if err != nil {
			return err.Error()
		}
		running = true
	}

	if startupDelayedRead {
		return m.responseOrPending(has, response, errorText, running)
	}

	wait := !has || force || isATActionCommand(command)
	if waitOverride != nil {
		wait = *waitOverride
	}
	if wait && done != nil {
		select {
		case <-done:
		case <-time.After(atCacheWaitTimeout(command)):
			return atCachePendingText
		}
		has, response, errorText, _, running = m.snapshot(command)
	}

	return m.responseOrPending(has, response, errorText, running)
}

func (m *atCommandCacheManager) responseOrPending(has bool, response, errorText string, running bool) string {
	if has {
		if strings.TrimSpace(response) != "" {
			return response
		}
		if strings.TrimSpace(errorText) != "" {
			return errorText
		}
		return ""
	}
	if strings.TrimSpace(errorText) != "" {
		return errorText
	}
	if running {
		return atCachePendingText
	}
	return ""
}

func (m *atCommandCacheManager) snapshot(command string) (bool, string, string, bool, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entries[command]
	if e == nil {
		return false, "", "", true, false
	}
	has := !e.updatedAt.IsZero()
	stale := !has || time.Since(e.updatedAt) >= maxAgeForATCacheCommand(command)
	return has, e.response, e.errorText, stale, e.running
}

func (m *atCommandCacheManager) enqueue(command string, force bool) (<-chan struct{}, error) {
	command = sanitizeATCommand(command)
	done := make(chan struct{})
	if command == "" {
		close(done)
		return done, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entries[command]
	if e == nil {
		if len(m.entries) >= atCacheMaxEntries {
			oldest := ""
			for key, entry := range m.entries {
				if !entry.running && (oldest == "" || entry.updatedAt.Before(m.entries[oldest].updatedAt)) {
					oldest = key
				}
			}
			if oldest == "" {
				return nil, errors.New(atCacheBusyText)
			}
			delete(m.entries, oldest)
		}
		e = &atCacheEntry{command: command}
	}
	if e.running {
		return e.done, nil
	}
	if !force && !e.updatedAt.IsZero() && time.Since(e.updatedAt) < maxAgeForATCacheCommand(command) {
		close(done)
		return done, nil
	}

	select {
	case m.queue <- command:
		e.running = true
		e.done = done
		m.entries[command] = e
	default:
		return nil, errors.New(atCacheBusyText)
	}
	return done, nil
}

func (m *atCommandCacheManager) run(command string) {
	command = sanitizeATCommand(command)
	if command == "" {
		return
	}

	mockMode := m.currentMockMode()
	response, err := executeCachedATCommand(command, mockMode)
	errorText := ""
	if err != nil {
		errorText = err.Error()
		if strings.TrimSpace(response) == "" {
			response = errorText
		}
	}

	m.mu.Lock()
	e := m.entries[command]
	if e == nil {
		e = &atCacheEntry{command: command}
		m.entries[command] = e
	}
	e.response = response
	e.errorText = errorText
	e.updatedAt = time.Now()
	e.running = false
	done := e.done
	e.done = nil
	m.mu.Unlock()

	if done != nil {
		close(done)
	}
	if err != nil {
		log.Printf("AT 后台缓存更新失败: command=%q error=%v", command, err)
	}
	if isATActionCommand(command) {
		m.invalidateReadCache()
		go func() {
			time.Sleep(2 * time.Second)
			m.enqueueCachedReadCommands()
		}()
	}
}

func (m *atCommandCacheManager) invalidateReadCache() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for command, entry := range m.entries {
		if entry.running || isATActionCommand(command) {
			continue
		}
		entry.updatedAt = time.Time{}
	}
}

func (m *atCommandCacheManager) MarkStale(command string) {
	command = sanitizeATCommand(command)
	if command == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.entries[command]
	if entry == nil || entry.running {
		return
	}
	entry.updatedAt = time.Time{}
}

func (m *atCommandCacheManager) currentMockMode() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mockMode
}

func (m *atCommandCacheManager) cachedReadCommandsNeedingRefresh() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	commands := make([]string, 0, len(m.entries))
	for command, entry := range m.entries {
		if entry.running || isATActionCommand(command) || isPeriodicRefreshSuppressedATCommand(command) {
			continue
		}
		if entry.updatedAt.IsZero() || time.Since(entry.updatedAt) >= maxAgeForATCacheCommand(command) {
			commands = append(commands, command)
		}
	}
	return commands
}

func (m *atCommandCacheManager) enqueueInitialReadCommands() {
	commands := commonATCacheCommands()
	if len(commands) == 0 {
		return
	}
	m.enqueue(commands[0], false)
}

func (m *atCommandCacheManager) enqueueCachedReadCommands() {
	for _, command := range m.cachedReadCommandsNeedingRefresh() {
		m.enqueue(command, false)
	}
}

func (m *atCommandCacheManager) waitUntilReadyFor(command string) {
	if isATActionCommand(command) || isATImmediateReadCommand(command) || m.currentMockMode() {
		return
	}
	if delay := m.startupDelayRemaining(); delay > 0 {
		time.Sleep(delay)
	}
}

func (m *atCommandCacheManager) startupDelayRemaining() time.Duration {
	m.mu.Lock()
	readyAt := m.readyAt
	m.mu.Unlock()
	if readyAt.IsZero() {
		return 0
	}
	if delay := time.Until(readyAt); delay > 0 {
		return delay
	}
	return 0
}

func atCacheStartupDelay(mockMode bool) time.Duration {
	if mockMode {
		return 0
	}
	uptime, ok := systemUptimeDuration()
	if !ok || uptime >= atCacheBootGracePeriod {
		return 0
	}
	return atCacheBootGracePeriod - uptime
}

func systemUptimeDuration() (time.Duration, bool) {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, false
	}
	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || seconds < 0 {
		return 0, false
	}
	return time.Duration(seconds * float64(time.Second)), true
}

func executeCachedATCommand(command string, mockMode bool) (string, error) {
	if mockMode {
		return mockATResponse(command), nil
	}
	return runATCommandUntilDone(command, 200, atCommandTimeoutMS(command))
}

func (s *simpleAdminServer) handleGetATCache(w http.ResponseWriter, r *http.Request) {
	command := sanitizeATCommand(requestValue(r, "atcmd"))
	if command == "" {
		writeText(w, http.StatusOK, "")
		return
	}
	atCommandCache.Start(s.cfg.mockMode)
	force := boolQuery(r, "force", false)
	var waitOverride *bool
	if len(requestValues(r, "wait")) > 0 {
		wait := boolQuery(r, "wait", false)
		waitOverride = &wait
	}
	writeText(w, http.StatusOK, atCommandCache.Fetch(command, force, waitOverride))
}

func requestValue(r *http.Request, name string) string {
	values := requestValues(r, name)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func requestValues(r *http.Request, name string) []string {
	if err := r.ParseForm(); err == nil {
		if values, ok := r.Form[name]; ok {
			return values
		}
	}
	return r.URL.Query()[name]
}

func boolQuery(r *http.Request, name string, fallback bool) bool {
	values := requestValues(r, name)
	if len(values) == 0 {
		return fallback
	}
	value := strings.TrimSpace(strings.ToLower(values[0]))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err == nil {
		return parsed
	}
	return value == "1" || value == "yes" || value == "on"
}

func atCacheWaitTimeout(command string) time.Duration {
	timeout := time.Duration(atCommandTimeoutMS(command)+2000) * time.Millisecond
	if timeout < 2*time.Second {
		return 2 * time.Second
	}
	return timeout
}

func maxAgeForATCacheCommand(command string) time.Duration {
	command = sanitizeATCommand(command)
	upper := strings.ToUpper(command)
	switch {
	case isATActionCommand(command):
		return 0
	case isStandaloneModelCommand(command) || isDeviceStaticInfoCommand(command):
		return atCacheStaticMaxAge
	case isSettingsStatusConfigCommand(command) || isStandaloneLANIPCommand(command) || isBandPreferenceQueryCommand(command):
		return atCacheConfigMaxAge
	case isNetworkPreferenceStatusCommand(command):
		return atCacheSemiStaticMaxAge
	case isSMSListCommand(command):
		return 5 * time.Second
	case strings.Contains(upper, "+QSIMSTAT") || strings.Contains(upper, "+CPIN") || strings.Contains(upper, `+QMAP="WWAN"`) || strings.Contains(upper, "+QCAINFO") || strings.Contains(upper, "+QENG") || strings.Contains(upper, "+QRSRP") || strings.Contains(upper, "+CSQ") || strings.Contains(upper, "+QTEMP"):
		return atCacheReadMaxAge
	case strings.Contains(upper, "+CIMI") || strings.Contains(upper, "+ICCID") || strings.Contains(upper, "+CNUM") || strings.Contains(upper, "+CGMI") || strings.Contains(upper, "+CGSN") || strings.Contains(upper, "+QGMR"):
		return atCacheStaticMaxAge
	default:
		return atCacheReadMaxAge
	}
}

func isStandaloneModelCommand(command string) bool {
	return strings.EqualFold(strings.TrimSpace(sanitizeATCommand(command)), "AT+CGMM")
}

func isStandaloneLANIPCommand(command string) bool {
	return strings.EqualFold(strings.TrimSpace(sanitizeATCommand(command)), `AT+QMAP="LANIP"`)
}

func isDeviceStaticInfoCommand(command string) bool {
	return strings.EqualFold(strings.TrimSpace(sanitizeATCommand(command)), `AT+CGMI;+CGSN;+QGMR;+CIMI;+ICCID;+CNUM`)
}

func isBandPreferenceQueryCommand(command string) bool {
	upper := strings.ToUpper(sanitizeATCommand(command))
	return strings.Contains(upper, `+QNWPREFCFG="LTE_BAND"`) &&
		strings.Contains(upper, `+QNWPREFCFG= "NSA_NR5G_BAND"`) &&
		strings.Contains(upper, `+QNWPREFCFG= "NR5G_BAND"`)
}

func isNetworkPreferenceStatusCommand(command string) bool {
	upper := strings.ToUpper(sanitizeATCommand(command))
	return strings.Contains(upper, `+QNWPREFCFG="MODE_PREF"`) &&
		strings.Contains(upper, `+QNWPREFCFG="NR5G_DISABLE_MODE"`) &&
		strings.Contains(upper, "+CGDCONT?") &&
		strings.Contains(upper, "+CGCONTRDP=1") &&
		strings.Contains(upper, `+QNWLOCK="COMMON/4G"`) &&
		strings.Contains(upper, `+QNWLOCK="COMMON/5G"`)
}

func isSettingsStatusConfigCommand(command string) bool {
	upper := strings.ToUpper(sanitizeATCommand(command))
	return strings.Contains(upper, `+QMAP="MPDN_RULE"`) &&
		strings.Contains(upper, `+QMAP="DHCPV6DNS"`) &&
		strings.Contains(upper, `+QCFG="USBNET"`) &&
		strings.Contains(upper, `+QMAP="DMZ"`) &&
		strings.Contains(upper, `+QMAP="DHCPV4DNS"`)
}

func isSMSListCommand(command string) bool {
	upper := strings.ToUpper(sanitizeATCommand(command))
	return strings.Contains(upper, "+CMGL=4") || strings.Contains(upper, `+CMGL="ALL"`)
}

func isPeriodicRefreshSuppressedATCommand(command string) bool {
	return isStandaloneModelCommand(command) || isSMSListCommand(command)
}

func isATImmediateReadCommand(command string) bool {
	return isStandaloneModelCommand(command)
}

func isATActionCommand(command string) bool {
	upper := strings.ToUpper(command)
	if upper == "AT&F" || strings.Contains(upper, ";AT&F") {
		return true
	}
	patterns := []string{
		"+CFUN=",
		"+EGMR=",
		"+CMGD",
		"+CMGS",
		"+QSCAN=",
		"+CGDCONT=",
		"+QMAPWAC=",
		`+QCFG="USBNET",`,
		`+QMAP="MPDN_RULE",0`,
		`+QMAP="DHCPV6DNS",`,
		`+QMAP="DHCPV4DNS",`,
		`+QMAP="DMZ",`,
		`+QMAP="LANIP",`,
		`+QNWPREFCFG="LTE_BAND",`,
		`+QNWPREFCFG="NSA_NR5G_BAND",`,
		`+QNWPREFCFG="NR5G_BAND",`,
		`+QNWPREFCFG="MODE_PREF",`,
		`+QNWPREFCFG="NR5G_DISABLE_MODE",`,
		`+QNWLOCK="COMMON/4G",`,
		`+QNWLOCK="COMMON/5G",`,
	}
	for _, pattern := range patterns {
		if strings.Contains(upper, pattern) {
			return true
		}
	}
	return false
}

func commonATCacheCommands() []string {
	commands := make([]string, 0, 10)
	for _, key := range []string{
		atKeyDashboard,
		atKeyDeviceInfo,
		atKeyNetworkBands,
		atKeyNetworkSettings,
		atKeySettingsStatus,
	} {
		commands = append(commands, pageATCommands(key)...)
	}
	return commands
}

func smsListATCommand() string {
	return `AT+CSMS=1;+CSDH=0;+CNMI=2,1,0,0,0;+CMGF=0;+CPMS="ME","ME","ME";+CMGL=4`
}
