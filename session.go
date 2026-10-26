package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

const (
	sessionCookie = "sa_sesja"
	loginCookie   = "sa_logowanie"
	sessionTTL    = 8 * time.Hour
	loginTTL      = 10 * time.Minute
)

// Session – zalogowany uczestnik. Przechowywana w podpisanym ciasteczku (bez stanu po stronie serwera).
type Session struct {
	OID   string `json:"o"`
	Name  string `json:"n"`
	UPN   string `json:"u"`
	Admin bool   `json:"a"`
	CSRF  string `json:"c"`
	Exp   int64  `json:"e"`
}

// loginState – dane jednej próby logowania (state, nonce, PKCE), ważne kilka minut.
type loginState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Exp      int64  `json:"e"`
}

type Signer struct{ key []byte }

// Seal: base64url(JSON) + "." + base64url(HMAC-SHA256(cel|JSON)). Cel rozdziela ciasteczka różnego rodzaju.
func (s Signer) Seal(purpose string, v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	p := base64.RawURLEncoding.EncodeToString(b)
	return p + "." + s.mac(purpose, p), nil
}

func (s Signer) Open(purpose, tok string, v any) error {
	p, sig, ok := strings.Cut(tok, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(s.mac(purpose, p))) {
		return errors.New("nieprawidłowy podpis")
	}
	b, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func (s Signer) mac(purpose, payload string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(purpose + "|" + payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (a *App) setCookie(w http.ResponseWriter, name, value string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", MaxAge: int(ttl.Seconds()),
		HttpOnly: true, Secure: a.cfg.Secure(), SameSite: http.SameSiteLaxMode,
	})
}

func (a *App) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: a.cfg.Secure(), SameSite: http.SameSiteLaxMode})
}

func (a *App) startSession(w http.ResponseWriter, s Session) error {
	s.CSRF = randomToken()
	s.Exp = time.Now().Add(sessionTTL).Unix()
	v, err := a.signer.Seal("sesja", s)
	if err != nil {
		return err
	}
	a.setCookie(w, sessionCookie, v, sessionTTL)
	return nil
}

// session zwraca bieżącą sesję albo nil (brak, zły podpis, wygasła).
func (a *App) session(r *http.Request) *Session {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	var s Session
	if a.signer.Open("sesja", c.Value, &s) != nil || time.Now().Unix() > s.Exp || s.OID == "" {
		return nil
	}
	return &s
}
