package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/subtle"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultStaticDir      = "/usrdata/simpleadmin/www"
	defaultAuthFile       = "/usrdata/simpleadmin/simpleadmin.auth"
	defaultCertFile       = "/usrdata/simpleadmin/server.crt"
	defaultKeyFile        = "/usrdata/simpleadmin/server.key"
	defaultCACertFile     = "/usrdata/simpleadmin/zbims-ca.crt"
	defaultCAKeyFile      = "/usrdata/simpleadmin/zbims-ca.key"
	defaultATDevice       = "/dev/smd11"
	defaultATDevicesFile  = "/usrdata/simpleadmin/at_devices.conf"
	defaultTTLValueFile   = "/usrdata/simpleadmin/ttlvalue"
	legacyTTLValueFile    = "/usrdata/simplefirewall/ttlvalue"
	languageConfigRelPath = "config/get_language.json"
	defaultLanguage       = "zh-CN"
)

type serverConfig struct {
	staticDir     string
	authFile      string
	certFile      string
	keyFile       string
	caCertFile    string
	caKeyFile     string
	httpAddr      string
	httpsAddr     string
	ttlFile       string
	atDevices     string
	atDevicesFile string
	noTLS         bool
	mockMode      bool
	atDebug       bool
}

type authConfig struct {
	Username string
	Password string
}

var processStartTime = time.Now()
var runtimeTTLValueFile = defaultTTLValueFile
var runtimeATDevices = defaultATDeviceCandidates()
var atDebugEnabled = envBool("SIMPLEADMIN_AT_DEBUG")

type languageConfig struct {
	Language string `json:"language"`
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	if len(os.Args) > 1 && os.Args[1] == "root-password-init" {
		marker := filepath.Join(filepath.Dir(defaultAuthFile), "root-password.initialized")
		if _, err := os.Stat(marker); err == nil {
			return
		}
		if err := changeSystemRootPassword("", "admin", true); err != nil {
			log.Fatal(err)
		}
		if err := writePersistentFile(marker, []byte("1\n"), 0600); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "passwd" {
		data, err := io.ReadAll(io.LimitReader(os.Stdin, 131))
		if err == nil {
			err = writeAuthConfig(defaultAuthFile, authConfig{Username: "admin", Password: strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")})
		}
		if err != nil {
			if persistenceCommitted(err) {
				log.Print(err)
				os.Exit(2)
			}
			log.Fatal(err)
		}
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "__smd-reader" {
		runSMDReaderCommand(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "ttl" {
		runTTLCommand(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "at" {
		runATCLICommand(os.Args[2:])
		return
	}

	args := os.Args[1:]
	if len(args) > 0 && args[0] == "serve" {
		args = args[1:]
	}
	runServeCommand(args)
}

func runServeCommand(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cfg := serverConfig{}
	fs.StringVar(&cfg.staticDir, "static", defaultStaticDir, "静态网页目录")
	fs.StringVar(&cfg.authFile, "auth-file", defaultAuthFile, "登录用户密码文件，格式为 username:password")
	fs.StringVar(&cfg.certFile, "cert", defaultCertFile, "HTTPS 服务器证书文件")
	fs.StringVar(&cfg.keyFile, "key", defaultKeyFile, "HTTPS 服务器私钥文件")
	fs.StringVar(&cfg.caCertFile, "ca-cert", defaultCACertFile, "HTTPS 本地根 CA 证书文件")
	fs.StringVar(&cfg.caKeyFile, "ca-key", defaultCAKeyFile, "HTTPS 本地根 CA 私钥文件")
	fs.StringVar(&cfg.httpAddr, "http", ":80", "HTTP 监听地址")
	fs.StringVar(&cfg.httpsAddr, "https", ":443", "HTTPS 监听地址")
	fs.StringVar(&cfg.ttlFile, "ttl-file", defaultTTLValueFile, "TTL 状态文件")
	fs.StringVar(&cfg.atDevices, "at-devices", "", "AT 设备列表，多个路径用逗号分隔；默认直接使用 /dev/smd11")
	fs.StringVar(&cfg.atDevicesFile, "at-devices-file", defaultATDevicesFile, "AT 客户端设备配置文件，每行一个设备路径")
	fs.BoolVar(&cfg.noTLS, "no-tls", true, "只启用 HTTP，不启用 HTTPS")
	fs.BoolVar(&cfg.mockMode, "mock", false, "启用本地测试 mock 模式，不访问真实串口、systemd 或 TTL 规则")
	fs.BoolVar(&cfg.atDebug, "at-debug", envBool("SIMPLEADMIN_AT_DEBUG"), "启用 AT 详细调试日志")
	_ = fs.Parse(args)
	persistenceMock = cfg.mockMode

	runtimeTTLValueFile = cfg.ttlFile
	atDebugEnabled = cfg.atDebug
	runtimeATDevices = collectATDeviceCandidates(cfg.atDevices, cfg.atDevicesFile)
	log.Printf("AT debug logging: %v", atDebugEnabled)
	log.Printf("AT client device candidates: %s", strings.Join(runtimeATDevices, ", "))
	logATDeviceStat("client candidate", runtimeATDevices)
	if err := validateStaticDir(cfg.staticDir); err != nil {
		log.Fatalf("静态网页目录无效: %v", err)
	}
	if absStaticDir, err := filepath.Abs(cfg.staticDir); err == nil {
		log.Printf("Static web directory: %s", absStaticDir)
	} else {
		log.Printf("Static web directory: %s", cfg.staticDir)
	}
	if cfg.mockMode {
		log.Printf("ZBIMS mock mode enabled: hardware-dependent APIs return local test data")
		startMockATConsole(true)
	}

	if err := ensureAuthFile(cfg.authFile); err != nil {
		log.Fatalf("初始化认证文件失败: %v", err)
	}

	if !cfg.mockMode {
		log.Printf("AT APIs use direct /dev/smd11 access")
		applySavedTTLAtStartup()
	}
	atCommandCache.Start(cfg.mockMode)

	app := &simpleAdminServer{cfg: cfg}
	cleanupContext, stopCleanup := context.WithCancel(context.Background())
	defer stopCleanup()
	go app.cleanupSessions(cleanupContext)
	app.telemetry = newTelemetryMonitor(filepath.Join(filepath.Dir(cfg.authFile), "monitor.json"), cfg.mockMode)
	app.telemetry.start(cleanupContext)
	handler := app.routes()

	if cfg.noTLS {
		log.Printf("ZBIMS HTTP 服务启动: %s", cfg.httpAddr)
		log.Fatal(newHTTPServer(cfg.httpAddr, handler).ListenAndServe())
		return
	}

	if err := ensureManagedTLSCertificate(cfg.certFile, cfg.keyFile, cfg.caCertFile, cfg.caKeyFile); err != nil {
		log.Fatalf("初始化 HTTPS 证书失败: %v", err)
	}

	go func() {
		redirectHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host := r.Host
			if strings.Contains(host, ":") {
				host = strings.Split(host, ":")[0]
			}
			target := "https://" + host + r.URL.RequestURI()
			http.Redirect(w, r, target, http.StatusMovedPermanently)
		})
		log.Printf("HTTP 重定向服务启动: %s", cfg.httpAddr)
		if err := newHTTPServer(cfg.httpAddr, redirectHandler).ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("HTTP 重定向服务退出: %v", err)
		}
	}()

	log.Printf("ZBIMS HTTPS 服务启动: %s", cfg.httpsAddr)
	log.Fatal(newHTTPServer(cfg.httpsAddr, handler).ListenAndServeTLS(cfg.certFile, cfg.keyFile))
}

type simpleAdminServer struct {
	cfg                serverConfig
	authMu             sync.Mutex
	sessionMu          sync.Mutex
	sessions           map[string]time.Time
	sessionConnections map[net.Conn]string
	telemetry          *telemetryMonitor
	mockRootPassword   string

	modelMu           sync.RWMutex
	cachedModuleModel string
}

func (s *simpleAdminServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/logout", s.handleLogout)
	mux.HandleFunc("/api/set_password", s.handleSetPassword)
	mux.HandleFunc("/api/set_root_password", s.handleSetRootPassword)
	mux.HandleFunc("/api/telemetry", s.handleTelemetry)
	mux.HandleFunc("/api/telemetry/target", s.handleTelemetryTarget)
	mux.HandleFunc("/api/module_model", s.handleModuleModel)
	mux.HandleFunc("/login.html", s.handleLoginPage)
	mux.HandleFunc("/logout.html", s.handleLogoutPage)
	mux.HandleFunc("/api/ws", s.handleAPIWebSocket)
	mux.HandleFunc("/api/console/ws", s.handleNativeConsoleWebSocket)
	mux.HandleFunc("/api/", s.handleAPINotFound)
	mux.HandleFunc("/cgi-bin/", s.handleLegacyCGINotFound)
	mux.HandleFunc("/console", s.handleNativeConsole)
	mux.HandleFunc("/console/", s.handleNativeConsole)
	mux.Handle("/", http.FileServer(http.Dir(s.cfg.staticDir)))
	return s.sessionAuth(mux)
}

func (s *simpleAdminServer) nativeAPIHandlers() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"/api/telemetry":        s.handleTelemetry,
		"/api/telemetry/target": s.handleTelemetryTarget,
		"/api/get_atcache":      s.handleGetATCache,
		"/api/get_atcommand":    s.handleGetATCommand,
		"/api/user_atcommand":   s.handleGetATCommand,
		"/api/dashboard_data":   s.handleDashboardData,
		"/api/mock_at":          s.handleMockATPayload,
		"/api/module_model":     s.handleModuleModel,
		"/api/device_info_data": s.handleDeviceInfoData,
		"/api/network_data":     s.handleNetworkData,
		"/api/settings_data":    s.handleSettingsData,
		"/api/sms_data":         s.handleSMSData,
		"/api/get_ping":         s.handleGetPing,
		"/api/get_sms":          s.handleGetSMS,
		"/api/get_ttl_status":   s.handleGetTTLStatus,
		"/api/get_uptime":       s.handleGetUptime,
		"/api/get_language":     s.handleGetLanguage,
		"/api/send_sms":         s.handleSendSMS,
		"/api/set_ttl":          s.handleSetTTL,
		"/api/set_language":     s.handleSetLanguage,
		"/api/set_password":     s.handleSetPassword,
	}
}

func (s *simpleAdminServer) legacyCGIAliases() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"/cgi-bin/get_atcache":      s.handleGetATCache,
		"/cgi-bin/get_atcommand":    s.handleGetATCommand,
		"/cgi-bin/user_atcommand":   s.handleGetATCommand,
		"/cgi-bin/dashboard_data":   s.handleDashboardData,
		"/cgi-bin/mock_at":          s.handleMockATPayload,
		"/cgi-bin/device_info_data": s.handleDeviceInfoData,
		"/cgi-bin/network_data":     s.handleNetworkData,
		"/cgi-bin/settings_data":    s.handleSettingsData,
		"/cgi-bin/sms_data":         s.handleSMSData,
		"/cgi-bin/get_ping":         s.handleGetPing,
		"/cgi-bin/get_sms":          s.handleGetSMS,
		"/cgi-bin/get_ttl_status":   s.handleGetTTLStatus,
		"/cgi-bin/get_uptime":       s.handleGetUptime,
		"/cgi-bin/get_language":     s.handleGetLanguage,
		"/cgi-bin/send_sms":         s.handleSendSMS,
		"/cgi-bin/set_ttl":          s.handleSetTTL,
		"/cgi-bin/set_language":     s.handleSetLanguage,
		"/cgi-bin/set_password":     s.handleSetPassword,
	}
}

func (s *simpleAdminServer) registerNativeAPIRoutes(mux *http.ServeMux) {
	for path, handler := range s.nativeAPIHandlers() {
		mux.HandleFunc(path, handler)
	}
	mux.HandleFunc("/api/", s.handleAPINotFound)
}

func (s *simpleAdminServer) registerLegacyCGIAliases(mux *http.ServeMux) {
	for path, handler := range s.legacyCGIAliases() {
		mux.HandleFunc(path, handler)
	}
	mux.HandleFunc("/cgi-bin/", s.handleLegacyCGINotFound)
}

func (s *simpleAdminServer) handleAPINotFound(w http.ResponseWriter, r *http.Request) {
	writeText(w, http.StatusNotFound, "unsupported api endpoint: "+r.URL.Path)
}

func (s *simpleAdminServer) handleLegacyCGINotFound(w http.ResponseWriter, r *http.Request) {
	writeText(w, http.StatusNotFound, "unsupported legacy cgi-bin endpoint: "+r.URL.Path)
}

const (
	sessionCookieName = "zbims_session"
	sessionDuration   = 24 * time.Hour
)

func (s *simpleAdminServer) sessionAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicAuthPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if s.isRequestAuthenticated(r) {
			next.ServeHTTP(w, r)
			return
		}
		s.writeAuthRequired(w, r)
	})
}

func isPublicAuthPath(path string) bool {
	switch path {
	case "/login.html", "/logout.html", "/api/login", "/api/logout", "/api/module_model":
		return true
	default:
		return false
	}
}

func (s *simpleAdminServer) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeText(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.isRequestAuthenticated(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	http.ServeFile(w, r, filepath.Join(s.cfg.staticDir, "login.html"))
}

func (s *simpleAdminServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid form"})
		return
	}
	s.authMu.Lock()
	defer s.authMu.Unlock()
	auth, err := loadAuthConfig(s.cfg.authFile)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "auth config error"})
		return
	}
	username := r.FormValue("username")
	password := r.FormValue("password")
	if !constantTimeEqual(username, auth.Username) || !constantTimeEqual(password, auth.Password) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "invalid credentials"})
		return
	}
	expiresAt := time.Now().Add(sessionDuration)
	token, err := s.createSession(expiresAt)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "session error"})
		return
	}
	setSessionCookie(w, r, token, expiresAt)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "redirect": "/"})
}

func (s *simpleAdminServer) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.destroyRequestSession(r)
	clearSessionCookie(w, r)
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	if r.Method == http.MethodGet || strings.Contains(r.Header.Get("Accept"), "text/html") {
		http.Redirect(w, r, "/login.html", http.StatusSeeOther)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "redirect": "/login.html"})
}

func (s *simpleAdminServer) handleLogoutPage(w http.ResponseWriter, r *http.Request) {
	s.destroyRequestSession(r)
	clearSessionCookie(w, r)
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	http.Redirect(w, r, "/login.html", http.StatusSeeOther)
}

func (s *simpleAdminServer) writeAuthRequired(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/cgi-bin/") {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "login required"})
		return
	}
	loginURL := "/login.html"
	if r.URL.Path != "/" || r.URL.RawQuery != "" {
		loginURL += "?next=" + url.QueryEscape(r.URL.RequestURI())
	}
	http.Redirect(w, r, loginURL, http.StatusSeeOther)
}

func (s *simpleAdminServer) isRequestAuthenticated(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	return s.validateSession(cookie.Value)
}

func (s *simpleAdminServer) createSession(expiresAt time.Time) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)
	s.sessionMu.Lock()
	s.pruneSessionsLocked(time.Now())
	if s.sessions == nil {
		s.sessions = make(map[string]time.Time)
	}
	if len(s.sessions) >= maxSessions {
		var oldestToken string
		var oldestExpiry time.Time
		for existing, expiry := range s.sessions {
			if oldestToken == "" || expiry.Before(oldestExpiry) {
				oldestToken, oldestExpiry = existing, expiry
			}
		}
		s.revokeSessionLocked(oldestToken, nil)
	}
	s.sessions[token] = expiresAt
	s.sessionMu.Unlock()
	return token, nil
}

func (s *simpleAdminServer) validateSession(token string) bool {
	now := time.Now()
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	if s.sessions == nil {
		return false
	}
	expiresAt, ok := s.sessions[token]
	if !ok {
		return false
	}
	if !expiresAt.After(now) {
		s.revokeSessionLocked(token, nil)
		return false
	}
	s.sessions[token] = now.Add(sessionDuration)
	return true
}

func (s *simpleAdminServer) destroyRequestSession(r *http.Request) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return
	}
	s.sessionMu.Lock()
	s.revokeSessionLocked(cookie.Value, nil)
	s.sessionMu.Unlock()
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int(time.Until(expiresAt).Seconds()),
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
}

func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func validateStaticDir(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	indexPath := filepath.Join(path, "index.html")
	if _, err := os.Stat(indexPath); err != nil {
		return fmt.Errorf("index.html missing in %s: %w", path, err)
	}
	return nil
}

func ensureAuthFile(path string) error {
	info, err := os.Stat(path)
	if err == nil && info.Size() > 0 {
		return nil
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return writePersistentFile(path, []byte("admin:admin\n"), 0600)
}

func loadAuthConfig(path string) (authConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return authConfig{}, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 || parts[0] == "" {
			return authConfig{}, fmt.Errorf("认证文件格式错误: %s", path)
		}
		return authConfig{Username: parts[0], Password: parts[1]}, nil
	}
	return authConfig{}, fmt.Errorf("认证文件为空: %s", path)
}

func writeAuthConfig(path string, auth authConfig) error {
	if strings.TrimSpace(auth.Username) == "" {
		return errors.New("username is empty")
	}
	if err := validateNewPassword(auth.Password); err != nil {
		return err
	}
	return writePersistentFile(path, []byte(fmt.Sprintf("%s:%s\n", auth.Username, auth.Password)), 0600)
}

func validateNewPassword(password string) error {
	if password == "" {
		return errors.New("new password is empty")
	}
	if strings.ContainsAny(password, "\r\n") {
		return errors.New("new password must not contain line breaks")
	}
	if len(password) > 128 {
		return errors.New("new password is too long")
	}
	return nil
}

func ensureManagedTLSCertificate(certPath, keyPath, caCertPath, caKeyPath string) error {
	caCert, caKey, err := ensureLocalCACertificate(caCertPath, caKeyPath)
	if err != nil {
		return err
	}
	if serverCertificateMatches(certPath, keyPath, caCert) {
		return nil
	}
	return writeServerCertificate(certPath, keyPath, caCert, caKey)
}

func ensureLocalCACertificate(certPath, keyPath string) (*x509.Certificate, *rsa.PrivateKey, error) {
	if cert, key, err := loadLocalCACertificate(certPath, keyPath); err == nil {
		return cert, key, nil
	}

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	serialNumber, err := randomSerialNumber()
	if err != nil {
		return nil, nil, err
	}

	tmpl := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"ZBIMS Local CA"},
			CommonName:   "ZBIMS Local Root CA",
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(20, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, nil, err
	}
	if err := writeCertificatePair(certPath, keyPath, certDER, privateKey); err != nil {
		return nil, nil, err
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, nil, err
	}
	return cert, privateKey, nil
}

func loadLocalCACertificate(certPath, keyPath string) (*x509.Certificate, *rsa.PrivateKey, error) {
	cert, err := loadSingleCertificate(certPath)
	if err != nil {
		return nil, nil, err
	}
	key, err := loadRSAPrivateKey(keyPath)
	if err != nil {
		return nil, nil, err
	}
	if !cert.IsCA || time.Now().After(cert.NotAfter.AddDate(0, 0, -30)) {
		return nil, nil, errors.New("local CA certificate is invalid or expiring")
	}
	return cert, key, nil
}

func serverCertificateMatches(certPath, keyPath string, caCert *x509.Certificate) bool {
	cert, err := loadSingleCertificate(certPath)
	if err != nil {
		return false
	}
	if _, err := loadRSAPrivateKey(keyPath); err != nil {
		return false
	}
	if cert.IsCA || time.Now().After(cert.NotAfter.AddDate(0, 0, -30)) {
		return false
	}
	if err := cert.CheckSignatureFrom(caCert); err != nil {
		return false
	}
	if !hasExtKeyUsage(cert, x509.ExtKeyUsageServerAuth) {
		return false
	}
	for _, ip := range requiredTLSCertificateIPs() {
		if !certificateHasIP(cert, ip) {
			return false
		}
	}
	return true
}

func writeServerCertificate(certPath, keyPath string, caCert *x509.Certificate, caKey *rsa.PrivateKey) error {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	serialNumber, err := randomSerialNumber()
	if err != nil {
		return err
	}

	tmpl := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"ZBIMS"},
			CommonName:   "ZBIMS Local HTTPS",
		},
		DNSNames:              requiredTLSCertificateDNSNames(),
		IPAddresses:           requiredTLSCertificateIPs(),
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &tmpl, caCert, &privateKey.PublicKey, caKey)
	if err != nil {
		return err
	}
	return writeCertificatePair(certPath, keyPath, certDER, privateKey)
}

func requiredTLSCertificateDNSNames() []string {
	return []string{"localhost", "zbims", "zbims.local"}
}

func requiredTLSCertificateIPs() []net.IP {
	seen := map[string]bool{}
	ips := make([]net.IP, 0, 8)
	addIP := func(value string) {
		ip := net.ParseIP(value)
		if ip == nil {
			return
		}
		key := ip.String()
		if seen[key] {
			return
		}
		seen[key] = true
		ips = append(ips, ip)
	}

	addIP("127.0.0.1")
	addIP("::1")
	addIP("192.168.225.1")

	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil {
				continue
			}
			addIP(ip.String())
		}
	}
	return ips
}

func randomSerialNumber() (*big.Int, error) {
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, serialLimit)
}

func hasExtKeyUsage(cert *x509.Certificate, usage x509.ExtKeyUsage) bool {
	for _, item := range cert.ExtKeyUsage {
		if item == usage {
			return true
		}
	}
	return false
}

func certificateHasIP(cert *x509.Certificate, want net.IP) bool {
	for _, got := range cert.IPAddresses {
		if got.Equal(want) {
			return true
		}
	}
	return false
}

func loadSingleCertificate(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("missing certificate PEM block")
	}
	return x509.ParseCertificate(block.Bytes)
}

func loadRSAPrivateKey(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "RSA PRIVATE KEY" {
		return nil, errors.New("missing RSA private key PEM block")
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

func writeCertificatePair(certPath, keyPath string, certDER []byte, privateKey *rsa.PrivateKey) error {
	keyBytes := x509.MarshalPKCS1PrivateKey(privateKey)
	return persistentSettings.write(
		persistentFile{certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}), 0644},
		persistentFile{keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: keyBytes}), 0600},
	)
}

func (s *simpleAdminServer) languageConfigPath() string {
	return filepath.Join(s.cfg.staticDir, languageConfigRelPath)
}

func normalizeLanguage(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "zh", "zh-cn", "cn", "chinese":
		return "zh-CN"
	case "en", "en-us", "english":
		return "en"
	default:
		return ""
	}
}

func readLanguageConfig(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return defaultLanguage, err
	}

	var cfg languageConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return defaultLanguage, err
	}
	if language := normalizeLanguage(cfg.Language); language != "" {
		return language, nil
	}
	return defaultLanguage, nil
}

func writeLanguageConfig(path, language string) error {
	language = normalizeLanguage(language)
	if language == "" {
		return fmt.Errorf("unsupported language: %s", language)
	}
	data, err := json.MarshalIndent(languageConfig{Language: language}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writePersistentFile(path, data, 0644)
}

func (s *simpleAdminServer) handleGetATCommand(w http.ResponseWriter, r *http.Request) {
	s.handleGetATCache(w, r)
}

func (s *simpleAdminServer) handleGetPing(w http.ResponseWriter, r *http.Request) {
	if s.cfg.mockMode {
		writeText(w, http.StatusOK, "OK")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if nativePing(ctx) {
		writeText(w, http.StatusOK, "OK")
		return
	}
	writeText(w, http.StatusOK, "ERROR")
}

func (s *simpleAdminServer) handleGetSMS(w http.ResponseWriter, r *http.Request) {
	atCommandCache.Start(s.cfg.mockMode)
	writeText(w, http.StatusOK, atCommandCache.Fetch(smsListATCommand(), boolQuery(r, "force", false), nil))
}

func (s *simpleAdminServer) handleSendSMS(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	phoneNumber := sanitizeATCommand(q.Get("number"))
	message := strings.TrimSpace(q.Get("msg"))
	commandMeta := sanitizeSMSCommandMeta(q.Get("Command"))
	if phoneNumber == "" || message == "" {
		writeText(w, http.StatusOK, "missing number or msg")
		return
	}
	_ = commandMeta
	result := s.sendSMSBusiness(phoneNumber, message)
	if ok, _ := result["ok"].(bool); !ok {
		writeText(w, http.StatusBadRequest, fmt.Sprint(result["error"]))
		return
	}
	writeText(w, http.StatusOK, fmt.Sprintf("OK segments=%v number=%v", result["segments"], result["number"]))
}

func (s *simpleAdminServer) handleGetLanguage(w http.ResponseWriter, r *http.Request) {
	language, err := readLanguageConfig(s.languageConfigPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("读取语言配置失败: %v", err)
	}
	writeJSON(w, http.StatusOK, languageConfig{Language: language})
}

func (s *simpleAdminServer) handleSetLanguage(w http.ResponseWriter, r *http.Request) {
	language := normalizeLanguage(r.URL.Query().Get("language"))
	if language == "" && r.Method == http.MethodPost {
		var cfg languageConfig
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&cfg); err == nil {
			language = normalizeLanguage(cfg.Language)
		}
	}
	if language == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported language"})
		return
	}
	if err := writeLanguageConfig(s.languageConfigPath(), language); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, languageConfig{Language: language})
}

func (s *simpleAdminServer) handleSetPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	if r.Header.Get("Origin") != "" && !apiWebSocketOriginAllowed(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "origin forbidden"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid form"})
		return
	}
	s.authMu.Lock()
	defer s.authMu.Unlock()
	auth, err := loadAuthConfig(s.cfg.authFile)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "auth config error"})
		return
	}

	currentPassword := requestValue(r, "current_password")
	if currentPassword == "" {
		currentPassword = requestValue(r, "currentPassword")
	}
	newPassword := requestValue(r, "new_password")
	if newPassword == "" {
		newPassword = requestValue(r, "newPassword")
	}
	confirmPassword := requestValue(r, "confirm_password")
	if confirmPassword == "" {
		confirmPassword = requestValue(r, "confirmPassword")
	}

	if !constantTimeEqual(currentPassword, auth.Password) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "current password incorrect"})
		return
	}
	if confirmPassword != "" && newPassword != confirmPassword {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "password confirmation mismatch"})
		return
	}
	if err := validateNewPassword(newPassword); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := writeAuthConfig(s.cfg.authFile, authConfig{Username: auth.Username, Password: newPassword}); err != nil {
		if persistenceCommitted(err) {
			current, _ := r.Context().Value(apiConnectionContextKey{}).(net.Conn)
			s.revokeAllSessions(current)
			clearSessionCookie(w, r)
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Keep the requesting WebSocket alive only long enough to deliver this response.
	current, _ := r.Context().Value(apiConnectionContextKey{}).(net.Conn)
	s.revokeAllSessions(current)
	clearSessionCookie(w, r)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "redirect": "/login.html"})
}

func (s *simpleAdminServer) handleGetTTLStatus(w http.ResponseWriter, r *http.Request) {
	value, enabled := currentTTLValue()
	writeJSON(w, http.StatusOK, map[string]any{"isEnabled": enabled, "ttl": value})
}

func (s *simpleAdminServer) handleSetTTL(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("ttlvalue")
	ttl, err := strconv.Atoi(stripNonDigits(raw))
	if err != nil || ttl < 0 || ttl > 255 {
		writeJSON(w, http.StatusOK, map[string]any{"debug_logs": []string{"invalid ttlvalue"}})
		return
	}

	logs := []string{fmt.Sprintf("Received parameter: ttlvalue=%d", ttl)}
	logs = append(logs, setNativeTTL(ttl)...)
	writeJSON(w, http.StatusOK, map[string]any{"debug_logs": logs})
}

func (s *simpleAdminServer) handleGetUptime(w http.ResponseWriter, r *http.Request) {
	if s.cfg.mockMode {
		writeText(w, http.StatusOK, mockUptimeText())
		return
	}
	writeText(w, http.StatusOK, nativeUptimeText())
}

func currentTTLValue() (int, bool) {
	v, err := readTTLValue()
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}

func nativePing(ctx context.Context) bool {
	for _, address := range []string{"223.5.5.5:53", "1.1.1.1:53", "8.8.8.8:53"} {
		d := net.Dialer{Timeout: 1500 * time.Millisecond}
		conn, err := d.DialContext(ctx, "tcp", address)
		if err == nil {
			_ = conn.Close()
			return true
		}
	}
	return false
}

func nativeUptimeText() string {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return "up unknown"
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return "up unknown"
	}
	secondsFloat, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return "up unknown"
	}
	totalMinutes := int(secondsFloat) / 60
	days := totalMinutes / (24 * 60)
	hours := (totalMinutes % (24 * 60)) / 60
	minutes := totalMinutes % 60
	return fmt.Sprintf("up %d day, %d hour, %d min", days, hours, minutes)
}

func envBool(name string) bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv(name)))
	switch value {
	case "1", "true", "yes", "on", "debug":
		return true
	default:
		return false
	}
}

func logATDebug(format string, args ...any) {
	if !atDebugEnabled {
		return
	}
	log.Printf("[AT-DEBUG] "+format, args...)
}

func atPayloadSummary(payload string) string {
	clean := strings.ReplaceAll(payload, "\r", "\\r")
	clean = strings.ReplaceAll(clean, "\n", "\\n")
	clean = strings.ReplaceAll(clean, "\x1a", "<CTRL-Z>")
	if len(clean) > 160 {
		clean = clean[:160] + "..."
	}
	return fmt.Sprintf("len=%d data=%q", len(payload), clean)
}

func outputSummary(output string) string {
	clean := strings.ReplaceAll(output, "\r", "\\r")
	clean = strings.ReplaceAll(clean, "\n", "\\n")
	if len(clean) > 240 {
		clean = clean[:240] + "..."
	}
	return fmt.Sprintf("len=%d data=%q", len(output), clean)
}

func logATDeviceStat(label string, devices []string) {
	if !atDebugEnabled {
		return
	}
	for _, device := range devices {
		info, err := os.Lstat(device)
		if err != nil {
			logATDebug("%s %s: stat error: %v", label, device, err)
			continue
		}
		target := ""
		if info.Mode()&os.ModeSymlink != 0 {
			if link, linkErr := os.Readlink(device); linkErr == nil {
				target = " -> " + link
			}
		}
		logATDebug("%s %s%s: mode=%s size=%d", label, device, target, info.Mode().String(), info.Size())
	}
}

func runATCLICommand(args []string) {
	fs := flag.NewFlagSet("at", flag.ExitOnError)
	devicesFlag := fs.String("devices", "", "AT device candidates, separated by comma or space")
	devicesFile := fs.String("devices-file", defaultATDevicesFile, "AT device candidates file")
	timeoutMS := fs.Int("timeout-ms", 1000, "AT command timeout in milliseconds")
	debugFlag := fs.Bool("debug", envBool("SIMPLEADMIN_AT_DEBUG"), "print detailed AT debug logs")
	_ = fs.Parse(args)

	command := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if command == "" {
		fmt.Fprintln(os.Stderr, "usage: simpleadmin-httpd at [--devices /dev/smd11] [--timeout-ms 1000] ATI")
		os.Exit(2)
	}

	atDebugEnabled = *debugFlag
	runtimeATDevices = collectATDeviceCandidates(*devicesFlag, *devicesFile)
	log.Printf("AT CLI debug logging: %v", atDebugEnabled)
	log.Printf("AT CLI candidates: %s", strings.Join(runtimeATDevices, ", "))
	logATDeviceStat("cli candidate", runtimeATDevices)
	if len(runtimeATDevices) == 0 {
		runtimeATDevices = defaultATDeviceCandidates()
	}

	out, err := runATCommandUntilDone(command, 200, *timeoutMS)
	if strings.TrimSpace(out) != "" {
		fmt.Print(out)
		if !strings.HasSuffix(out, "\n") {
			fmt.Println()
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "AT command failed: %v\n", err)
		os.Exit(1)
	}
}

func atCommandTimeoutMS(command string) int {
	return atGroupedCommandTimeoutMS(command)
}

func atGroupedCommandTimeoutMS(command string) int {
	parts := splitATCommandParts(sanitizeATCommand(command))
	if len(parts) <= 1 {
		return atSingleCommandTimeoutMS(command)
	}
	total := 0
	for _, part := range normalizeATCommandParts(parts) {
		total += atSingleCommandTimeoutMS(part)
	}
	if total <= 0 {
		return atSingleCommandTimeoutMS(command)
	}
	return total
}

func atSingleCommandTimeoutMS(command string) int {
	upper := strings.ToUpper(strings.TrimSpace(command))
	switch {
	case upper == `AT+QMAP="MPDN_RULE",0`:
		return 10000
	case strings.Contains(upper, "QSCAN"):
		return 120000
	default:
		return 1000
	}
}

func runATCommandUntilDone(command string, startTimeoutMS, maxTimeoutMS int) (string, error) {
	_ = startTimeoutMS
	command = sanitizeATCommand(command)
	if command == "" {
		return "", nil
	}
	logATDebug("run AT command: command=%q timeout_ms=%d", command, maxTimeoutMS)
	return runATPayload(command+"\r\n", maxTimeoutMS)
}

func runATCommand(command string, timeoutMS int) (string, error) {
	command = sanitizeATCommand(command)
	if command == "" {
		return "", nil
	}
	return runATPayload(command+"\r\n", timeoutMS)
}

// AT transactions are serialized by a global file lock and a per-device lock.
// The module AT port is accessed directly through /dev/smd11; the runtime does
// not create or rebuild socat, ttyIN, ttyOUT, or cat bridge processes.
var atDeviceLockRegistry = struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}{locks: make(map[string]*sync.Mutex)}

func lockForATDevice(device string) *sync.Mutex {
	atDeviceLockRegistry.mu.Lock()
	defer atDeviceLockRegistry.mu.Unlock()
	if atDeviceLockRegistry.locks == nil {
		atDeviceLockRegistry.locks = make(map[string]*sync.Mutex)
	}
	mu := atDeviceLockRegistry.locks[device]
	if mu == nil {
		mu = &sync.Mutex{}
		atDeviceLockRegistry.locks[device] = mu
	}
	return mu
}

func runATPayload(payload string, timeoutMS int) (string, error) {
	return runATPayloadWaitFor(payload, time.Duration(timeoutMS)*time.Millisecond, "OK", "ERROR")
}

func runATPayloadWaitFor(payload string, timeout time.Duration, terminalTokens ...string) (string, error) {
	unlock, err := lockGlobalATFile()
	if err != nil {
		return "", err
	}
	defer unlock()

	if timeout < 300*time.Millisecond {
		timeout = 300 * time.Millisecond
	}
	return runATPayloadAcrossCandidates(payload, timeout, terminalTokens...)
}

func runATPayloadAcrossCandidates(payload string, timeout time.Duration, terminalTokens ...string) (string, error) {
	devices := atDeviceCandidates()
	logATDebug("AT payload start: %s timeout=%s tokens=%v candidates=%s", atPayloadSummary(payload), timeout, terminalTokens, strings.Join(devices, ","))
	logATDeviceStat("payload candidate", devices)
	if len(devices) == 0 {
		return "", errors.New("no AT device candidates configured")
	}
	if existing := existingATDeviceCandidates(devices); len(existing) > 0 {
		logATDebug("existing AT candidates: %s", strings.Join(existing, ","))
		devices = existing
	} else {
		logATDebug("no configured AT candidate currently exists; trying full candidate list")
	}

	out, err, details, busyOnSMD, retryable := tryATPayloadOnce(devices, payload, timeout, terminalTokens...)
	if err == nil {
		return out, nil
	}

	if busyOnSMD {
		recoverBusyATDevices()
		out, err, retryDetails, _, _ := tryATPayloadOnce(devices, payload, timeout, terminalTokens...)
		if err == nil {
			return out, nil
		}
		return out, fmt.Errorf("all AT device candidates failed after smd recovery retry: %s", strings.Join(retryDetails, "; "))
	}

	if retryable {
		time.Sleep(250 * time.Millisecond)
		out, err, retryDetails, _, _ := tryATPayloadOnce(devices, payload, timeout, terminalTokens...)
		if err == nil {
			return out, nil
		}
		return out, fmt.Errorf("all AT device candidates failed after retry: %s", strings.Join(retryDetails, "; "))
	}

	return out, fmt.Errorf("all AT device candidates failed: %s", strings.Join(details, "; "))
}

func existingATDeviceCandidates(devices []string) []string {
	out := make([]string, 0, len(devices))
	for _, device := range devices {
		if _, err := os.Stat(device); err == nil {
			out = append(out, device)
		}
	}
	return out
}

func tryATPayloadOnce(devices []string, payload string, timeout time.Duration, terminalTokens ...string) (string, error, []string, bool, bool) {
	details := make([]string, 0, len(devices))
	busyOnSMD := false
	retryable := false
	lastOutput := ""

	for _, device := range devices {
		logATDebug("try AT device: %s", device)
		deviceMu := lockForATDevice(device)
		deviceMu.Lock()
		start := time.Now()
		out, err := nativeATWriteRead(device, payload, timeout, terminalTokens...)
		elapsed := time.Since(start)
		deviceMu.Unlock()
		lastOutput = out
		if err != nil {
			logATDebug("device %s failed after %s: err=%v output=%s", device, elapsed, err, outputSummary(out))
		} else {
			logATDebug("device %s success after %s: output=%s", device, elapsed, outputSummary(out))
		}

		if err == nil {
			return out, nil, details, busyOnSMD, retryable
		}

		details = append(details, fmt.Sprintf("%s: %v", device, err))
		if shouldRecoverBusyATDevice(device, err) {
			busyOnSMD = true
		}
		if !shouldTryNextATDevice(err) {
			return out, err, details, busyOnSMD, retryable
		}
		retryable = true
	}
	return lastOutput, errors.New("all AT device candidates failed"), details, busyOnSMD, retryable
}

func nativeATWriteRead(device, payload string, timeout time.Duration, terminalTokens ...string) (string, error) {
	logATDebug("nativeATWriteRead open: device=%s timeout=%s tokens=%v payload=%s", device, timeout, terminalTokens, atPayloadSummary(payload))
	session, err := openATCommandSession(device)
	if err != nil {
		return "", err
	}
	defer func() {
		if err := closeATSession(session); err != nil {
			logATDebug("close AT session %s error: %v", device, err)
		}
	}()

	logATDebug("drain AT device %s before write", device)
	drainATSession(session, 150*time.Millisecond)
	logATDebug("write AT payload to %s: %s", device, atPayloadSummary(payload))
	if err := writeATSessionString(session, payload); err != nil {
		return "", fmt.Errorf("write failed: %w", err)
	}

	logATDebug("read AT response from %s", device)
	out, err := readATSessionUntilTokens(session, timeout, terminalTokens...)
	logATDebug("read AT response done from %s: err=%v output=%s", device, err, outputSummary(out))
	if err != nil {
		return out, fmt.Errorf("read failed: %w", err)
	}
	if containsAnyToken(out, terminalTokens...) {
		return out, nil
	}
	if strings.TrimSpace(out) != "" {
		return out, fmt.Errorf("timeout waiting for %s", strings.Join(terminalTokens, "/"))
	}
	return "", fmt.Errorf("timeout waiting for %s", strings.Join(terminalTokens, "/"))
}

func shouldTryNextATDevice(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ENODEV) ||
		errors.Is(err, syscall.ENXIO) || errors.Is(err, syscall.EIO) || errors.Is(err, syscall.EACCES) ||
		errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EBUSY) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such file or directory") ||
		strings.Contains(msg, "no such device") ||
		strings.Contains(msg, "input/output error") ||
		strings.Contains(msg, "permission denied") ||
		strings.Contains(msg, "operation not permitted") ||
		strings.Contains(msg, "device or resource busy") ||
		strings.Contains(msg, "timed out") ||
		strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "eof")
}

func runSMSTransaction(sendCmd, message string) (string, error) {
	unlock, err := lockGlobalATFile()
	if err != nil {
		return "", err
	}
	defer unlock()

	devices := atDeviceCandidates()
	if existing := existingATDeviceCandidates(devices); len(existing) > 0 {
		devices = existing
	}

	out, err, details, busyOnSMD, retryable := trySMSTransactionOnce(devices, sendCmd, message)
	if err == nil {
		return out, nil
	}
	if busyOnSMD {
		recoverBusyATDevices()
		out, err, retryDetails, _, _ := trySMSTransactionOnce(devices, sendCmd, message)
		if err == nil {
			return out, nil
		}
		return out, fmt.Errorf("SMS AT transaction failed after smd recovery retry: %s", strings.Join(retryDetails, "; "))
	}
	if retryable {
		time.Sleep(250 * time.Millisecond)
		out, err, retryDetails, _, _ := trySMSTransactionOnce(devices, sendCmd, message)
		if err == nil {
			return out, nil
		}
		return out, fmt.Errorf("SMS AT transaction failed after retry: %s", strings.Join(retryDetails, "; "))
	}
	return out, fmt.Errorf("SMS AT transaction failed: %s", strings.Join(details, "; "))
}

func trySMSTransactionOnce(devices []string, sendCmd, message string) (string, error, []string, bool, bool) {
	details := make([]string, 0, len(devices))
	busyOnSMD := false
	retryable := false
	lastOutput := ""

	for _, device := range devices {
		deviceMu := lockForATDevice(device)
		deviceMu.Lock()
		out, err := sendSMSOnDevice(device, sendCmd, message)
		deviceMu.Unlock()
		lastOutput = out

		if err == nil {
			return out, nil, details, busyOnSMD, retryable
		}
		details = append(details, fmt.Sprintf("%s: %v", device, err))
		if shouldRecoverBusyATDevice(device, err) {
			busyOnSMD = true
		}
		if !shouldTryNextATDevice(err) {
			return out, err, details, busyOnSMD, retryable
		}
		retryable = true
	}
	return lastOutput, errors.New("all AT device candidates failed"), details, busyOnSMD, retryable
}

func sendSMSOnDevice(device, sendCmd, message string) (string, error) {
	session, err := openATCommandSession(device)
	if err != nil {
		return "", err
	}
	defer closeATSession(session)

	drainATSession(session, 150*time.Millisecond)
	if err := writeATSessionString(session, sendCmd+"\r"); err != nil {
		return "", fmt.Errorf("write CMGS failed: %w", err)
	}
	prompt, err := readATSessionUntilTokens(session, 10*time.Second, ">", "ERROR")
	if err != nil {
		return prompt, fmt.Errorf("read SMS prompt failed: %w", err)
	}
	if strings.Contains(prompt, "ERROR") {
		return prompt, nil
	}
	if !strings.Contains(prompt, ">") {
		return prompt, fmt.Errorf("timeout waiting for SMS prompt")
	}
	if err := writeATSessionString(session, message+"\x1A"); err != nil {
		return prompt, fmt.Errorf("write SMS body failed: %w", err)
	}
	body, err := readATSessionUntilTokens(session, 60*time.Second, "OK", "ERROR")
	return prompt + body, err
}

func atDeviceCandidates() []string {
	if len(runtimeATDevices) == 0 {
		return defaultATDeviceCandidates()
	}
	return append([]string(nil), runtimeATDevices...)
}

func collectATDeviceCandidates(flagValue, filePath string) []string {
	var envCandidates []string
	var flagCandidates []string
	var fileCandidates []string
	addTokens := func(dst *[]string, value string) {
		for _, token := range strings.FieldsFunc(value, func(r rune) bool {
			return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
		}) {
			token = strings.TrimSpace(token)
			if token == "" || strings.HasPrefix(token, "#") {
				continue
			}
			*dst = append(*dst, token)
		}
	}

	addTokens(&envCandidates, os.Getenv("SIMPLEADMIN_AT_DEVICES"))
	addTokens(&flagCandidates, flagValue)
	if filePath != "" {
		if data, err := os.ReadFile(filePath); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if idx := strings.Index(line, "#"); idx >= 0 {
					line = strings.TrimSpace(line[:idx])
				}
				addTokens(&fileCandidates, line)
			}
		}
	}

	// Precedence is explicit and non-additive:
	// env overrides CLI, CLI overrides file, file overrides defaults.
	// This keeps a field debug command like --devices /dev/smd11 from silently
	// appending unrelated configured candidates.
	candidates := envCandidates
	if len(candidates) == 0 {
		candidates = flagCandidates
	}
	if len(candidates) == 0 {
		candidates = fileCandidates
	}
	if len(candidates) == 0 {
		candidates = defaultATDeviceCandidates()
	}

	result := filterFixedATDeviceCandidates(uniqueDevicePaths(candidates))
	logATDebug("collect AT candidates: env=%q flag=%q file=%q result=%s", os.Getenv("SIMPLEADMIN_AT_DEVICES"), flagValue, filePath, strings.Join(result, ","))
	return result
}

func defaultATDeviceCandidates() []string {
	return []string{
		"/dev/smd11",
	}
}

func filterFixedATDeviceCandidates(paths []string) []string {
	allowed := make(map[string]struct{}, len(defaultATDeviceCandidates()))
	for _, device := range defaultATDeviceCandidates() {
		allowed[device] = struct{}{}
	}

	filtered := make([]string, 0, len(paths))
	for _, path := range paths {
		if _, ok := allowed[path]; ok {
			filtered = append(filtered, path)
		}
	}
	if len(filtered) == 0 {
		return defaultATDeviceCandidates()
	}
	return filtered
}

func uniqueDevicePaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if !strings.HasPrefix(path, "/dev/") {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	return out
}

func containsAnyToken(text string, terminalTokens ...string) bool {
	if len(terminalTokens) == 0 {
		return true
	}
	for _, token := range terminalTokens {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		upperToken := strings.ToUpper(token)
		if upperToken == "OK" || upperToken == "ERROR" {
			if containsATFinalLine(text, upperToken) {
				return true
			}
			continue
		}
		if strings.Contains(text, token) {
			return true
		}
	}
	return false
}

func containsATFinalLine(text string, token string) bool {
	text = strings.ReplaceAll(text, "\r", "\n")
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		upper := strings.ToUpper(line)
		if token == "OK" && upper == "OK" {
			return true
		}
		if token == "ERROR" && (upper == "ERROR" || strings.HasPrefix(upper, "+CME ERROR:") || strings.HasPrefix(upper, "+CMS ERROR:")) {
			return true
		}
	}
	return false
}

func sanitizeATCommand(value string) string {
	value = strings.ReplaceAll(value, "\r", "")
	value = strings.ReplaceAll(value, "\n", "")
	return strings.TrimSpace(value)
}

func normalizeATCommandParts(rawParts []string) []string {
	commands := make([]string, 0, len(rawParts))
	for i, part := range rawParts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		upperPart := strings.ToUpper(part)
		if i > 0 && !strings.HasPrefix(upperPart, "AT") {
			if strings.HasPrefix(part, "+") || strings.HasPrefix(part, "&") {
				part = "AT" + part
			} else {
				part = "AT+" + part
			}
		}
		commands = append(commands, part)
	}
	return commands
}

func splitATCommandParts(command string) []string {
	parts := make([]string, 0, 4)
	var current strings.Builder
	inQuote := false
	for _, r := range command {
		switch r {
		case '"':
			inQuote = !inQuote
			current.WriteRune(r)
		case ';':
			if inQuote {
				current.WriteRune(r)
				continue
			}
			parts = append(parts, current.String())
			current.Reset()
		default:
			current.WriteRune(r)
		}
	}
	parts = append(parts, current.String())
	return parts
}

func sanitizeSMSCommandMeta(value string) string {
	value = strings.TrimSpace(value)
	re := regexp.MustCompile(`[^0-9,]`)
	return re.ReplaceAllString(value, "")
}

func stripNonDigits(value string) string {
	re := regexp.MustCompile(`[^0-9]`)
	return re.ReplaceAllString(value, "")
}

func mockATResponse(command string) string {
	upper := strings.ToUpper(strings.TrimSpace(command))
	switch {
	case upper == "AT":
		return "AT\r\nOK\r\n"
	case upper == "ATI":
		return "ATI\r\nSimpleAdmin Windows mock modem\r\nOK\r\n"
	case upper == "AT+CGMM" || upper == "+CGMM" || upper == "CGMM":
		return command + "\r\nRG520N-EB\r\nOK\r\n"
	case strings.Contains(upper, "+QSIMSTAT") && strings.Contains(upper, "+QENG"):
		return currentMockDashboardATResponse(command)
	case strings.Contains(upper, "QENG"):
		return currentMockQENGATResponse(command)
	case strings.Contains(upper, "QCAINFO"):
		return currentMockQCAInfoATResponse(command)
	case upper == "AT+CIMI" || upper == "+CIMI" || upper == "CIMI":
		return command + "\r\n420031234567890\r\nOK\r\n"
	case strings.Contains(upper, "+QNWPREFCFG"):
		return mockQNWPREFCFGResponse(command)
	case strings.Contains(upper, "CMGL"):
		return mockSMSList()
	default:
		return command + "\r\nOK\r\n"
	}
}

func mockQNWPREFCFGResponse(command string) string {
	lines := []string{command}
	queries := []struct {
		name  string
		value string
	}{
		{"lte_band", "1:3:5:7:8:20:28:32:38:40:41:42:43"},
		{"nsa_nr5g_band", "1:3:5:7:8:20:28:38:40:41:75:76:77:78"},
		{"nr5g_band", "1:3:5:7:8:20:28:38:40:41:75:76:77:78"},
		{"mode_pref", "AUTO"},
		{"nr5g_disable_mode", "0"},
	}
	for _, query := range queries {
		pattern := regexp.MustCompile(`(?i)\+QNWPREFCFG\s*=\s*"` + regexp.QuoteMeta(query.name) + `"\s*(?:;|\r|\n|$)`)
		if pattern.MatchString(command) {
			lines = append(lines, fmt.Sprintf(`+QNWPREFCFG: "%s",%s`, query.name, query.value))
		}
	}
	lines = append(lines, "OK")
	return strings.Join(lines, "\r\n") + "\r\n"
}

func mockSMSList() string {
	pdu := buildMockSMSDeliverPDU("+10000000000", "SimpleAdmin Windows mock SMS")
	return fmt.Sprintf("+CMGL: 1,0,,%d\r\n%s\r\nOK\r\n", len(pdu)/2-1, pdu)
}

func mockSMSSendResponse(phoneNumber string) string {
	return fmt.Sprintf("> \r\n+CMGS: 1\r\nOK\r\n# mock sent to %s\r\n", phoneNumber)
}

func mockUptimeText() string {
	totalMinutes := int(time.Since(processStartTime).Seconds()) / 60
	days := totalMinutes / (24 * 60)
	hours := (totalMinutes % (24 * 60)) / 60
	minutes := totalMinutes % 60
	return fmt.Sprintf("up %d day, %d hour, %d min", days, hours, minutes)
}

func writeText(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
	if !strings.HasSuffix(body, "\n") {
		_, _ = w.Write([]byte("\n"))
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONAndFlush(w http.ResponseWriter, status int, v any) {
	writeJSON(w, status, v)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}
