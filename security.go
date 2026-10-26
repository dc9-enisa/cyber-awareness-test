package main

// Zabezpieczenia HTTP według OWASP Top 10 (2021) i OWASP Secure Headers:
//   A01 kontrola dostępu – właściciel próby/zaświadczenia, rola raportu (main.go), unieważnianie sesji,
//   A02 kryptografia – ciasteczka __Host-, Secure, HttpOnly, SameSite, podpis HMAC, HSTS,
//   A03 wstrzyknięcia – html/template, formuły CSV (admin.go), ścisłe CSP,
//   A04 projekt – limity żądań, rozmiaru treści i czasu,
//   A05 konfiguracja – nagłówki, brak listingu katalogów, tylko zadeklarowane metody,
//   A07 uwierzytelnianie – OIDC z PKCE/nonce/state (oidc.go), limit prób logowania, wylogowanie unieważnia sesję,
//   A09 rejestrowanie – dziennik zdarzeń bezpieczeństwa (JSON) bez tokenów i treści odpowiedzi,
//   A10 SSRF – połączenia wychodzące tylko do hosta ENTRA_AUTHORITY (oidc.go).

import (
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// secLog – zdarzenia bezpieczeństwa w JSON na stderr (docker logs); pole "zdarzenie" ułatwia filtrowanie.
var secLog = slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("kanal", "bezpieczenstwo")

func (a *App) audit(r *http.Request, event string, attrs ...any) {
	attrs = append([]any{"zdarzenie", event, "ip", a.clientIP(r), "metoda", r.Method, "sciezka", r.URL.Path}, attrs...)
	secLog.Info("audyt", attrs...)
}

// clientIP – adres klienta. Nagłówek CLIENT_IP_HEADER (np. CF-Connecting-IP) jest brany pod uwagę tylko wtedy,
// gdy ustawiono go w konfiguracji – aplikacja musi być wtedy osiągalna wyłącznie przez serwer pośredniczący.
func (a *App) clientIP(r *http.Request) string {
	if h := a.cfg.ClientIPHeader; h != "" {
		if v := strings.TrimSpace(strings.Split(r.Header.Get(h), ",")[0]); net.ParseIP(v) != nil {
			return v
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// cspPolicy – bez skryptów i stylów inline (szablony ich nie używają), zasoby tylko z własnego źródła.
func (a *App) cspPolicy() string {
	p := "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; font-src 'self'; connect-src 'self'; " +
		"form-action 'self' https://login.microsoftonline.com; frame-ancestors 'none'; base-uri 'none'; object-src 'none'"
	if a.cfg.Authority != "https://login.microsoftonline.com" {
		p = strings.Replace(p, "https://login.microsoftonline.com", "https://login.microsoftonline.com "+a.cfg.Authority, 1)
	}
	if a.cfg.Secure() {
		p += "; upgrade-insecure-requests"
	}
	return p
}

const maxBody = 64 << 10 // formularze aplikacji mają kilkaset bajtów

func (a *App) securityHeaders(h http.Handler) http.Handler {
	origin := a.cfg.BaseURL
	csp := a.cspPolicy()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", csp)
		hd.Set("X-Content-Type-Options", "nosniff")
		// same-origin, nie no-referrer: przy no-referrer Chrome wysyła w formularzach „Origin: null”,
		// a sprawdzenie źródła poniżej odrzuciłoby każde własne żądanie POST
		hd.Set("Referrer-Policy", "same-origin")
		hd.Set("Cache-Control", "no-store")
		hd.Set("Pragma", "no-cache")
		hd.Set("X-Frame-Options", "DENY")
		hd.Set("Cross-Origin-Opener-Policy", "same-origin")
		hd.Set("Cross-Origin-Resource-Policy", "same-origin")
		hd.Set("Cross-Origin-Embedder-Policy", "require-corp")
		hd.Set("X-Permitted-Cross-Domain-Policies", "none")
		hd.Set("Permissions-Policy", "accelerometer=(), autoplay=(), camera=(), display-capture=(), encrypted-media=(), "+
			"fullscreen=(), geolocation=(), gyroscope=(), magnetometer=(), microphone=(), midi=(), payment=(), "+
			"picture-in-picture=(), publickey-credentials-get=(), screen-wake-lock=(), sync-xhr=(), usb=(), xr-spatial-tracking=(), "+
			"browsing-topics=(), interest-cohort=()")
		if a.cfg.Secure() {
			hd.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, maxBody)
			// CSRF, warstwa 1: żądania zmieniające stan tylko z własnego źródła (nagłówek Origin, gdy jest).
			if o := r.Header.Get("Origin"); o != "" && o != origin {
				a.audit(r, "csrf_obce_zrodlo", "origin", o)
				http.Error(w, "Niedozwolone źródło żądania", http.StatusForbidden)
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}

// noListing – pliki statyczne bez listingu katalogów (A05).
func noListing(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "" || strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// ---------- limity żądań (A04, A07) ----------

// limiter – kubełek tokenów na klucz (adres IP lub użytkownik); nieużywane klucze usuwane co minutę.
type limiter struct {
	mu    sync.Mutex
	rate  float64 // tokeny na sekundę
	burst float64
	keys  map[string]*bucket
	last  time.Time
}

type bucket struct {
	tokens float64
	seen   time.Time
}

func newLimiter(perMinute, burst int) *limiter {
	return &limiter{rate: float64(perMinute) / 60, burst: float64(burst), keys: map[string]*bucket{}, last: time.Now()}
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if now.Sub(l.last) > time.Minute {
		for k, b := range l.keys {
			if now.Sub(b.seen) > 10*time.Minute {
				delete(l.keys, k)
			}
		}
		l.last = now
	}
	b := l.keys[key]
	if b == nil {
		b = &bucket{tokens: l.burst, seen: now}
		l.keys[key] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.seen).Seconds()*l.rate)
	b.seen = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

type limits struct {
	login *limiter // /auth/* na adres IP
	all   *limiter // wszystkie żądania na adres IP
}

func newLimits() *limits {
	return &limits{login: newLimiter(20, 10), all: newLimiter(600, 120)}
}

func (a *App) rateLimit(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			h.ServeHTTP(w, r)
			return
		}
		ip := a.clientIP(r)
		lim, kind := a.limits.all, "ogolny"
		if strings.HasPrefix(r.URL.Path, "/auth/") {
			lim, kind = a.limits.login, "logowanie"
		}
		if !lim.allow(ip) {
			a.audit(r, "limit_zadan", "limit", kind)
			w.Header().Set("Retry-After", "60")
			http.Error(w, "Zbyt wiele żądań. Spróbuj ponownie za minutę.", http.StatusTooManyRequests)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// ---------- unieważnianie sesji (A07) ----------

// Revoked – identyfikatory sesji wylogowanych przed upływem ważności. Wpis wygasa razem z sesją (najwyżej 8 h),
// więc lista pozostaje mała. Po restarcie serwera lista jest pusta – stąd krótki czas życia sesji.
type Revoked struct {
	mu  sync.Mutex
	ids map[string]int64 // id → exp
}

func (v *Revoked) Add(id string, exp int64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	now := time.Now().Unix()
	for k, e := range v.ids {
		if e < now {
			delete(v.ids, k)
		}
	}
	v.ids[id] = exp
}

func (v *Revoked) Has(id string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	_, ok := v.ids[id]
	return ok
}
