package main

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Config – ustawienia z zmiennych środowiskowych (opis w WDROZENIE.md).
type Config struct {
	Listen       string // adres nasłuchu w kontenerze, np. ":8080"
	BaseURL      string // publiczny adres aplikacji bez końcowego „/”, np. https://szkolenie.firma.pl
	Authority    string // https://login.microsoftonline.com (zmiana tylko w testach lub chmurach krajowych)
	TenantID     string
	ClientID     string
	ClientSecret string
	SessionKey   []byte // ≥ 32 bajty, podpis ciasteczek
	DataDir      string // katalog na wyniki (wolumen)
	AdminRole    string // wartość roli aplikacji w Entra dającej dostęp do raportów
	AdminUPNs    map[string]bool
	OrgName      string // nazwa organizacji na zaświadczeniu i w informacji o danych
	PrivacyText  string // dodatkowy akapit informacji o przetwarzaniu danych (administrator, kontakt)
	Retention    int    // dni przechowywania wyników; 0 = bez automatycznego usuwania
	CertNeedPass bool   // zaświadczenie tylko po osiągnięciu progu testu końcowego
	DevLogin     bool   // logowanie testowe bez Entra – wyłącznie na localhost
	Questions    string // plik banku pytań zamiast wbudowanego data/questions.json
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func LoadConfig() (*Config, error) {
	c := &Config{
		Listen:       env("LISTEN", ":8080"),
		BaseURL:      strings.TrimRight(env("BASE_URL", "http://localhost:8080"), "/"),
		Authority:    strings.TrimRight(env("ENTRA_AUTHORITY", "https://login.microsoftonline.com"), "/"),
		TenantID:     env("ENTRA_TENANT_ID", ""),
		ClientID:     env("ENTRA_CLIENT_ID", ""),
		ClientSecret: os.Getenv("ENTRA_CLIENT_SECRET"),
		DataDir:      env("DATA_DIR", "/data"),
		AdminRole:    env("ADMIN_ROLE", "Raporty"),
		AdminUPNs:    map[string]bool{},
		OrgName:      env("ORG_NAME", ""),
		PrivacyText:  env("PRIVACY_TEXT", ""),
		CertNeedPass: env("CERT_REQUIRE_PASS", "1") != "0",
		DevLogin:     env("DEV_LOGIN", "0") == "1",
		Questions:    env("QUESTIONS_FILE", ""),
	}
	for _, u := range strings.Split(env("ADMIN_UPNS", ""), ",") {
		if u = strings.ToLower(strings.TrimSpace(u)); u != "" {
			c.AdminUPNs[u] = true
		}
	}
	days, err := strconv.Atoi(env("RETENTION_DAYS", "365"))
	if err != nil || days < 0 {
		return nil, errors.New("RETENTION_DAYS: podaj liczbę dni (0 = bez usuwania)")
	}
	c.Retention = days

	if c.SessionKey, err = decodeKey(os.Getenv("SESSION_KEY")); err != nil {
		return nil, err
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("BASE_URL: nieprawidłowy adres %q", c.BaseURL)
	}
	local := isLocalHost(u.Hostname())
	if u.Scheme == "http" && !local {
		return nil, errors.New("BASE_URL musi używać https (http dozwolone tylko dla localhost)")
	}
	if c.DevLogin {
		if !local {
			return nil, errors.New("DEV_LOGIN=1 jest dozwolone tylko, gdy BASE_URL wskazuje localhost")
		}
	} else if c.TenantID == "" || c.ClientID == "" || c.ClientSecret == "" {
		return nil, errors.New("ustaw ENTRA_TENANT_ID, ENTRA_CLIENT_ID i ENTRA_CLIENT_SECRET")
	}
	if c.OrgName == "" {
		return nil, errors.New("ustaw ORG_NAME (nazwa organizacji na zaświadczeniu)")
	}
	return c, nil
}

func (c *Config) Secure() bool { return strings.HasPrefix(c.BaseURL, "https://") }

func isLocalHost(h string) bool { return h == "localhost" || h == "127.0.0.1" || h == "::1" }

// decodeKey przyjmuje klucz w base64 (standardowym lub URL) albo hex; wymaga co najmniej 32 bajtów.
func decodeKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("ustaw SESSION_KEY (np. wynik: openssl rand -base64 32)")
	}
	for _, dec := range []func(string) ([]byte, error){
		base64.StdEncoding.DecodeString, base64.RawStdEncoding.DecodeString,
		base64.URLEncoding.DecodeString, base64.RawURLEncoding.DecodeString, hex.DecodeString,
	} {
		if b, err := dec(s); err == nil && len(b) >= 32 {
			return b, nil
		}
	}
	return nil, errors.New("SESSION_KEY: wymagane co najmniej 32 bajty w base64 lub hex")
}
