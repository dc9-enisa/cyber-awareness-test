package main

// Logowanie Microsoft Entra ID (OpenID Connect, przepływ kodu autoryzacji z PKCE, klient poufny).
// Tylko biblioteka standardowa: discovery, klucze JWKS, weryfikacja podpisu RS256 i oświadczeń id_token.

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const clockSkew = 2 * time.Minute

type discovery struct {
	Issuer        string `json:"issuer"`
	AuthEndpoint  string `json:"authorization_endpoint"`
	TokenEndpoint string `json:"token_endpoint"`
	JWKSURI       string `json:"jwks_uri"`
	EndSession    string `json:"end_session_endpoint"`
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// Claims – oświadczenia id_token używane przez aplikację.
type Claims struct {
	Iss               string   `json:"iss"`
	Aud               audience `json:"aud"`
	Exp               int64    `json:"exp"`
	Nbf               int64    `json:"nbf"`
	Iat               int64    `json:"iat"`
	Nonce             string   `json:"nonce"`
	Tid               string   `json:"tid"`
	Oid               string   `json:"oid"`
	Name              string   `json:"name"`
	PreferredUsername string   `json:"preferred_username"`
	Email             string   `json:"email"`
	Roles             []string `json:"roles"`
}

// audience – „aud” bywa napisem albo listą.
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		*a = []string{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*a = many
	return nil
}

type OIDC struct {
	cfg    *Config
	client *http.Client
	meta   discovery

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
}

func NewOIDC(cfg *Config) (*OIDC, error) {
	o := &OIDC{cfg: cfg, client: &http.Client{Timeout: 15 * time.Second}, keys: map[string]*rsa.PublicKey{}}
	u := fmt.Sprintf("%s/%s/v2.0/.well-known/openid-configuration", cfg.Authority, url.PathEscape(cfg.TenantID))
	if err := o.getJSON(u, &o.meta); err != nil {
		return nil, fmt.Errorf("discovery Entra: %w", err)
	}
	if o.meta.AuthEndpoint == "" || o.meta.TokenEndpoint == "" || o.meta.JWKSURI == "" || o.meta.Issuer == "" {
		return nil, errors.New("discovery Entra: niepełne metadane")
	}
	// Konfiguracja wielodostępna ma w issuer znacznik {tenantid}; aplikacja jest jednodostępna.
	o.meta.Issuer = strings.ReplaceAll(o.meta.Issuer, "{tenantid}", cfg.TenantID)
	if err := o.refreshKeys(); err != nil {
		return nil, err
	}
	return o, nil
}

func (o *OIDC) getJSON(u string, v any) error {
	resp, err := o.client.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(v)
}

func (o *OIDC) refreshKeys() error {
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := o.getJSON(o.meta.JWKSURI, &set); err != nil {
		return fmt.Errorf("klucze JWKS: %w", err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "RSA" || (k.Use != "" && k.Use != "sig") {
			continue
		}
		n, err1 := base64.RawURLEncoding.DecodeString(k.N)
		e, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil || len(e) > 4 {
			continue
		}
		ei := 0
		for _, b := range e {
			ei = ei<<8 | int(b)
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: ei}
	}
	if len(keys) == 0 {
		return errors.New("klucze JWKS: brak kluczy RSA")
	}
	o.mu.Lock()
	o.keys, o.fetchedAt = keys, time.Now()
	o.mu.Unlock()
	return nil
}

// key zwraca klucz o danym kid; nieznany kid odświeża zestaw (najwyżej raz na minutę – rotacja kluczy Entra).
func (o *OIDC) key(kid string) (*rsa.PublicKey, error) {
	o.mu.Lock()
	k, ok := o.keys[kid]
	stale := time.Since(o.fetchedAt) > time.Minute
	o.mu.Unlock()
	if ok {
		return k, nil
	}
	if !stale {
		return nil, errors.New("nieznany klucz podpisu")
	}
	if err := o.refreshKeys(); err != nil {
		return nil, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if k, ok = o.keys[kid]; !ok {
		return nil, errors.New("nieznany klucz podpisu")
	}
	return k, nil
}

func (o *OIDC) RedirectURI() string { return o.cfg.BaseURL + "/auth/callback" }

// AuthURL – adres strony logowania Entra z parametrami state, nonce i PKCE (S256).
func (o *OIDC) AuthURL(state, nonce, verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"client_id":             {o.cfg.ClientID},
		"response_type":         {"code"},
		"redirect_uri":          {o.RedirectURI()},
		"response_mode":         {"query"},
		"scope":                 {"openid profile email"},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
		"prompt":                {"select_account"},
	}
	return o.meta.AuthEndpoint + "?" + q.Encode()
}

// LogoutURL – wylogowanie także z sesji Entra, powrót na stronę aplikacji.
func (o *OIDC) LogoutURL() string {
	if o.meta.EndSession == "" {
		return "/wylogowano"
	}
	return o.meta.EndSession + "?" + url.Values{"post_logout_redirect_uri": {o.cfg.BaseURL + "/wylogowano"}}.Encode()
}

// Exchange wymienia kod na tokeny i zwraca zweryfikowane oświadczenia id_token.
func (o *OIDC) Exchange(code, verifier, nonce string) (*Claims, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {o.RedirectURI()},
		"client_id":     {o.cfg.ClientID},
		"client_secret": {o.cfg.ClientSecret},
		"code_verifier": {verifier},
		"scope":         {"openid profile email"},
	}
	resp, err := o.client.PostForm(o.meta.TokenEndpoint, form)
	if err != nil {
		return nil, fmt.Errorf("wymiana kodu: %w", err)
	}
	defer resp.Body.Close()
	var tok struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
		Desc    string `json:"error_description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok); err != nil {
		return nil, fmt.Errorf("wymiana kodu: %w", err)
	}
	if resp.StatusCode != http.StatusOK || tok.IDToken == "" {
		// opis błędu Entra trafia tylko do dziennika serwera
		return nil, fmt.Errorf("wymiana kodu: HTTP %d %s %s", resp.StatusCode, tok.Error, firstLine(tok.Desc))
	}
	return o.Verify(tok.IDToken, nonce, time.Now())
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// Verify sprawdza podpis RS256 i oświadczenia: wystawca, odbiorca, dzierżawa, ważność, nonce.
func (o *OIDC) Verify(raw, nonce string, now time.Time) (*Claims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, errors.New("id_token: zły format")
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodeSeg(parts[0], &hdr); err != nil {
		return nil, fmt.Errorf("id_token: nagłówek: %w", err)
	}
	if hdr.Alg != "RS256" {
		return nil, fmt.Errorf("id_token: niedozwolony algorytm %q", hdr.Alg)
	}
	key, err := o.key(hdr.Kid)
	if err != nil {
		return nil, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("id_token: zły podpis")
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig); err != nil {
		return nil, errors.New("id_token: podpis nieprawidłowy")
	}
	var c Claims
	if err := decodeSeg(parts[1], &c); err != nil {
		return nil, fmt.Errorf("id_token: treść: %w", err)
	}
	switch {
	case c.Iss != o.meta.Issuer:
		return nil, fmt.Errorf("id_token: wystawca %q", c.Iss)
	case !contains(c.Aud, o.cfg.ClientID):
		return nil, errors.New("id_token: inny odbiorca")
	case !strings.EqualFold(c.Tid, o.cfg.TenantID):
		return nil, errors.New("id_token: inna dzierżawa (tenant)")
	case c.Exp == 0 || now.After(time.Unix(c.Exp, 0).Add(clockSkew)):
		return nil, errors.New("id_token: wygasł")
	case c.Nbf != 0 && now.Add(clockSkew).Before(time.Unix(c.Nbf, 0)):
		return nil, errors.New("id_token: jeszcze nieważny")
	case nonce == "" || subtle.ConstantTimeCompare([]byte(c.Nonce), []byte(nonce)) != 1:
		return nil, errors.New("id_token: nonce się nie zgadza")
	case c.Oid == "":
		return nil, errors.New("id_token: brak oid")
	}
	return &c, nil
}

func decodeSeg(seg string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
