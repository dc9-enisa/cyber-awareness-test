package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestSecurityHeaders(t *testing.T) {
	e := newEnv(t)
	r, _ := e.browser().do("GET", "/", nil)
	want := map[string]string{
		"X-Content-Type-Options":       "nosniff",
		"X-Frame-Options":              "DENY",
		"Referrer-Policy":              "same-origin",
		"Cache-Control":                "no-store",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Cross-Origin-Embedder-Policy": "require-corp",
	}
	for k, v := range want {
		if got := r.Header.Get(k); got != v {
			t.Errorf("%s = %q, oczekiwano %q", k, got, v)
		}
	}
	csp := r.Header.Get("Content-Security-Policy")
	for _, d := range []string{"default-src 'none'", "script-src 'self'", "frame-ancestors 'none'", "object-src 'none'", "base-uri 'none'"} {
		if !strings.Contains(csp, d) {
			t.Errorf("CSP bez %q: %s", d, csp)
		}
	}
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP z unsafe-*: %s", csp)
	}
	if !strings.Contains(r.Header.Get("Permissions-Policy"), "camera=()") {
		t.Error("brak Permissions-Policy")
	}
}

func TestNoDirectoryListing(t *testing.T) {
	e := newEnv(t)
	b := e.browser()
	for _, p := range []string{"/static/", "/static/fonts/"} {
		if r, _ := b.do("GET", p, nil); r.StatusCode != 404 {
			t.Errorf("%s: %d, oczekiwano 404", p, r.StatusCode)
		}
	}
	if r, _ := b.do("GET", "/static/web.css", nil); r.StatusCode != 200 {
		t.Errorf("plik statyczny: %d", r.StatusCode)
	}
	if r, _ := b.do("DELETE", "/", nil); r.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("DELETE /: %d", r.StatusCode)
	}
}

func TestLogoutRevokesSessionCookie(t *testing.T) {
	e := newEnv(t)
	b := e.browser()
	b.login(anna)
	u, _ := url.Parse(e.srv.URL)
	stolen := b.c.Jar.Cookies(u) // kopia ciasteczka sesji przed wylogowaniem
	b.do("POST", "/auth/logout", url.Values{"csrf": {b.csrf()}})

	replay := e.browser()
	replay.c.Jar.SetCookies(u, stolen)
	if _, page := replay.do("GET", "/", nil); !strings.Contains(page, "Zaloguj się kontem służbowym") {
		t.Fatal("ciasteczko sesji działa po wylogowaniu")
	}
}

func TestRateLimitLogin(t *testing.T) {
	e := newEnv(t)
	b := e.browser()
	limited := false
	for i := 0; i < 40; i++ {
		if r, _ := b.do("GET", "/auth/login", nil); r.StatusCode == http.StatusTooManyRequests {
			limited = r.Header.Get("Retry-After") != ""
			break
		}
	}
	if !limited {
		t.Fatal("brak limitu prób logowania")
	}
	if r, _ := b.do("GET", "/healthz", nil); r.StatusCode != 200 {
		t.Fatal("/healthz nie może podlegać limitowi")
	}
}

func TestBodyLimit(t *testing.T) {
	e := newEnv(t)
	b := e.browser()
	b.login(anna)
	big := url.Values{"csrf": {b.csrf()}, "x": {strings.Repeat("a", maxBody+1)}}
	if r, _ := b.do("POST", "/start/A", big); r.StatusCode < 400 {
		t.Fatalf("treść ponad limit przyjęta: %d", r.StatusCode)
	}
}

func TestClientIPHeaderOnlyWhenConfigured(t *testing.T) {
	a := &App{cfg: &Config{}}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.2:5000"
	r.Header.Set("CF-Connecting-IP", "203.0.113.7")
	if ip := a.clientIP(r); ip != "10.0.0.2" {
		t.Fatalf("bez konfiguracji nagłówek nie może być zaufany: %s", ip)
	}
	a.cfg.ClientIPHeader = "CF-Connecting-IP"
	if ip := a.clientIP(r); ip != "203.0.113.7" {
		t.Fatalf("nagłówek z konfiguracji: %s", ip)
	}
	r.Header.Set("CF-Connecting-IP", "nie-adres")
	if ip := a.clientIP(r); ip != "10.0.0.2" {
		t.Fatalf("nieprawidłowy nagłówek: %s", ip)
	}
}

func TestDiscoveryRejectsForeignHosts(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/"+tTenant+"/v2.0/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"issuer": srv.URL + "/" + tTenant + "/v2.0", "authorization_endpoint": srv.URL + "/authorize",
			"token_endpoint": srv.URL + "/token", "jwks_uri": "http://169.254.169.254/keys"})
	})
	_, err := NewOIDC(&Config{Authority: srv.URL, TenantID: tTenant, ClientID: tClient})
	if err == nil || !strings.Contains(err.Error(), "spoza") {
		t.Fatalf("jwks_uri spoza hosta Entra przyjęty: %v", err)
	}
}
