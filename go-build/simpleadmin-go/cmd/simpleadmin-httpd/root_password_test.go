package main

import "testing"

func TestRootPasswordAPI(t *testing.T) {
	s := securityServer(t)
	cookie := securitySession(t, s)
	if w := securityRequest(s, nil, "POST", "/api/set_root_password", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := securityRequest(s, cookie, "GET", "/api/set_root_password", ""); w.Code != 405 {
		t.Fatal(w.Code)
	}
	if w := securityRequest(s, cookie, "POST", "/api/set_root_password", "current_password=wrong&new_password=changed123&confirm_password=changed123"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := securityRequest(s, cookie, "POST", "/api/set_root_password", "current_password=admin&new_password=changed123&confirm_password=other"); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := securityRequest(s, cookie, "POST", "/api/set_root_password", "current_password=admin&new_password=changed123&confirm_password=changed123"); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if s.validateSession(cookie.Value) || s.rootPasswordMatches("admin") || !s.rootPasswordMatches("changed123") {
		t.Fatal("old root password or web session remained valid")
	}
	auth, err := loadAuthConfig(s.cfg.authFile)
	if err != nil || auth.Password != "admin" {
		t.Fatal("root password changed web credentials")
	}
}
