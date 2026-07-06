package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestATCacheRequestReadsPOSTForm(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/get_atcache?atcmd=AT%2BOLD", strings.NewReader("atcmd=AT%2BCGMI&force=1&wait=0"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	if got := requestValue(req, "atcmd"); got != "AT+CGMI" {
		t.Fatalf("requestValue(atcmd) = %q, want AT+CGMI", got)
	}
	if !boolQuery(req, "force", false) {
		t.Fatalf("boolQuery(force) = false, want true")
	}
	if boolQuery(req, "wait", true) {
		t.Fatalf("boolQuery(wait) = true, want false")
	}
}

func TestATCommandTimeoutUsesLongWaitForDisableIPPassthrough(t *testing.T) {
	if got := atCommandTimeoutMS(`AT+QMAP="MPDN_RULE",0`); got != 10000 {
		t.Fatalf("atCommandTimeoutMS(disable passthrough) = %d, want 10000", got)
	}
	if got := atCacheWaitTimeout(`AT+QMAP="MPDN_RULE",0`); got != 12*time.Second {
		t.Fatalf("atCacheWaitTimeout(disable passthrough) = %s, want 12s", got)
	}
}

func TestPageATCommandsIsolateLANIPOnlyInFixedCacheCommands(t *testing.T) {
	deviceInfo := pageATCommands(atKeyDeviceInfo)
	wantDeviceInfo := []string{
		`AT+CGMI;+CGSN;+QGMR;+CIMI;+ICCID;+CNUM`,
		`AT+QSIMSTAT?;+CPIN?;+QMAP="WWAN"`,
		`AT+QMAP="LANIP"`,
	}
	if !reflect.DeepEqual(deviceInfo, wantDeviceInfo) {
		t.Fatalf("device info commands = %#v, want %#v", deviceInfo, wantDeviceInfo)
	}

	settingsStatus := pageATCommands(atKeySettingsStatus)
	wantSettingsStatus := []string{
		`AT+QMAP="MPDN_RULE";+QMAP="DHCPV6DNS";+QCFG="usbnet";+QMAP="DMZ";+QMAP="DHCPV4DNS"`,
		`AT+CGSN`,
		`AT+QMAP="LANIP"`,
	}
	if !reflect.DeepEqual(settingsStatus, wantSettingsStatus) {
		t.Fatalf("settings status commands = %#v, want %#v", settingsStatus, wantSettingsStatus)
	}
}

func TestContainsAnyTokenRequiresATFinalLine(t *testing.T) {
	if containsAnyToken("+QSPN: \"LOOKUP\"\r\n", "OK", "ERROR") {
		t.Fatalf("containsAnyToken matched OK inside a payload line")
	}
	if !containsAnyToken("ATI\r\nQuectel\r\n\r\nOK\r\n", "OK", "ERROR") {
		t.Fatalf("containsAnyToken did not match final OK line")
	}
	if !containsAnyToken("+CME ERROR: 10\r\n", "OK", "ERROR") {
		t.Fatalf("containsAnyToken did not match CME ERROR line")
	}
	if !containsAnyToken("+CMS ERROR: 500\r\n", "OK", "ERROR") {
		t.Fatalf("containsAnyToken did not match CMS ERROR line")
	}
}

func TestCommonATCacheCommandsDoNotCombineLANIPRead(t *testing.T) {
	foundStandaloneLANIP := 0
	for _, command := range commonATCacheCommands() {
		if command == `AT+QMAP="LANIP"` {
			foundStandaloneLANIP++
			continue
		}
		if strings.Contains(command, `+QMAP="LANIP"`) {
			t.Fatalf("common AT cache command combines LANIP read with other commands: %q", command)
		}
	}
	if foundStandaloneLANIP != 2 {
		t.Fatalf("standalone LANIP cache commands = %d, want 2", foundStandaloneLANIP)
	}
}

func TestSettingsRebootNoticeResponse(t *testing.T) {
	got := settingsRebootNoticeResponse("AT+QMAPWAC=1\r\nOK\r\n", time.Second)
	if got["ok"] != true || got["reboot"] != true || got["rebooting"] != true {
		t.Fatalf("reboot flags = %#v, want ok/reboot/rebooting true", got)
	}
	if got["rebootAfterSeconds"] != 1 {
		t.Fatalf("rebootAfterSeconds = %#v, want 1", got["rebootAfterSeconds"])
	}
	if got["rebootCountdownSeconds"] != 40 {
		t.Fatalf("rebootCountdownSeconds = %#v, want 40", got["rebootCountdownSeconds"])
	}
	if strings.TrimSpace(fmt.Sprint(got["message"])) == "" {
		t.Fatalf("message is empty: %#v", got)
	}
}

func TestATCacheFetchDuringStartupDelayReturnsPendingWithoutWaiting(t *testing.T) {
	mgr := &atCommandCacheManager{
		entries: make(map[string]*atCacheEntry),
		queue:   make(chan string, 1),
		readyAt: time.Now().Add(time.Minute),
	}

	start := time.Now()
	got := mgr.Fetch("AT+CGMI", false, nil)
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("Fetch waited during startup delay: %s", elapsed)
	}
	if got != atCachePendingText {
		t.Fatalf("Fetch() = %q, want pending text", got)
	}
	select {
	case cmd := <-mgr.queue:
		if cmd != "AT+CGMI" {
			t.Fatalf("queued command = %q, want AT+CGMI", cmd)
		}
	default:
		t.Fatalf("expected command to be queued for delayed background refresh")
	}
}

func TestLogoutPageClearsSessionAndRedirectsToLogin(t *testing.T) {
	dir := t.TempDir()
	authFile := filepath.Join(dir, "simpleadmin.auth")
	staticDir := filepath.Join(dir, "www")
	if err := os.MkdirAll(staticDir, 0755); err != nil {
		t.Fatalf("make static dir: %v", err)
	}
	if err := os.WriteFile(authFile, []byte("admin:admin\n"), 0600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	srv := &simpleAdminServer{cfg: serverConfig{authFile: authFile, staticDir: staticDir}}
	token, err := srv.createSession(time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/logout.html", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()

	srv.routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303, body = %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("WWW-Authenticate"); got != "" {
		t.Fatalf("WWW-Authenticate = %q, want empty for page login", got)
	}
	if got := rr.Header().Get("Location"); got != "/login.html" {
		t.Fatalf("Location = %q, want /login.html", got)
	}
	if srv.validateSession(token) {
		t.Fatalf("logout did not remove session token")
	}
}

func TestModuleServiceUsesHTTPOnly(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "development", "simpleadmin", "systemd", "simpleadmin-httpd.service"))
	if err != nil {
		t.Fatalf("read service file: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "-http :80") || !strings.Contains(content, "-no-tls") || !strings.Contains(content, "-static /usrdata/simpleadmin/www") {
		t.Fatalf("module service should use HTTP-only mode with supported flags: %s", content)
	}
	if strings.Contains(content, "--https") || strings.Contains(content, "--ca-cert") || strings.Contains(content, "--ca-key") || strings.Contains(content, "--data-dir") || strings.Contains(content, "--www") {
		t.Fatalf("module service should not enable HTTPS or CA parameters: %s", content)
	}
}

func TestCACertAndHelpNoLongerBypassLoginPageAuth(t *testing.T) {
	dir := t.TempDir()
	authFile := filepath.Join(dir, "simpleadmin.auth")
	staticDir := filepath.Join(dir, "www")
	if err := os.MkdirAll(staticDir, 0755); err != nil {
		t.Fatalf("make static dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staticDir, "index.html"), []byte("ok"), 0644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	if err := os.WriteFile(authFile, []byte("admin:admin\n"), 0600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}
	srv := &simpleAdminServer{cfg: serverConfig{authFile: authFile, staticDir: staticDir}}
	for _, path := range []string{"/zbims-ca.crt", "/zbims-ca.cer", "/cert.html"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		srv.routes().ServeHTTP(rr, req)
		if rr.Code != http.StatusSeeOther {
			t.Fatalf("%s status = %d, want 303", path, rr.Code)
		}
		if got := rr.Header().Get("Location"); !strings.HasPrefix(got, "/login.html") {
			t.Fatalf("%s Location = %q, want /login.html", path, got)
		}
		if got := rr.Header().Get("WWW-Authenticate"); got != "" {
			t.Fatalf("%s WWW-Authenticate = %q, want empty", path, got)
		}
	}
}

func TestLoginPageAuthDoesNotUseBrowserBasicPrompt(t *testing.T) {
	dir := t.TempDir()
	authFile := filepath.Join(dir, "simpleadmin.auth")
	staticDir := filepath.Join(dir, "www")
	if err := os.MkdirAll(staticDir, 0755); err != nil {
		t.Fatalf("make static dir: %v", err)
	}
	if err := os.WriteFile(authFile, []byte("admin:admin\n"), 0600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	srv := &simpleAdminServer{cfg: serverConfig{authFile: authFile, staticDir: staticDir}}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("log", "out")
	rr := httptest.NewRecorder()

	srv.routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rr.Code)
	}
	if got := rr.Header().Get("Location"); got != "/login.html" {
		t.Fatalf("Location = %q, want /login.html", got)
	}
	if got := rr.Header().Get("WWW-Authenticate"); got != "" {
		t.Fatalf("WWW-Authenticate = %q, want empty", got)
	}
}

func TestLoginAPIRejectsWrongCredentialsWithoutBasicPrompt(t *testing.T) {
	dir := t.TempDir()
	authFile := filepath.Join(dir, "simpleadmin.auth")
	staticDir := filepath.Join(dir, "www")
	if err := os.MkdirAll(staticDir, 0755); err != nil {
		t.Fatalf("make static dir: %v", err)
	}
	if err := os.WriteFile(authFile, []byte("admin:admin\n"), 0600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	srv := &simpleAdminServer{cfg: serverConfig{authFile: authFile, staticDir: staticDir}}
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader("username=admin&password=wrong"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	srv.routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
	if got := rr.Header().Get("WWW-Authenticate"); got != "" {
		t.Fatalf("WWW-Authenticate = %q, want empty", got)
	}
}

func TestLoginAPISetsSessionCookieAndAllowsProtectedPage(t *testing.T) {
	dir := t.TempDir()
	authFile := filepath.Join(dir, "simpleadmin.auth")
	staticDir := filepath.Join(dir, "www")
	if err := os.MkdirAll(staticDir, 0755); err != nil {
		t.Fatalf("make static dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staticDir, "index.html"), []byte("ok"), 0644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	if err := os.WriteFile(authFile, []byte("admin:admin\n"), 0600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	srv := &simpleAdminServer{cfg: serverConfig{authFile: authFile, staticDir: staticDir}}
	loginReq := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader("username=admin&password=admin"))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRR := httptest.NewRecorder()
	srv.routes().ServeHTTP(loginRR, loginReq)
	if loginRR.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", loginRR.Code, loginRR.Body.String())
	}
	cookies := loginRR.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, cookie := range cookies {
		if cookie.Name == sessionCookieName {
			sessionCookie = cookie
			break
		}
	}
	if sessionCookie == nil || sessionCookie.Value == "" || !sessionCookie.HttpOnly {
		t.Fatalf("invalid session cookie: %#v", cookies)
	}

	pageReq := httptest.NewRequest(http.MethodGet, "/", nil)
	pageReq.AddCookie(sessionCookie)
	pageRR := httptest.NewRecorder()
	srv.routes().ServeHTTP(pageRR, pageReq)
	if pageRR.Code != http.StatusOK || strings.TrimSpace(pageRR.Body.String()) != "ok" {
		t.Fatalf("protected page status/body = %d/%q", pageRR.Code, pageRR.Body.String())
	}
}

func TestSetPasswordHandlerUpdatesAuthFile(t *testing.T) {
	dir := t.TempDir()
	authFile := filepath.Join(dir, "simpleadmin.auth")
	if err := os.WriteFile(authFile, []byte("admin:admin\n"), 0600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	srv := &simpleAdminServer{cfg: serverConfig{authFile: authFile}}
	req := httptest.NewRequest(http.MethodPost, "/api/set_password", strings.NewReader("current_password=admin&new_password=secret123&confirm_password=secret123"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	srv.handleSetPassword(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	auth, err := loadAuthConfig(authFile)
	if err != nil {
		t.Fatalf("load auth: %v", err)
	}
	if auth.Username != "admin" || auth.Password != "secret123" {
		t.Fatalf("auth = %#v, want admin/secret123", auth)
	}
}

func TestSetPasswordHandlerRejectsWrongCurrentPassword(t *testing.T) {
	dir := t.TempDir()
	authFile := filepath.Join(dir, "simpleadmin.auth")
	if err := os.WriteFile(authFile, []byte("admin:admin\n"), 0600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	srv := &simpleAdminServer{cfg: serverConfig{authFile: authFile}}
	req := httptest.NewRequest(http.MethodPost, "/api/set_password", strings.NewReader("current_password=wrong&new_password=secret123&confirm_password=secret123"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	srv.handleSetPassword(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d, body = %s", rr.Code, http.StatusForbidden, rr.Body.String())
	}

	auth, err := loadAuthConfig(authFile)
	if err != nil {
		t.Fatalf("load auth: %v", err)
	}
	if auth.Password != "admin" {
		t.Fatalf("password changed unexpectedly: %#v", auth)
	}
}

func TestHTMLDoesNotReferenceLegacyCGIBin(t *testing.T) {
	staticDir := filepath.Clean(filepath.Join("..", "..", "..", "..", "development", "simpleadmin", "www"))
	entries, err := os.ReadDir(staticDir)
	if err != nil {
		t.Fatalf("read static dir: %v", err)
	}

	re := regexp.MustCompile(`/cgi-bin/[A-Za-z0-9_\-]+`)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".html" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(staticDir, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		if match := re.FindString(string(data)); match != "" {
			t.Fatalf("%s still references legacy cgi-bin path: %s", entry.Name(), match)
		}
	}
}

func TestAllHTMLAPIPathsHaveNativeHandlers(t *testing.T) {
	staticDir := filepath.Clean(filepath.Join("..", "..", "..", "..", "development", "simpleadmin", "www"))
	entries, err := os.ReadDir(staticDir)
	if err != nil {
		t.Fatalf("read static dir: %v", err)
	}

	re := regexp.MustCompile(`/api/[A-Za-z0-9_\-]+`)
	referenced := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".html" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(staticDir, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		for _, path := range re.FindAllString(string(data), -1) {
			referenced[path] = true
		}
	}

	handlers := (&simpleAdminServer{}).nativeAPIHandlers()
	for path := range referenced {
		if path == "/api/login" || path == "/api/logout" {
			continue
		}
		if handlers[path] == nil {
			t.Fatalf("missing native API handler for %s", path)
		}
	}
}

func TestExternalTtydRuntimeArtifactsAreNotPackaged(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", "..", "..", "development", "simpleadmin"))
	for _, rel := range []string{
		filepath.Join("console", "ttyd.armhf"),
		filepath.Join("console", "ttyd.bash"),
		filepath.Join("systemd", "ttyd.service"),
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
			t.Fatalf("external ttyd runtime artifact is still packaged: %s", rel)
		}
	}
}

func TestConsoleWebSocketOriginAllowed(t *testing.T) {
	cases := []struct {
		name   string
		target string
		origin string
		want   bool
	}{
		{name: "https same host", target: "https://router.local/api/console/ws", origin: "https://router.local", want: true},
		{name: "https same host default port", target: "https://router.local:443/api/console/ws", origin: "https://router.local", want: true},
		{name: "http same host", target: "http://192.168.225.1/api/console/ws", origin: "http://192.168.225.1", want: true},
		{name: "different host", target: "https://router.local/api/console/ws", origin: "https://evil.example", want: false},
		{name: "different scheme", target: "https://router.local/api/console/ws", origin: "http://router.local", want: false},
		{name: "missing origin", target: "https://router.local/api/console/ws", origin: "", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.target, nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if got := consoleWebSocketOriginAllowed(req); got != tc.want {
				t.Fatalf("consoleWebSocketOriginAllowed() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNativeConsoleRequiresFixedCredentials(t *testing.T) {
	if nativeConsoleUsername != "root" {
		t.Fatalf("nativeConsoleUsername = %q, want root", nativeConsoleUsername)
	}
	if nativeConsolePassword != "admin321" {
		t.Fatalf("nativeConsolePassword = %q, want admin321", nativeConsolePassword)
	}
}

func TestNativeConsoleLineInputEchoAndHiddenPassword(t *testing.T) {
	var builder strings.Builder
	var echo strings.Builder
	done, err := appendNativeConsoleLineInput(&builder, []byte("roox\x7ft\r"), false, func(payload []byte) error {
		echo.Write(payload)
		return nil
	})
	if err != nil || !done {
		t.Fatalf("append username done=%v err=%v", done, err)
	}
	if got := builder.String(); got != "root" {
		t.Fatalf("username builder = %q, want root", got)
	}
	if got := echo.String(); !strings.Contains(got, "\b \b") || strings.Contains(got, "admin321") {
		t.Fatalf("username echo unexpected: %q", got)
	}

	builder.Reset()
	echo.Reset()
	done, err = appendNativeConsoleLineInput(&builder, []byte("admin321\r"), true, func(payload []byte) error {
		echo.Write(payload)
		return nil
	})
	if err != nil || !done {
		t.Fatalf("append password done=%v err=%v", done, err)
	}
	if got := builder.String(); got != "admin321" {
		t.Fatalf("password builder = %q, want admin321", got)
	}
	if got := echo.String(); got != "\r\n" {
		t.Fatalf("hidden password echo = %q, want newline only", got)
	}
}

func TestNativeConsoleBannerDoesNotMentionWebPasswordHelper(t *testing.T) {
	data, err := os.ReadFile("native_console.go")
	if err != nil {
		t.Fatalf("read native_console.go: %v", err)
	}
	content := string(data)
	if strings.Contains(content, "Web password: run simplepasswd") {
		t.Fatalf("native console still prints Web password helper line")
	}
}

func TestWebsocketAcceptKey(t *testing.T) {
	got := websocketAcceptKey("dGhlIHNhbXBsZSBub25jZQ==")
	want := "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
	if got != want {
		t.Fatalf("websocket accept key = %q, want %q", got, want)
	}
}

func TestParseSettingsStatusMPDNRuleDisabledLowercase(t *testing.T) {
	raw := `AT+QMAP="MPDN_RULE";+QMAP="DHCPV6DNS";+QCFG="usbnet";+QMAP="DMZ";+QMAP="LANIP";+QMAP="DHCPV4DNS"

+QMAP: "MPDN_rule",0,0,0,0,0
+QMAP: "MPDN_rule",1,0,0,0,0
+QMAP: "MPDN_rule",2,0,0,0,0
+QMAP: "MPDN_rule",3,0,0,0,0

+QMAP: "DHCPV6DNS","disable"

+QCFG: "usbnet",1

+QMAP: "DMZ",0,4
+QMAP: "DMZ",0,6

+QMAP: "LANIP",192.168.225.20,192.168.225.170,192.168.225.1

+QMAP: "DHCPV4DNS","enable"

OK`
	got := parseSettingsStatusAT(raw)
	if got["ipPassStatus"] != false {
		t.Fatalf("ipPassStatus = %v, want false", got["ipPassStatus"])
	}
	if got["DNSV4ProxyStatus"] != true {
		t.Fatalf("DNSV4ProxyStatus = %v, want true", got["DNSV4ProxyStatus"])
	}
	if got["DNSV6ProxyStatus"] != false {
		t.Fatalf("DNSV6ProxyStatus = %v, want false", got["DNSV6ProxyStatus"])
	}
	if got["currentUsbNetMode"] != "ECM" {
		t.Fatalf("currentUsbNetMode = %v, want ECM", got["currentUsbNetMode"])
	}
	if got["lanGwIp"] != "192.168.225.1" || got["lanIpStart"] != "20" || got["lanIpEnd"] != "170" {
		t.Fatalf("LAN parse = start %v end %v gw %v", got["lanIpStart"], got["lanIpEnd"], got["lanGwIp"])
	}
}

func TestParseSettingsStatusMPDNRuleEnabled(t *testing.T) {
	raw := `+QMAP: "MPDN_rule",0,1,0,1,1,"FF:FF:FF:FF:FF:FF"
+QMAP: "MPDN_rule",1,0,0,0,0
OK`
	got := parseSettingsStatusAT(raw)
	if got["ipPassStatus"] != true {
		t.Fatalf("ipPassStatus = %v, want true", got["ipPassStatus"])
	}
}

func TestSettingsStatusCommandAndParserReadsIMEI(t *testing.T) {
	command := pageATCommand(atKeySettingsStatus)
	if !strings.Contains(command, "+CGSN") {
		t.Fatalf("settings status command = %q, want +CGSN", command)
	}
	raw := "AT+QMAP=\"DHCPV4DNS\";+CGSN\r\n+QMAP: \"DHCPV4DNS\",\"enable\"\r\n867123456789012\r\n\r\nOK\r\n"
	got := parseSettingsStatusAT(raw)
	if got["imei"] != "867123456789012" {
		t.Fatalf("imei = %v, want 867123456789012", got["imei"])
	}
}

func TestManagedTTLDeleteArgs(t *testing.T) {
	args, ok := managedTTLDeleteArgs("-A POSTROUTING -o rmnet+ -j TTL --ttl-set 65", "TTL", "--ttl-set")
	if !ok {
		t.Fatalf("expected managed IPv4 TTL rule")
	}
	want := []string{"-D", "POSTROUTING", "-o", "rmnet+", "-j", "TTL", "--ttl-set", "65"}
	if len(args) != len(want) {
		t.Fatalf("args length = %d, want %d: %#v", len(args), len(want), args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q: %#v", i, args[i], want[i], args)
		}
	}
}

func TestManagedTTLDeleteArgsIgnoresUnrelatedRules(t *testing.T) {
	cases := []string{
		"-A POSTROUTING -o wlan0 -j TTL --ttl-set 65",
		"-A POSTROUTING -o rmnet+ -j MASQUERADE",
		"-A PREROUTING -o rmnet+ -j TTL --ttl-set 65",
	}
	for _, tc := range cases {
		if _, ok := managedTTLDeleteArgs(tc, "TTL", "--ttl-set"); ok {
			t.Fatalf("unexpected managed rule match: %s", tc)
		}
	}
}

func TestVuePagesHaveValidMountScripts(t *testing.T) {
	staticDir := filepath.Clean(filepath.Join("..", "..", "..", "..", "development", "simpleadmin", "www"))
	entries, err := os.ReadDir(staticDir)
	if err != nil {
		t.Fatalf("read static dir: %v", err)
	}

	legacyEvent := regexp.MustCompile(`x-on:`)
	scriptWithSrcAndBody := regexp.MustCompile(`(?is)<script\b[^>]*\bsrc=[^>]*>\s*[^\s<][\s\S]*?</script>`)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".html" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(staticDir, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		if match := legacyEvent.Find(data); match != nil {
			t.Fatalf("%s still contains legacy Alpine event directive: %s", entry.Name(), string(match))
		}
		if match := scriptWithSrcAndBody.Find(data); match != nil {
			t.Fatalf("%s contains inline code inside a script tag with src; browsers ignore that code: %s", entry.Name(), string(match))
		}
	}
}

func TestWebFrontendUsesVue3AndNoAlpine(t *testing.T) {
	staticDir := filepath.Clean(filepath.Join("..", "..", "..", "..", "development", "simpleadmin", "www"))
	if _, err := os.Stat(filepath.Join(staticDir, "js", "vue.global.prod.js")); err != nil {
		t.Fatalf("Vue 3 runtime is not packaged: %v", err)
	}
	if _, err := os.Stat(filepath.Join(staticDir, "js", "vue-app.js")); err != nil {
		t.Fatalf("Vue mount helper is not packaged: %v", err)
	}
	if _, err := os.Stat(filepath.Join(staticDir, "js", "alpinejs.min.js")); err == nil {
		t.Fatalf("Alpine runtime should not be packaged after Vue 3 migration")
	}

	entries, err := os.ReadDir(staticDir)
	if err != nil {
		t.Fatalf("read static dir: %v", err)
	}
	legacy := regexp.MustCompile(`(?i)alpine|x-data|x-init|x-text|x-html|x-show|x-if|x-for|x-model|x-bind`)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".html" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(staticDir, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		if match := legacy.FindString(string(data)); match != "" {
			t.Fatalf("%s still contains legacy Alpine markup: %s", entry.Name(), match)
		}
		// 前端已重构为 index.html 单页应用；www 根目录只保留实际
		// 使用的 index.html 和 login.html，不再保留兼容跳转 HTML。
		if entry.Name() == "index.html" && !regexp.MustCompile(`js/vue\.global\.prod\.js`).Match(data) {
			t.Fatalf("%s does not load local Vue 3 runtime", entry.Name())
		}
		if entry.Name() != "index.html" && entry.Name() != "login.html" {
			t.Fatalf("unexpected extra html file in www root: %s", entry.Name())
		}
	}
}

func TestCollectATDeviceCandidatesSupportsConfigAndDefaults(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "at_devices.conf")
	if err := os.WriteFile(cfg, []byte("# old config should be filtered\n/dev/not_used0\n/dev/smd7 # USB bridge, must be filtered\n/dev/smd11 # fixed device\n/dev/customAT0\n"), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	devices := collectATDeviceCandidates("/dev/flagAT0,/dev/customAT1", cfg)
	wantPrefix := []string{"/dev/smd11"}
	for i, want := range wantPrefix {
		if i >= len(devices) || devices[i] != want {
			t.Fatalf("devices prefix = %#v, want %s at index %d", devices, want, i)
		}
	}
	seen := map[string]bool{}
	for _, device := range devices {
		if seen[device] {
			t.Fatalf("duplicate AT device candidate: %s in %#v", device, devices)
		}
		seen[device] = true
	}
	for _, disallowed := range []string{"/dev/not_used0", "/dev/not_used1", "/dev/customAT0", "/dev/customAT1", "/dev/flagAT0", "/dev/smd7"} {
		if seen[disallowed] {
			t.Fatalf("disallowed AT candidate %s appeared in %#v", disallowed, devices)
		}
	}
}

func TestDefaultATDeviceCandidateOrderMatchesDirectSMDDesign(t *testing.T) {
	got := defaultATDeviceCandidates()
	wantPrefix := []string{"/dev/smd11"}
	for i, want := range wantPrefix {
		if i >= len(got) || got[i] != want {
			t.Fatalf("default AT candidate order = %#v, want %s at index %d", got, want, i)
		}
	}
}

func TestParseSMSListMergesSameSenderSameDateFragments(t *testing.T) {
	parts := []string{
		"欢迎使用湖北电信短信营业厅\r\n\r\n请回复序号：\r\n1  话费积分\r\n2  优惠促销\r\n3  业务办理\r\n5  客户信息管理\r\n6  常",
		"用信息查询\r\n7  充值交费\r\n8  流量包订购\r\n---------\r\n105  积分查询\r\n82  1/3/5/7天流量包\r\n--",
		"-------\r\n抽奖立得20元权益金！ 立即参与 https://waphb.189.cn/game/hd/index.html?a",
		"=dfgewtrs\r\n【中国电信】",
	}
	raw := `+CSCA: "+8613334262200",145
+CMGL: 0,"REC READ","10001",,"26/05/20,20:17:07+32"
` + testUCS2Hex(parts[0]) + `
+CMGL: 1,"REC READ","10001",,"26/05/20,20:17:07+32"
` + testUCS2Hex(parts[1]) + `
+CMGL: 2,"REC READ","10001",,"26/05/20,20:17:07+32"
` + testUCS2Hex(parts[2]) + `
+CMGL: 3,"REC READ","10001",,"26/05/20,20:17:07+32"
` + testUCS2Hex(parts[3]) + `
OK`

	data := parseSMSListAT(raw)
	messages := data["messages"].([]map[string]any)
	if len(messages) != 1 {
		t.Fatalf("len(messages) = %d, want 1: %#v", len(messages), messages)
	}
	if got, want := messages[0]["text"], normalizeSMSLineBreaks(strings.Join(parts, "")); got != want {
		t.Fatalf("text = %#v, want %#v", got, want)
	}
	indices := messages[0]["indices"].([]int)
	if got, want := fmt.Sprint(indices), "[0 1 2 3]"; got != want {
		t.Fatalf("indices = %s, want %s", got, want)
	}
}

func TestParseSMSListMergesSameSenderWithinFiveSeconds(t *testing.T) {
	parts := []string{
		"欢迎使用湖北电信短信营业厅\r\n\r\n请回复序号：\r\n1  话费积分\r\n2  优惠促销\r\n3  业务办理\r\n5  客户信息管理\r\n6  常",
		"用信息查询\r\n7  充值交费\r\n8  流量包订购\r\n---------\r\n105  积分查询\r\n82  1/3/5/7天流量包\r\n--",
		"-------\r\n抽奖立得20元权益金！ 立即参与 https://waphb.189.cn/game/hd/index.html?a",
		"=dfgewtrs\r\n【中国电信】",
	}
	raw := `+CSCA: "+8613334262200",145
+CMGL: 4,"REC READ","10001",,"26/05/20,23:39:17+32"
` + testUCS2Hex(parts[0]) + `
+CMGL: 5,"REC READ","10001",,"26/05/20,23:39:17+32"
` + testUCS2Hex(parts[1]) + `
+CMGL: 6,"REC READ","10001",,"26/05/20,23:39:17+32"
` + testUCS2Hex(parts[2]) + `
+CMGL: 7,"REC READ","10001",,"26/05/20,23:39:18+32"
` + testUCS2Hex(parts[3]) + `
OK`

	data := parseSMSListAT(raw)
	messages := data["messages"].([]map[string]any)
	if len(messages) != 1 {
		t.Fatalf("len(messages) = %d, want 1: %#v", len(messages), messages)
	}
	if got, want := messages[0]["text"], normalizeSMSLineBreaks(strings.Join(parts, "")); got != want {
		t.Fatalf("text = %#v, want %#v", got, want)
	}
	indices := messages[0]["indices"].([]int)
	if got, want := fmt.Sprint(indices), "[4 5 6 7]"; got != want {
		t.Fatalf("indices = %s, want %s", got, want)
	}
}

func TestParseSMSListKeepsContinuousIndexOrder(t *testing.T) {
	parts := []string{
		`欢迎使用湖北电信短信营业厅

请回复序号：
1  话费积分
2  优惠促销
3  业务办理
5  客户信息管理
6  常`,
		`用信息查询
7  充值交费
8  流量包订购
---------
105  积分查询
82  1/3/5/7天流量包
--`,
		`-------
抽奖立得20元权益金！ 立即参与 https://waphb.189.cn/game/hd/index.html?a`,
		`=dfgewtrs
【中国电信】`,
	}
	raw := `+CSCA: "+8613334262200",145
+CMGL: 0,"REC READ","10001",,"26/05/20,23:46:24+32"
` + testUCS2Hex(parts[0]) + `
+CMGL: 1,"REC READ","10001",,"26/05/20,23:46:24+32"
` + testUCS2Hex(parts[1]) + `
+CMGL: 2,"REC READ","10001",,"26/05/20,23:46:24+32"
` + testUCS2Hex(parts[3]) + `
+CMGL: 3,"REC READ","10001",,"26/05/20,23:46:24+32"
` + testUCS2Hex(parts[2]) + `
OK`

	data := parseSMSListAT(raw)
	messages := data["messages"].([]map[string]any)
	if len(messages) != 1 {
		t.Fatalf("len(messages) = %d, want 1: %#v", len(messages), messages)
	}
	if got, want := messages[0]["text"], parts[0]+parts[1]+parts[3]+parts[2]; got != want {
		t.Fatalf("text = %#v, want %#v", got, want)
	}
	indices := messages[0]["indices"].([]int)
	if got, want := fmt.Sprint(indices), "[0 1 2 3]"; got != want {
		t.Fatalf("indices = %s, want %s", got, want)
	}
}

func TestParseSMSListKeepsNumberedMenuOrder(t *testing.T) {
	parts := []string{
		"您好，欢迎进入湖北电信短信营业厅，请回代码：\n101 实时话费\n102 账户余额\n103 上月账单\n104 历史账单\n105 积分查询",
		"\n106 积分兑换\n108 套餐使用情况\n110 缴费记录\n113 积分介绍\n返回首页导航请回复数字10001\t\n来中国电信APP领暖",
		"心福利包，抽取话费和金豆，参与请点击  http://a.189.cn/JeMHPJ",
	}
	raw := `+CSCA: "+8613334262200",145
+CMGL: 8,"REC READ","10001",,"26/05/21,00:04:01+32"
` + testUCS2Hex(parts[0]) + `
+CMGL: 9,"REC READ","10001",,"26/05/21,00:04:01+32"
` + testUCS2Hex(parts[1]) + `
+CMGL: 10,"REC READ","10001",,"26/05/21,00:04:01+32"
` + testUCS2Hex(parts[2]) + `
OK`

	data := parseSMSListAT(raw)
	messages := data["messages"].([]map[string]any)
	if len(messages) != 1 {
		t.Fatalf("len(messages) = %d, want 1: %#v", len(messages), messages)
	}
	if got, want := messages[0]["text"], strings.Join(parts, ""); got != want {
		t.Fatalf("text = %#v, want %#v", got, want)
	}
	indices := messages[0]["indices"].([]int)
	if got, want := fmt.Sprint(indices), "[8 9 10]"; got != want {
		t.Fatalf("indices = %s, want %s", got, want)
	}
}

func TestParseSMSListKeepsPointsExchangeFragmentsInStorageOrder(t *testing.T) {
	parts := []string{
		"您好，您的电信积分余额为3014分。回复以下代码兑换：\n【积分兑换流量】\n10622 兑换6GB全国流量需3000积分\n10671 兑",
		"换2GB全国流量需1000积分\n10621 兑换1GB全国流量需500积分\n【积分热兑推荐】\n10631 兑换视频彩铃半年卡需3600",
		"积分\n10661 兑换5元翼支付金需500积分\n1065  兑换热门权益包\n1061  兑换话费\n\n【营业厅积分兑换】积分兑换千兆光猫",
		"等可到附近电信营业厅咨询办理。\n更多积分兑换，请点击 http://a.189.cn/2EIJUu  登录电信营业厅APP查看。",
	}
	raw := `+CSCA: "+8613334262200",145
+CMGL: 4,"REC READ","10001",,"26/05/21,00:14:05+32"
` + testUCS2Hex(parts[0]) + `
+CMGL: 5,"REC READ","10001",,"26/05/21,00:14:05+32"
` + testUCS2Hex(parts[1]) + `
+CMGL: 6,"REC READ","10001",,"26/05/21,00:14:06+32"
` + testUCS2Hex(parts[2]) + `
+CMGL: 7,"REC READ","10001",,"26/05/21,00:14:06+32"
` + testUCS2Hex(parts[3]) + `
OK`

	data := parseSMSListAT(raw)
	messages := data["messages"].([]map[string]any)
	if len(messages) != 1 {
		t.Fatalf("len(messages) = %d, want 1: %#v", len(messages), messages)
	}
	if got, want := messages[0]["text"], strings.Join(parts, ""); got != want {
		t.Fatalf("text = %#v, want %#v", got, want)
	}
	indices := messages[0]["indices"].([]int)
	if got, want := fmt.Sprint(indices), "[4 5 6 7]"; got != want {
		t.Fatalf("indices = %s, want %s", got, want)
	}
}

func testUCS2Hex(value string) string {
	var b strings.Builder
	for _, r := range value {
		b.WriteString(fmt.Sprintf("%04X", r))
	}
	return b.String()
}

func TestDeviceInfoCommandSplitsStaticAndFreshSIMStatus(t *testing.T) {
	commands := pageATCommands(atKeyDeviceInfo)
	if len(commands) != 3 {
		t.Fatalf("device info commands = %#v, want static info, SIM/WWAN status, standalone LANIP", commands)
	}
	staticCommand := commands[0]
	freshCommand := commands[1]
	lanIPCommand := commands[2]
	for _, part := range []string{"+CGMI", "+CGSN", "+QGMR", "+CIMI", "+ICCID", "+CNUM"} {
		if !strings.Contains(staticCommand, part) {
			t.Fatalf("device static command = %q, want %s", staticCommand, part)
		}
	}
	if strings.Contains(staticCommand, "+CGMM") {
		t.Fatalf("device static command = %q, want model queried only by standalone AT+CGMM", staticCommand)
	}
	for _, part := range []string{"+QSIMSTAT?", "+CPIN?", `+QMAP="WWAN"`} {
		if !strings.Contains(freshCommand, part) {
			t.Fatalf("device fresh command = %q, want %s", freshCommand, part)
		}
	}
	if got := maxAgeForATCacheCommand(staticCommand); got != atCacheStaticMaxAge {
		t.Fatalf("static device maxAge = %s, want %s", got, atCacheStaticMaxAge)
	}
	if got := maxAgeForATCacheCommand(freshCommand); got != atCacheReadMaxAge {
		t.Fatalf("fresh device maxAge = %s, want %s", got, atCacheReadMaxAge)
	}
	if lanIPCommand != `AT+QMAP="LANIP"` {
		t.Fatalf("device LANIP command = %q, want AT+QMAP=\"LANIP\"", lanIPCommand)
	}
}

func TestParseDeviceInfoWithoutCGMMKeepsFirmwareSlot(t *testing.T) {
	raw := `+QSIMSTAT: 0,1
+CPIN: READY
Quectel
867123456789012
RG520NEBDGR03A03M4G
+QMAP: "LANIP",192.168.225.1,192.168.225.100,192.168.225.1
+QMAP: "WWAN",1,1,"IPV4","10.47.28.216"
460111234567890
+ICCID: 89861123456789012345
OK`

	data := parseDeviceInfoAT(raw)
	if got := data["manufacturer"]; got != "Quectel" {
		t.Fatalf("manufacturer = %#v, want Quectel", got)
	}
	if got := data["modelName"]; got != "-" {
		t.Fatalf("modelName = %#v, want standalone AT+CGMM to fill it later", got)
	}
	if got := data["firmwareVersion"]; got != "RG520NEBDGR03A03M4G" {
		t.Fatalf("firmwareVersion = %#v, want RG520NEBDGR03A03M4G", got)
	}
}

func TestParseDeviceInfoSIMInsertedWithoutCNUMDoesNotShowNotInserted(t *testing.T) {
	raw := `+QSIMSTAT: 0,1
+CPIN: READY
Quectel
RG520N
RG520NEBDGR03A03M4G
867123456789012
+QMAP: "LANIP",192.168.225.1,192.168.225.100,192.168.225.1
+QMAP: "WWAN",1,1,"IPV4","10.47.28.216"
460111234567890
+ICCID: 89861123456789012345
OK`

	data := parseDeviceInfoAT(raw)
	if got := data["simStatus"]; got != "已插卡" {
		t.Fatalf("simStatus = %#v, want 已插卡", got)
	}
	if got := data["simInserted"]; got != true {
		t.Fatalf("simInserted = %#v, want true", got)
	}
	if got := data["phoneNumber"]; got == "未插卡" {
		t.Fatalf("phoneNumber = %#v, want inserted-card fallback instead of 未插卡", got)
	}
	if got := data["phoneNumber"]; got != "无本机号码" {
		t.Fatalf("phoneNumber = %#v, want 无本机号码", got)
	}
	if got := data["imsi"]; got != "460111234567890" {
		t.Fatalf("imsi = %#v, want parsed IMSI", got)
	}
	if got := data["iccid"]; got != "89861123456789012345" {
		t.Fatalf("iccid = %#v, want parsed ICCID", got)
	}
}

func TestParseDeviceInfoSIMAbsentClearsStaleSIMData(t *testing.T) {
	raw := `Quectel
RG520N
RG520NEBDGR03A03M4G
867123456789012
+QMAP: "LANIP",192.168.225.1,192.168.225.100,192.168.225.1
+QSIMSTAT: 0,0
+CPIN: NOT INSERTED
+QMAP: "WWAN",1,1,"IPV4","10.47.28.216"
460111234567890
+ICCID: 89861123456789012345
+CME ERROR: 10
OK`

	data := parseDeviceInfoAT(raw)
	if got := data["simStatus"]; got != "未插卡" {
		t.Fatalf("simStatus = %#v, want 未插卡", got)
	}
	if got := data["simInserted"]; got != false {
		t.Fatalf("simInserted = %#v, want false", got)
	}
	if got := data["phoneNumber"]; got != "未插卡" {
		t.Fatalf("phoneNumber = %#v, want 未插卡", got)
	}
	if got := data["manufacturer"]; got != "Quectel" {
		t.Fatalf("manufacturer = %#v, want Quectel", got)
	}
	if got := data["modelName"]; got != "RG520N" {
		t.Fatalf("modelName = %#v, want RG520N", got)
	}
	if got := data["firmwareVersion"]; got != "RG520NEBDGR03A03M4G" {
		t.Fatalf("firmwareVersion = %#v, want RG520NEBDGR03A03M4G", got)
	}
	if got := data["lanIp"]; got != "192.168.225.1" {
		t.Fatalf("lanIp = %#v, want 192.168.225.1", got)
	}
	if got := data["imsi"]; got != "-" {
		t.Fatalf("imsi = %#v, want cleared", got)
	}
	if got := data["iccid"]; got != "-" {
		t.Fatalf("iccid = %#v, want cleared", got)
	}
	if got := data["wwanIpv4"]; got != "-" {
		t.Fatalf("wwanIpv4 = %#v, want cleared", got)
	}
}

func TestParseDashboardSIMAbsentOverridesStaleNetworkData(t *testing.T) {
	raw := `AT+QSIMSTAT?;+CSQ;+QMAP="WWAN";+QENG="servingcell";+CGCONTRDP=1

+QSIMSTAT: 0,0

+CSQ: 99,99

+QMAP: "WWAN",1,1,"IPV4","10.47.28.216"
+QMAP: "WWAN",1,1,"IPV6","240e:45d:820:43e6:a098:a1e9:3b2b:e417"

+QENG: "servingcell","NOCONN","NR5G-SA","TDD",460,11,94203C105,543,8EB001,627264,78,12,-90,-11,12,1,-

+CGCONTRDP: 1,0,"ctnet","10.47.28.216","36.14.4.93.8.32.67.230.24.177.68.18.213.197.193.145"

OK`

	data := parseDashboardAT(raw)
	if got := data["sim"]; got != "未激活" {
		t.Fatalf("sim = %#v, want 未激活", got)
	}
	if got := data["network_mode"]; got != "未插卡" {
		t.Fatalf("network_mode = %#v, want 未插卡", got)
	}
	if got := data["ipv4"]; got != "-" {
		t.Fatalf("ipv4 = %#v, want -", got)
	}
	if got := data["ipv6"]; got != "-" {
		t.Fatalf("ipv6 = %#v, want -", got)
	}
	if got := data["rsrpNR"]; got != "-" {
		t.Fatalf("rsrpNR = %#v, want -", got)
	}
}

func TestParseDashboardSIMInsertedKeepsNetworkData(t *testing.T) {
	raw := `+QSIMSTAT: 0,1
+QMAP: "WWAN",1,1,"IPV4","10.47.28.216"
+QENG: "servingcell","NOCONN","NR5G-SA","TDD",460,11,94203C105,543,8EB001,627264,78,12,-90,-11,12,1,-
+QCAINFO: "PCC",627264,12,"NR5G BAND 78",543
+QRSRP: -99,-89,-107,-93,NR5G
OK`

	data := parseDashboardAT(raw)
	if got := data["sim"]; got != "已激活" {
		t.Fatalf("sim = %#v, want 已激活", got)
	}
	if got := data["ipv4"]; got != "10.47.28.216" {
		t.Fatalf("ipv4 = %#v, want 10.47.28.216", got)
	}
	if got := data["network_mode"]; got == "未插卡" || got == "-" {
		t.Fatalf("network_mode = %#v, want parsed network mode", got)
	}
}

func TestParseDashboardQCAInfoKeepsDuplicateCarriers(t *testing.T) {
	raw := `+QSIMSTAT: 0,1
+QSPN: "mobily","mobily","mobily",0,"42003"
+QMAP: "WWAN",1,1,"IPV4","10.219.172.53"
+QMAP: "WWAN",1,1,"IPV6","2a02:9b0:4070:fb:25a7:ccee:35bc:8062"
+QENG: "servingcell","NOCONN"
+QENG: "LTE","FDD",420,03,29E5B01,445,1850,3,5,5,439E,-88,-7,-61,15,10,200,-
+QENG: "NR5G-NSA",420,03,542,-98,18,-10,660768,77,12,1
+QCAINFO: "PCC",1850,100,"LTE BAND 3",1,445,-88,-8,-61,10
+QCAINFO: "SCC",300,100,"LTE BAND 1",1,445,-96,-12,-74,6,0,-,-
+QCAINFO: "SCC",40590,100,"LTE BAND 41",1,418,-107,-15,-83,0,0,-,-
+QCAINFO: "SCC",40392,100,"LTE BAND 41",1,418,-105,-11,-84,1,0,-,-
+QCAINFO: "SCC",660768,12,"NR5G BAND 77",542
+QRSRP: -91,-88,-140,-140,LTE
+QRSRP: -120,-98,-110,-106,NR5G
+CGCONTRDP: 1,5,"WEB2","10.219.172.53","42.2.9.176.64.112.0.251.24.115.78.183.236.93.220.56"
+QGDNRCNT: 1961485,17217894
+QGDCNT: 1979589,17236170
+CSQ: 28,99
OK`

	data := parseDashboardAT(raw)
	if got, want := data["bands"], "B3 / B1 / B41 / B41 / N77"; got != want {
		t.Fatalf("bands = %#v, want %s", got, want)
	}
	if got, want := data["bandwidth"], "20MHz / 20MHz / 20MHz / 20MHz / 100MHz"; got != want {
		t.Fatalf("bandwidth = %#v, want %s", got, want)
	}
	if got, want := data["earfcns"], "1850 / 300 / 40590 / 40392 / 660768"; got != want {
		t.Fatalf("earfcns = %#v, want %s", got, want)
	}
}

func TestParseDashboardQCAInfoNR5GSCCPCIUsesSecondPCIField(t *testing.T) {
	raw := `+QSIMSTAT: 0,1
+QCAINFO: "PCC",633984,12,"NR5G BAND 78",919
+QCAINFO: "SCC",428910,3,"NR5G BAND 1",1,143,0,-,-
OK`

	data := parseDashboardAT(raw)
	if got, want := data["pcc_pci"], "919"; got != want {
		t.Fatalf("pcc_pci = %#v, want %s", got, want)
	}
	if got, want := data["scc_pci"], "143"; got != want {
		t.Fatalf("scc_pci = %#v, want %s", got, want)
	}
}

func TestMockDashboardPayloadOverrideParsesManualInput(t *testing.T) {
	clearMockATPayload("")
	defer clearMockATPayload("")

	setMockATPayload("at", `+QSIMSTAT: 0,1
+QSPN: "4E2D56FD75354FE1","4E2D56FD75354FE1","4E2D56FD75354FE1",1,"46011"
+QMAP: "WWAN",1,1,"IPV4","10.47.28.216"
+QENG: "servingcell","NOCONN","NR5G-SA","TDD",460,11,94203C105,543,8EB001,627264,78,12,-90,-11,12,1,-
+QCAINFO: "PCC",627264,12,"NR5G BAND 78",543
+QRSRP: -99,-89,-107,-93,NR5G
OK`)

	data := parseDashboardAT(currentMockDashboardATResponse(pageATCommand(atKeyDashboard)))
	if got := data["network_provider"]; got != "中国电信" {
		t.Fatalf("network_provider = %#v, want 中国电信", got)
	}
	if got := data["network_mode"]; got != "NR5G-SA TDD" {
		t.Fatalf("network_mode = %#v, want NR5G-SA TDD", got)
	}
	if got := data["ipv4"]; got != "10.47.28.216" {
		t.Fatalf("ipv4 = %#v, want 10.47.28.216", got)
	}
}

func TestMockQENGPayloadReplacesDefaultDashboardLines(t *testing.T) {
	clearMockATPayload("")
	defer clearMockATPayload("")

	setMockATPayload("qeng", `+QENG: "servingcell","NOCONN","NR5G-SA","TDD",460,11,94203C105,543,8EB001,627264,78,12,-90,-11,12,1,-`)
	raw := currentMockDashboardATResponse(pageATCommand(atKeyDashboard))
	if strings.Contains(raw, `+QENG: "LTE"`) || strings.Contains(raw, `+QENG: "NR5G-NSA"`) {
		t.Fatalf("default QENG lines were not replaced: %s", raw)
	}
	if !strings.Contains(raw, `NR5G-SA`) {
		t.Fatalf("manual QENG payload missing from raw: %s", raw)
	}
}

func TestSignalPercentageUsesWeightedRsrpRsrqSinr(t *testing.T) {
	raw := `+QSIMSTAT: 0,1
+QENG: "servingcell","NOCONN","NR5G-SA","TDD",460,11,94203C105,543,8EB001,627264,78,12,-90,-11,12,1,-
OK`

	data := parseDashboardAT(raw)
	checks := map[string]int{
		"rsrpNRPercentage": 75,
		"rsrqNRPercentage": 75,
		"sinrNRPercentage": 60,
		"signalPercentage": 71,
	}
	for key, want := range checks {
		if got := data[key]; got != want {
			t.Fatalf("%s = %#v, want %d", key, got, want)
		}
	}
}

func TestSignalPercentageThresholds(t *testing.T) {
	checks := []struct {
		name string
		got  int
		want int
	}{
		{"rsrp bad", calcRSRP(-120), 0},
		{"rsrp good", calcRSRP(-80), 100},
		{"rsrq bad", calcRSRQ(-20), 0},
		{"rsrq good", calcRSRQ(-8), 100},
		{"sinr bad", calcSINR(0), 0},
		{"sinr good", calcSINR(20), 100},
		{"weighted", weightedSignalPercent(80, 60, 40), 65},
	}
	for _, check := range checks {
		if check.got != check.want {
			t.Fatalf("%s = %d, want %d", check.name, check.got, check.want)
		}
	}
}

func TestNativeConsoleHidesInnerScrollbars(t *testing.T) {
	checks := []string{
		"html,body{height:100%;margin:0;overflow:hidden",
		"body{display:flex;flex-direction:column;min-height:0}",
		"#term{flex:1 1 auto;min-height:0;height:auto;box-sizing:border-box;overflow:auto;scrollbar-width:none",
		"#term::-webkit-scrollbar{width:0;height:0}",
	}
	for _, want := range checks {
		if !strings.Contains(nativeConsoleHTML, want) {
			t.Fatalf("nativeConsoleHTML missing %q", want)
		}
	}
}

func TestNormalizeSMSNumberUsesCIMIMCCCallingCode(t *testing.T) {
	got, err := normalizeSMSNumber("138 0013 8000", "AT+CIMI\r\n460111234567890\r\nOK\r\n")
	if err != nil {
		t.Fatalf("normalizeSMSNumber returned error: %v", err)
	}
	if got != "+8613800138000" {
		t.Fatalf("normalizeSMSNumber = %q, want +8613800138000", got)
	}
}

func TestNormalizeSMSNumberKeepsInternationalNumber(t *testing.T) {
	got, err := normalizeSMSNumber("+49 170 1234567", "")
	if err != nil {
		t.Fatalf("normalizeSMSNumber returned error: %v", err)
	}
	if got != "+491701234567" {
		t.Fatalf("normalizeSMSNumber = %q, want +491701234567", got)
	}
}

func TestNormalizeSMSNumberConvertsIDDPrefix(t *testing.T) {
	got, err := normalizeSMSNumber("0086 13800138000", "")
	if err != nil {
		t.Fatalf("normalizeSMSNumber returned error: %v", err)
	}
	if got != "+8613800138000" {
		t.Fatalf("normalizeSMSNumber = %q, want +8613800138000", got)
	}
}

func TestNormalizeSMSNumberReturnsErrorForUnknownMCC(t *testing.T) {
	_, err := normalizeSMSNumber("13800138000", "AT+CIMI\r\n999111234567890\r\nOK\r\n")
	if err == nil {
		t.Fatalf("normalizeSMSNumber error = nil, want unsupported MCC error")
	}
	if !strings.Contains(err.Error(), "MCC 999") {
		t.Fatalf("normalizeSMSNumber error = %q, want MCC 999", err.Error())
	}
}

func TestMCCToCallingCodeUsesProvidedMapping(t *testing.T) {
	cases := map[string]string{
		"420": "966",
		"460": "86",
		"310": "1",
		"262": "49",
		"999": "",
	}
	for mcc, want := range cases {
		if got := mccToCallingCode(mcc); got != want {
			t.Fatalf("mccToCallingCode(%q) = %q, want %q", mcc, got, want)
		}
	}
}

func TestMergeSMSFragmentsByConcatRefAcrossInterleavedMessages(t *testing.T) {
	entries := []map[string]any{
		{"sender": "10001", "date": "26/06/03,10:21:21+32", "text": "欢迎使用湖北电信短信营业厅\n", "indices": []int{3}, "concatRef": "10001:8:6B", "concatTotal": 4, "concatSeq": 1},
		{"sender": "10001", "date": "26/06/03,02:52:15+32", "text": "旧短信", "indices": []int{4, 5, 6}, "concatTotal": 3},
		{"sender": "10001", "date": "26/06/03,10:21:21+32", "text": "请回复序号：", "indices": []int{7}, "concatRef": "10001:8:6B", "concatTotal": 4, "concatSeq": 2},
		{"sender": "10001", "date": "26/06/03,10:21:21+32", "text": "1 话费积分", "indices": []int{8}, "concatRef": "10001:8:6B", "concatTotal": 4, "concatSeq": 3},
		{"sender": "10001", "date": "26/06/03,10:21:21+32", "text": "【中国电信】", "indices": []int{9}, "concatRef": "10001:8:6B", "concatTotal": 4, "concatSeq": 4},
	}

	messages := mergeSMSFragments(entries)
	if len(messages) != 2 {
		t.Fatalf("len(messages) = %d, want 2: %#v", len(messages), messages)
	}
	if got := fmt.Sprint(messages[0]["indices"]); got != "[3 7 8 9]" {
		t.Fatalf("merged indices = %s, want [3 7 8 9]", got)
	}
	if got := fmt.Sprint(messages[0]["text"]); !strings.Contains(got, "欢迎使用湖北电信短信营业厅") || !strings.Contains(got, "【中国电信】") {
		t.Fatalf("merged text = %q, want first and last fragments", got)
	}
	if got, want := messages[0]["concatTotal"], 4; got != want {
		t.Fatalf("concatTotal = %#v, want %#v", got, want)
	}
	if got, want := messages[0]["concatRef"], "10001:8:6B"; got != want {
		t.Fatalf("concatRef = %#v, want %#v", got, want)
	}
	if got := fmt.Sprint(messages[1]["indices"]); got != "[4 5 6]" {
		t.Fatalf("second message indices = %s, want [4 5 6]", got)
	}
}

func TestSMSListMetaOnlyRemovesTextPayload(t *testing.T) {
	data := map[string]any{
		"serviceCenters": []string{"+8613800100500"},
		"messages": []map[string]any{
			{
				"sender":      "10001",
				"date":        "26/06/03,02:52:15+32",
				"text":        strings.Repeat("短信正文", 100),
				"textLines":   []string{"短信正文"},
				"indices":     []int{4, 5, 6},
				"concatTotal": 3,
			},
		},
	}
	meta := smsListMetaOnly(data)
	messages := meta["messages"].([]map[string]any)
	if len(messages) != 1 {
		t.Fatalf("len(messages) = %d, want 1", len(messages))
	}
	if _, ok := messages[0]["text"]; ok {
		t.Fatalf("list_meta should not include text: %#v", messages[0])
	}
	if _, ok := messages[0]["textLines"]; ok {
		t.Fatalf("list_meta should not include textLines: %#v", messages[0])
	}
	if got, want := messages[0]["concatTotal"], 3; got != want {
		t.Fatalf("concatTotal = %#v, want %#v", got, want)
	}
	if got := fmt.Sprint(messages[0]["indices"]); got != "[4 5 6]" {
		t.Fatalf("indices = %s, want [4 5 6]", got)
	}
}

func TestSMSListCommandUsesPDUMode(t *testing.T) {
	command := smsListATCommand()
	if !strings.Contains(command, "+CMGF=0") {
		t.Fatalf("smsListATCommand = %q, want PDU mode +CMGF=0", command)
	}
	if !strings.Contains(command, "+CMGL=4") {
		t.Fatalf("smsListATCommand = %q, want PDU list +CMGL=4", command)
	}
	if strings.Contains(command, "+CMGF=1") || strings.Contains(command, `+CMGL="ALL"`) {
		t.Fatalf("smsListATCommand = %q, still contains text mode SMS command", command)
	}
}

func TestParseSMSListPDUModeUCS2KeepsNewlines(t *testing.T) {
	pdu := buildMockSMSDeliverPDU("+8610001", "第一行\n第二行")
	raw := fmt.Sprintf("+CMGL: 2,0,,%d\r\n%s\r\nOK\r\n", len(pdu)/2-1, pdu)
	data := parseSMSListAT(raw)
	messages := data["messages"].([]map[string]any)
	if len(messages) != 1 {
		t.Fatalf("len(messages) = %d, want 1: %#v", len(messages), messages)
	}
	if got, want := messages[0]["sender"], "+8610001"; got != want {
		t.Fatalf("sender = %#v, want %#v", got, want)
	}
	if got, want := messages[0]["text"], "第一行\n第二行"; got != want {
		t.Fatalf("text = %#v, want %#v", got, want)
	}
}

func TestParseSMSListPDUModeUCS2ConvertsCarriageReturnToNewline(t *testing.T) {
	pdu := buildMockSMSDeliverPDU("+8610001", "第一行\r第二行")
	raw := fmt.Sprintf("+CMGL: 3,0,,%d\r\n%s\r\nOK\r\n", len(pdu)/2-1, pdu)
	data := parseSMSListAT(raw)
	messages := data["messages"].([]map[string]any)
	if len(messages) != 1 {
		t.Fatalf("len(messages) = %d, want 1: %#v", len(messages), messages)
	}
	if got, want := messages[0]["text"], "第一行\n第二行"; got != want {
		t.Fatalf("text = %#v, want %#v", got, want)
	}
}

func TestNormalizeSMSLineBreaksHandlesUnicodeSeparators(t *testing.T) {
	got := normalizeSMSLineBreaks("第一行\u2028第二行\u2029第三行\u0085第四行")
	want := "第一行\n第二行\n第三行\n第四行"
	if got != want {
		t.Fatalf("normalizeSMSLineBreaks = %#v, want %#v", got, want)
	}
}

func TestParseSMSListPDUModeAddsTextLines(t *testing.T) {
	pdu := buildMockSMSDeliverPDU("+8610001", "第一行\n\n第三行")
	raw := fmt.Sprintf("+CMGL: 4,0,,%d\r\n%s\r\nOK\r\n", len(pdu)/2-1, pdu)
	data := parseSMSListAT(raw)
	messages := data["messages"].([]map[string]any)
	if len(messages) != 1 {
		t.Fatalf("len(messages) = %d, want 1: %#v", len(messages), messages)
	}
	lines, ok := messages[0]["textLines"].([]string)
	if !ok {
		t.Fatalf("textLines has type %T, want []string", messages[0]["textLines"])
	}
	want := []string{"第一行", "", "第三行"}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("textLines = %#v, want %#v", lines, want)
	}
}

func TestNormalizeSMSLineBreaksHandlesLiteralEscapesAndVerticalSeparators(t *testing.T) {
	got := normalizeSMSLineBreaks(`第一行\n第二行\r\n第三行` + "\v第四行\f第五行")
	want := "第一行\n第二行\n第三行\n第四行\n第五行"
	if got != want {
		t.Fatalf("normalizeSMSLineBreaks = %#v, want %#v", got, want)
	}
}

func TestBuildSMSSubmitPDUsUsesPDUCMGSLength(t *testing.T) {
	segments := buildSMSSubmitPDUs("+8613800138000", "测试短信", 9)
	if len(segments) != 1 {
		t.Fatalf("len(segments) = %d, want 1", len(segments))
	}
	if !strings.HasPrefix(segments[0].PDUHex, "00") {
		t.Fatalf("PDUHex = %q, want SMSC length prefix 00", segments[0].PDUHex)
	}
	if got, want := segments[0].TPDUOctets, len(segments[0].PDUHex)/2-1; got != want {
		t.Fatalf("TPDUOctets = %d, want %d", got, want)
	}
}

func TestParseModelATSupportsPlainCGMMResponse(t *testing.T) {
	raw := "AT+CGMM\r\nRG520N-EB\r\n\r\nOK\r\n"
	if got := parseModelAT(raw); got != "RG520N-EB" {
		t.Fatalf("parseModelAT = %q, want RG520N-EB", got)
	}
}

func TestCGMMBypassesStartupReadDelay(t *testing.T) {
	if !isATImmediateReadCommand(`AT+CGMM`) {
		t.Fatalf("AT+CGMM should bypass startup read delay")
	}
	if isATImmediateReadCommand(`AT+CGSN`) {
		t.Fatalf("AT+CGSN should not bypass startup read delay")
	}
}

func TestParseModelATSupportsCGMMPrefix(t *testing.T) {
	raw := "AT+CGMM\r\n+CGMM: RG520N-EB\r\n\r\nOK\r\n"
	if got := parseModelAT(raw); got != "RG520N-EB" {
		t.Fatalf("parseModelAT = %q, want RG520N-EB", got)
	}
}

func TestParseModelATSupportsQuotedCGMMPrefix(t *testing.T) {
	raw := "AT+CGMM\r\n+CGMM: \"RG520N-EB\"\r\n\r\nOK\r\n"
	if got := parseModelAT(raw); got != "RG520N-EB" {
		t.Fatalf("parseModelAT = %q, want RG520N-EB", got)
	}
}

func TestModelCommandUsesStandaloneCGMMOnly(t *testing.T) {
	command := pageATCommand(atKeyModel)
	if command != `AT+CGMM` {
		t.Fatalf("model command = %q, want AT+CGMM", command)
	}
	if strings.Contains(pageATCommand(atKeyDeviceInfo), "+CGMM") {
		t.Fatalf("device info command = %q, model must be queried by standalone AT+CGMM only", pageATCommand(atKeyDeviceInfo))
	}
	standaloneCGMM := 0
	for _, command := range commonATCacheCommands() {
		if strings.Contains(command, "+GMM") || strings.Contains(command, "AT+GMM") {
			t.Fatalf("common AT cache command still contains GMM: %q", command)
		}
		if strings.Contains(command, "+CGMM") && command != `AT+CGMM` {
			t.Fatalf("common AT cache command combines CGMM with other commands: %q", command)
		}
		if command == `AT+CGMM` {
			standaloneCGMM++
		}
	}
	if standaloneCGMM != 0 {
		t.Fatalf("standalone AT+CGMM cache commands = %d, want 0; model is fetched only on demand and then cached", standaloneCGMM)
	}
}

func TestFetchStandaloneModelUsesRuntimeCacheBeforeAT(t *testing.T) {
	s := &simpleAdminServer{}
	s.storeStandaloneModel("RG520N-EB")

	model, pending := s.fetchStandaloneModel(true)
	if model != "RG520N-EB" || pending {
		t.Fatalf("fetchStandaloneModel = (%q, %v), want cached RG520N-EB and pending=false", model, pending)
	}
}

func TestStandaloneModelCommandNotPeriodicallyRefreshed(t *testing.T) {
	mgr := &atCommandCacheManager{
		entries: map[string]*atCacheEntry{
			`AT+CGMM`: {command: `AT+CGMM`, response: "RG520N-EB", updatedAt: time.Now().Add(-24 * time.Hour)},
			`AT+CGMI`: {command: `AT+CGMI`, response: "Quectel", updatedAt: time.Now().Add(-24 * time.Hour)},
		},
	}

	commands := mgr.cachedReadCommandsNeedingRefresh()
	for _, command := range commands {
		if command == `AT+CGMM` {
			t.Fatalf("cachedReadCommandsNeedingRefresh includes AT+CGMM: %#v", commands)
		}
	}
	if len(commands) != 1 || commands[0] != `AT+CGMI` {
		t.Fatalf("cachedReadCommandsNeedingRefresh = %#v, want only AT+CGMI", commands)
	}
}

func TestSMSListCommandNotPeriodicallyRefreshed(t *testing.T) {
	cmd := smsListATCommand()
	mgr := &atCommandCacheManager{
		entries: map[string]*atCacheEntry{
			cmd: {command: cmd, response: "OK", updatedAt: time.Now().Add(-24 * time.Hour)},
		},
	}
	commands := mgr.cachedReadCommandsNeedingRefresh()
	if len(commands) != 0 {
		t.Fatalf("cachedReadCommandsNeedingRefresh = %#v, want SMS list excluded from periodic refresh", commands)
	}
}

func TestCacheMaxAgesClassifyStableConfigQueries(t *testing.T) {
	commands := pageATCommands(atKeyNetworkBands)
	if got := maxAgeForATCacheCommand(commands[0]); got != atCacheConfigMaxAge {
		t.Fatalf("network bands maxAge = %s, want %s", got, atCacheConfigMaxAge)
	}
	settings := pageATCommands(atKeySettingsStatus)
	if got := maxAgeForATCacheCommand(settings[0]); got != atCacheConfigMaxAge {
		t.Fatalf("settings status config maxAge = %s, want %s", got, atCacheConfigMaxAge)
	}
	if got := maxAgeForATCacheCommand(settings[1]); got != atCacheStaticMaxAge {
		t.Fatalf("settings IMEI maxAge = %s, want %s", got, atCacheStaticMaxAge)
	}
	if got := maxAgeForATCacheCommand(settings[2]); got != atCacheConfigMaxAge {
		t.Fatalf("LANIP maxAge = %s, want %s", got, atCacheConfigMaxAge)
	}
	networkSettings := pageATCommands(atKeyNetworkSettings)
	if got := maxAgeForATCacheCommand(networkSettings[0]); got != atCacheSemiStaticMaxAge {
		t.Fatalf("network preference status maxAge = %s, want %s", got, atCacheSemiStaticMaxAge)
	}
	if got := maxAgeForATCacheCommand(networkSettings[1]); got != atCacheReadMaxAge {
		t.Fatalf("QCAINFO maxAge = %s, want %s", got, atCacheReadMaxAge)
	}
}

func TestEnsureAuthFileResetsEmptyFile(t *testing.T) {
	dir := t.TempDir()
	authFile := filepath.Join(dir, "simpleadmin.auth")
	if err := os.WriteFile(authFile, nil, 0600); err != nil {
		t.Fatalf("write empty auth file: %v", err)
	}
	if err := ensureAuthFile(authFile); err != nil {
		t.Fatalf("ensureAuthFile: %v", err)
	}
	auth, err := loadAuthConfig(authFile)
	if err != nil {
		t.Fatalf("load auth: %v", err)
	}
	if auth.Username != "admin" || auth.Password != "admin" {
		t.Fatalf("auth = %#v, want admin/admin", auth)
	}
}

func TestEnsureAuthFileKeepsExistingValidFile(t *testing.T) {
	dir := t.TempDir()
	authFile := filepath.Join(dir, "simpleadmin.auth")
	if err := os.WriteFile(authFile, []byte("admin:custompass\n"), 0600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}
	if err := ensureAuthFile(authFile); err != nil {
		t.Fatalf("ensureAuthFile: %v", err)
	}
	auth, err := loadAuthConfig(authFile)
	if err != nil {
		t.Fatalf("load auth: %v", err)
	}
	if auth.Username != "admin" || auth.Password != "custompass" {
		t.Fatalf("auth = %#v, want admin/custompass", auth)
	}
}
