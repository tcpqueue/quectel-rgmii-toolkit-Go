package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoginLocaleResourceIsPublicButOtherAssetsRemainProtected(t *testing.T) {
	s := securityServer(t)
	if err := os.MkdirAll(filepath.Join(s.cfg.staticDir, "js"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.cfg.staticDir, "js", "locales.js"), []byte("window.localeLoaded=true;"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := securityRequest(s, nil, "GET", "/js/locales.js?v=3.3", ""); got.Code != 200 || got.Body.String() != "window.localeLoaded=true;" {
		t.Fatal(got.Code, got.Body.String())
	}
	for _, path := range []string{"/js/simpleadmin-core.js", "/api/get_language", "/api/telemetry"} {
		if got := securityRequest(s, nil, "GET", path, "").Code; got == 200 {
			t.Fatalf("unauthenticated access to %s", path)
		}
	}
}

func TestSupportedLanguageAliases(t *testing.T) {
	for input, want := range map[string]string{"zh-CN": "zh-CN", "en-US": "en", "ru": "ru", "ru-RU": "ru", "ar": "ar", "ar-SA": "ar", "invalid": ""} {
		if got := normalizeLanguage(input); got != want {
			t.Errorf("%q: got %q, want %q", input, got, want)
		}
	}
}
