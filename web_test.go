package main

// Testy całej ścieżki z fałszywym dostawcą tożsamości (Entra ID): discovery, JWKS, wymiana kodu,
// weryfikacja id_token, sesja, CSRF, test, zapis wyniku, zaświadczenie, raport i CSV.

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	tTenant = "11111111-2222-3333-4444-555555555555"
	tClient = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
)

type fakeIdP struct {
	t      *testing.T
	srv    *httptest.Server
	key    *rsa.PrivateKey
	mu     sync.Mutex
	codes  map[string]map[string]any // kod → oświadczenia (z nonce z żądania)
	nextID map[string]any            // oświadczenia dla następnego logowania
	claims func(map[string]any)      // modyfikacja oświadczeń (testy negatywne)
}

func newIdP(t *testing.T) *fakeIdP {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIdP{t: t, key: k, codes: map[string]map[string]any{}}
	mux := http.NewServeMux()
	f.srv = httptest.NewServer(mux)
	iss := f.srv.URL + "/" + tTenant + "/v2.0"
	mux.HandleFunc("/"+tTenant+"/v2.0/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"issuer": iss, "authorization_endpoint": f.srv.URL + "/authorize", "token_endpoint": f.srv.URL + "/token",
			"jwks_uri": f.srv.URL + "/keys", "end_session_endpoint": f.srv.URL + "/logout"})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "use": "sig", "kid": "k1",
			"n": base64.RawURLEncoding.EncodeToString(k.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.E)).Bytes())}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.mu.Lock()
		cl := f.codes[r.Form.Get("code")]
		delete(f.codes, r.Form.Get("code"))
		f.mu.Unlock()
		if cl == nil || r.Form.Get("client_secret") != "sekret" || r.Form.Get("code_verifier") == "" {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		// PKCE: challenge z żądania autoryzacji musi pasować do verifier
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != cl["_challenge"] {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": "PKCE"})
			return
		}
		delete(cl, "_challenge")
		json.NewEncoder(w).Encode(map[string]string{"id_token": f.sign(cl, "RS256", "k1")})
	})
	return f
}

func (f *fakeIdP) sign(cl map[string]any, alg, kid string) string {
	h, _ := json.Marshal(map[string]string{"alg": alg, "kid": kid, "typ": "JWT"})
	p, _ := json.Marshal(cl)
	in := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(p)
	sum := sha256.Sum256([]byte(in))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	return in + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (f *fakeIdP) baseClaims(nonce string) map[string]any {
	now := time.Now().Unix()
	return map[string]any{"iss": f.srv.URL + "/" + tTenant + "/v2.0", "aud": tClient, "tid": tTenant,
		"exp": now + 3600, "nbf": now - 10, "iat": now, "nonce": nonce}
}

// authorize – symulacja strony logowania Entra: odczyt parametrów i wydanie kodu dla użytkownika.
func (f *fakeIdP) authorize(t *testing.T, loc string, user map[string]any) string {
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("client_id") != tClient || q.Get("response_type") != "code" {
		t.Fatalf("niepełne żądanie autoryzacji: %s", loc)
	}
	cl := f.baseClaims(q.Get("nonce"))
	for k, v := range user {
		cl[k] = v
	}
	if f.claims != nil {
		f.claims(cl)
	}
	cl["_challenge"] = q.Get("code_challenge")
	code := randomToken()
	f.mu.Lock()
	f.codes[code] = cl
	f.mu.Unlock()
	return q.Get("redirect_uri") + "?" + url.Values{"code": {code}, "state": {q.Get("state")}}.Encode()
}

type testEnv struct {
	t   *testing.T
	idp *fakeIdP
	app *App
	srv *httptest.Server
	dir string
}

func newEnv(t *testing.T) *testEnv {
	idp := newIdP(t)
	t.Cleanup(idp.srv.Close)
	dir := t.TempDir()
	e := &testEnv{t: t, idp: idp, dir: dir}
	// serwer aplikacji startuje przed konfiguracją, bo BASE_URL to jego adres
	var handler http.Handler
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	t.Cleanup(e.srv.Close)
	cfg := &Config{BaseURL: e.srv.URL, Authority: idp.srv.URL, TenantID: tTenant, ClientID: tClient, ClientSecret: "sekret",
		SessionKey: []byte(strings.Repeat("k", 32)), DataDir: dir, AdminRole: "Raporty", AdminUPNs: map[string]bool{},
		OrgName: "Przykładowa Organizacja sp. z o.o.", Retention: 365, CertNeedPass: true}
	app, err := NewApp(cfg)
	if err != nil {
		t.Fatal(err)
	}
	e.app, handler = app, app.Handler()
	return e
}

type browser struct {
	t *testing.T
	c *http.Client
	e *testEnv
}

func (e *testEnv) browser() *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: e.t, e: e, c: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (b *browser) do(method, path string, form url.Values) (*http.Response, string) {
	b.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	u := path
	if strings.HasPrefix(path, "/") {
		u = b.e.srv.URL + path
	}
	req, _ := http.NewRequest(method, u, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", b.e.srv.URL)
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, string(data)
}

// login przechodzi przez /auth/login → (Entra) → /auth/callback; zwraca odpowiedź callbacku.
func (b *browser) login(user map[string]any) (*http.Response, string) {
	b.t.Helper()
	resp, _ := b.do("GET", "/auth/login", nil)
	if resp.StatusCode != http.StatusFound {
		b.t.Fatalf("login: status %d", resp.StatusCode)
	}
	return b.do("GET", b.e.idp.authorize(b.t, resp.Header.Get("Location"), user), nil)
}

var csrfRe = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func (b *browser) csrf() string {
	_, page := b.do("GET", "/", nil)
	m := csrfRe.FindStringSubmatch(page)
	if m == nil {
		b.t.Fatal("brak tokenu CSRF na stronie startowej")
	}
	return m[1]
}

var anna = map[string]any{"oid": "oid-anna", "name": "Anna Żółkiewska-Ślęzak", "preferred_username": "anna@firma.example"}
var adam = map[string]any{"oid": "oid-adam", "name": "Adam Nowak", "preferred_username": "adam@firma.example", "roles": []string{"Raporty"}}

// solve rozwiązuje część testu: correct pierwszych pytań poprawnie, reszta błędnie.
func (b *browser) solve(part string, correct int) (attemptPath string) {
	b.t.Helper()
	tok := b.csrf()
	resp, _ := b.do("POST", "/start/"+part, url.Values{"csrf": {tok}})
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasSuffix(loc, "/1") {
		b.t.Fatalf("start: %d %s", resp.StatusCode, loc)
	}
	base := strings.TrimSuffix(loc, "/1")
	p := b.e.app.bank.Part(part)
	for i, q := range p.Questions {
		ans := q.Key
		if i >= correct {
			ans = string(rune('A' + (int(q.Key[0]-'A')+1)%4))
		}
		r, _ := b.do("POST", fmt.Sprintf("%s/%d", base, i+1), url.Values{"csrf": {tok}, "odp": {ans}, "ruch": {"dalej"}})
		if r.StatusCode != http.StatusSeeOther {
			b.t.Fatalf("odpowiedź %d: %d", i+1, r.StatusCode)
		}
	}
	r, _ := b.do("POST", base+"/oddaj", url.Values{"csrf": {tok}})
	if r.StatusCode != http.StatusSeeOther {
		b.t.Fatalf("oddanie: %d", r.StatusCode)
	}
	return base
}

func TestFullFlowWithCertificate(t *testing.T) {
	e := newEnv(t)
	b := e.browser()

	// bez logowania: strona powitalna, chronione adresy przekierowują do logowania
	if _, page := b.do("GET", "/", nil); !strings.Contains(page, "Zaloguj się kontem służbowym") {
		t.Fatal("brak strony powitalnej")
	}
	if r, _ := b.do("GET", "/admin", nil); r.StatusCode != http.StatusSeeOther || !strings.HasPrefix(r.Header.Get("Location"), "/auth/login") {
		t.Fatalf("admin bez logowania: %d", r.StatusCode)
	}

	resp, _ := b.login(anna)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("callback: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if _, page := b.do("GET", "/", nil); !strings.Contains(page, "Anna Żółkiewska-Ślęzak") {
		t.Fatal("brak nazwy użytkownika po zalogowaniu")
	}

	// diagnoza i test niezaliczony (próg − 1): brak zaświadczenia
	n, th := len(e.app.bank.Part("B").Questions), e.app.bank.Threshold
	pass := min(n, th+2)
	b.solve("A", 1)
	base := b.solve("B", th-1)
	_, page := b.do("GET", base+"/wynik", nil)
	if !strings.Contains(page, fmt.Sprintf("%d/%d", th-1, n)) || strings.Contains(page, "/zaswiadczenie/") {
		t.Fatalf("wynik %d/%d nie powinien dawać zaświadczenia", th-1, n)
	}
	if !strings.Contains(page, fmt.Sprintf("otrzymasz po osiągnięciu co najmniej %d", th)) {
		t.Fatal("brak informacji o progu zaświadczenia")
	}

	// test zaliczony: zaświadczenie
	base = b.solve("B", pass)
	_, page = b.do("GET", base+"/wynik", nil)
	m := regexp.MustCompile(`/zaswiadczenie/([A-Z2-7-]{19})\.pdf`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("brak łącza do zaświadczenia po zaliczeniu")
	}
	cert := m[1]
	score := fmt.Sprintf("%d z %d", pass, n)
	r, pdf := b.do("GET", "/zaswiadczenie/"+cert+".pdf", nil)
	if r.StatusCode != 200 || r.Header.Get("Content-Type") != "application/pdf" || !strings.HasPrefix(pdf, "%PDF-1.7") {
		t.Fatalf("zaświadczenie: %d %s", r.StatusCode, r.Header.Get("Content-Type"))
	}
	checkPDFText(t, pdf, []string{"Zaświadczenie", "Anna Żółkiewska-Ślęzak", score, cert, "Przykładowa Organizacja sp. z o.o."})

	// ponowne oddanie tej samej próby nie tworzy drugiego wyniku
	tok := b.csrf()
	b.do("POST", base+"/oddaj", url.Values{"csrf": {tok}})
	if n := len(e.app.results.ByUser("oid-anna")); n != 3 {
		t.Fatalf("liczba zapisanych wyników %d, oczekiwano 3", n)
	}

	// inny użytkownik: nie widzi cudzej próby ani zaświadczenia, nie ma raportu; weryfikacja działa
	other := e.browser()
	other.login(map[string]any{"oid": "oid-ewa", "name": "Ewa Test", "preferred_username": "ewa@firma.example"})
	if r, page := other.do("GET", base+"/wynik", nil); r.StatusCode != 404 || !strings.Contains(page, "niedostępna") {
		t.Fatalf("cudza próba: %d", r.StatusCode)
	}
	if r, _ := other.do("GET", "/zaswiadczenie/"+cert+".pdf", nil); r.StatusCode != 404 {
		t.Fatalf("cudze zaświadczenie: %d", r.StatusCode)
	}
	if r, _ := other.do("GET", "/admin", nil); r.StatusCode != 403 {
		t.Fatalf("raport bez roli: %d", r.StatusCode)
	}
	if _, page := other.do("GET", "/weryfikacja?numer="+strings.ToLower(strings.ReplaceAll(cert, "-", "")), nil); !strings.Contains(page, "jest prawdziwe") {
		t.Fatal("weryfikacja numeru wpisanego małymi literami bez myślników")
	}
	if _, page := other.do("GET", "/weryfikacja?numer=AAAA-BBBB-CCCC-DDDD", nil); !strings.Contains(page, "Nie znaleziono") {
		t.Fatal("weryfikacja nieistniejącego numeru")
	}

	// administrator (rola aplikacji z Entra): raport, CSV, dostęp do zaświadczenia
	adm := e.browser()
	adm.login(adam)
	r, page = adm.do("GET", "/admin", nil)
	if r.StatusCode != 200 || !strings.Contains(page, "Anna Żółkiewska-Ślęzak") || !strings.Contains(page, score) {
		t.Fatalf("raport: %d", r.StatusCode)
	}
	r, csv := adm.do("GET", "/admin/wyniki.csv", nil)
	if r.StatusCode != 200 || !strings.HasPrefix(csv, "\xEF\xBB\xBF") || strings.Count(csv, "\n") != 4 || !strings.Contains(csv, cert) {
		t.Fatalf("CSV: %d\n%s", r.StatusCode, csv)
	}
	if strings.Contains(csv, "3/3") {
		t.Fatal("CSV: wynik modułu w postaci daty")
	}
	if r, _ := adm.do("GET", "/zaswiadczenie/"+cert+".pdf", nil); r.StatusCode != 200 {
		t.Fatalf("zaświadczenie dla administratora: %d", r.StatusCode)
	}

	// wyniki przetrwają restart (plik JSON Lines)
	re, err := OpenResults(e.dir)
	if err != nil || len(re.All()) != 3 || re.ByCert(cert) == nil {
		t.Fatalf("odczyt po restarcie: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(e.dir, "wyniki.jsonl"))
	if strings.Contains(string(raw), `"odp"`) || strings.Contains(string(raw), "Klikam") {
		t.Fatal("w pliku wyników nie może być treści odpowiedzi")
	}

	// wylogowanie: przekierowanie do Entra, sesja usunięta
	tok = b.csrf()
	r, _ = b.do("POST", "/auth/logout", url.Values{"csrf": {tok}})
	if !strings.HasPrefix(r.Header.Get("Location"), e.idp.srv.URL+"/logout?post_logout_redirect_uri=") {
		t.Fatalf("wylogowanie: %s", r.Header.Get("Location"))
	}
	if _, page := b.do("GET", "/", nil); !strings.Contains(page, "Zaloguj się kontem służbowym") {
		t.Fatal("sesja nadal aktywna po wylogowaniu")
	}
}

func checkPDFText(t *testing.T, pdf string, want []string) {
	t.Helper()
	tool, err := exec.LookPath("pdftotext")
	if err != nil {
		t.Log("pdftotext niedostępny – pominięto sprawdzenie tekstu PDF")
		return
	}
	f := filepath.Join(t.TempDir(), "z.pdf")
	os.WriteFile(f, []byte(pdf), 0o600)
	if dst := os.Getenv("SA_CERT_OUT"); dst != "" {
		os.WriteFile(dst, []byte(pdf), 0o644) // podgląd zaświadczenia z testu
	}
	out, err := exec.Command(tool, "-layout", f, "-").CombinedOutput()
	if err != nil {
		t.Fatalf("pdftotext: %v %s", err, out)
	}
	text := strings.Join(strings.Fields(string(out)), " ")
	for _, w := range want {
		if !strings.Contains(text, w) {
			t.Errorf("PDF: brak tekstu %q\n%s", w, text)
		}
	}
}

func TestCSRFAndOrigin(t *testing.T) {
	e := newEnv(t)
	b := e.browser()
	b.login(anna)
	if r, _ := b.do("POST", "/start/B", url.Values{"csrf": {"zly"}}); r.StatusCode != 403 {
		t.Fatalf("POST bez poprawnego CSRF: %d", r.StatusCode)
	}
	req, _ := http.NewRequest("POST", e.srv.URL+"/start/B", strings.NewReader("csrf="+b.csrf()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://atak.example")
	resp, _ := b.c.Do(req)
	if resp.StatusCode != 403 {
		t.Fatalf("POST z obcego źródła: %d", resp.StatusCode)
	}
	req.Header.Set("Origin", "null")
	req.Body = io.NopCloser(strings.NewReader("csrf=" + b.csrf()))
	if resp, _ = b.c.Do(req); resp.StatusCode != 403 {
		t.Fatalf("POST z Origin: null: %d", resp.StatusCode)
	}
	// przeglądarka podaje źródło przy własnych formularzach tylko, gdy Referrer-Policy nie jest no-referrer
	if r, _ := b.do("GET", "/", nil); r.Header.Get("Referrer-Policy") != "same-origin" {
		t.Fatalf("Referrer-Policy: %q", r.Header.Get("Referrer-Policy"))
	}
}

func TestLoginRejections(t *testing.T) {
	cases := map[string]func(map[string]any){
		"inna dzierżawa": func(c map[string]any) { c["tid"] = "99999999-0000-0000-0000-000000000000" },
		"inny odbiorca":  func(c map[string]any) { c["aud"] = "inna-aplikacja" },
		"inny wystawca":  func(c map[string]any) { c["iss"] = "https://login.microsoftonline.com/x/v2.0" },
		"wygasły":        func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() },
		"zły nonce":      func(c map[string]any) { c["nonce"] = "podmieniony" },
		"brak oid":       func(c map[string]any) { delete(c, "oid") },
		"przyszłe nbf":   func(c map[string]any) { c["nbf"] = time.Now().Add(time.Hour).Unix() },
	}
	for name, mod := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.idp.claims = mod
			r, page := e.browser().login(anna)
			if r.StatusCode != http.StatusUnauthorized || !strings.Contains(page, "Logowanie nieudane") {
				t.Fatalf("status %d", r.StatusCode)
			}
		})
	}
	t.Run("podmieniony state", func(t *testing.T) {
		e := newEnv(t)
		b := e.browser()
		resp, _ := b.do("GET", "/auth/login", nil)
		cb := e.idp.authorize(t, resp.Header.Get("Location"), anna)
		cb = regexp.MustCompile(`state=[^&]+`).ReplaceAllString(cb, "state=obcy")
		if r, _ := b.do("GET", cb, nil); r.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status %d", r.StatusCode)
		}
	})
	t.Run("callback bez ciasteczka logowania", func(t *testing.T) {
		e := newEnv(t)
		resp, _ := e.browser().do("GET", "/auth/login", nil)
		cb := e.idp.authorize(t, resp.Header.Get("Location"), anna)
		if r, _ := e.browser().do("GET", cb, nil); r.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status %d", r.StatusCode)
		}
	})
}

func TestVerifyRejectsAlgAndSignature(t *testing.T) {
	e := newEnv(t)
	o := e.app.oidc
	cl := e.idp.baseClaims("n1")
	cl["oid"] = "x"
	good := e.idp.sign(cl, "RS256", "k1")
	if _, err := o.Verify(good, "n1", time.Now()); err != nil {
		t.Fatalf("poprawny token odrzucony: %v", err)
	}
	parts := strings.Split(good, ".")
	none := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","kid":"k1"}`)) + "." + parts[1] + "."
	if _, err := o.Verify(none, "n1", time.Now()); err == nil {
		t.Fatal("alg=none przyjęty")
	}
	tampered := parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"oid":"admin"}`)) + "." + parts[2]
	if _, err := o.Verify(tampered, "n1", time.Now()); err == nil {
		t.Fatal("zmieniona treść przyjęta")
	}
	if _, err := o.Verify(e.idp.sign(cl, "RS256", "nieznany"), "n1", time.Now()); err == nil {
		t.Fatal("nieznany klucz przyjęty")
	}
}

func TestSafeReturnAndCertID(t *testing.T) {
	for in, want := range map[string]string{"/proba/x/1": "/proba/x/1", "//atak.example": "/", "https://atak.example": "/", "/\\atak": "/", "": "/"} {
		if got := safeReturn(in); got != want {
			t.Errorf("safeReturn(%q)=%q", in, got)
		}
	}
	id := NewCertID()
	if NormalizeCertID(strings.ToLower(strings.ReplaceAll(id, "-", ""))) != id || NormalizeCertID("ABCD-0000-1111-8888") != "" {
		t.Fatal("NormalizeCertID")
	}
}

func TestRetentionPurge(t *testing.T) {
	r, _ := OpenResults(t.TempDir())
	now := time.Now()
	r.Add(Record{ID: "stary", OID: "o", Finished: now.AddDate(0, 0, -400)})
	r.Add(Record{ID: "nowy", OID: "o", Finished: now.AddDate(0, 0, -10)})
	if n, err := r.Purge(365, now); err != nil || n != 1 || len(r.All()) != 1 || r.All()[0].ID != "nowy" {
		t.Fatalf("purge: %d %v", n, err)
	}
	re, _ := OpenResults(filepath.Dir(r.path))
	if len(re.All()) != 1 {
		t.Fatal("purge nie zapisał pliku")
	}
}

func TestConfigValidation(t *testing.T) {
	set := func(kv map[string]string) {
		for _, k := range []string{"BASE_URL", "ENTRA_TENANT_ID", "ENTRA_CLIENT_ID", "ENTRA_CLIENT_SECRET", "SESSION_KEY", "ORG_NAME", "DEV_LOGIN"} {
			t.Setenv(k, kv[k])
		}
	}
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))
	set(map[string]string{"BASE_URL": "https://szkolenie.example", "ENTRA_TENANT_ID": "t", "ENTRA_CLIENT_ID": "c", "ENTRA_CLIENT_SECRET": "s", "SESSION_KEY": key, "ORG_NAME": "O"})
	if _, err := LoadConfig(); err != nil {
		t.Fatalf("poprawna konfiguracja: %v", err)
	}
	set(map[string]string{"BASE_URL": "http://szkolenie.example", "ENTRA_TENANT_ID": "t", "ENTRA_CLIENT_ID": "c", "ENTRA_CLIENT_SECRET": "s", "SESSION_KEY": key, "ORG_NAME": "O"})
	if _, err := LoadConfig(); err == nil {
		t.Fatal("http poza localhost przyjęty")
	}
	set(map[string]string{"BASE_URL": "https://szkolenie.example", "DEV_LOGIN": "1", "SESSION_KEY": key, "ORG_NAME": "O"})
	if _, err := LoadConfig(); err == nil {
		t.Fatal("DEV_LOGIN poza localhost przyjęty")
	}
	set(map[string]string{"BASE_URL": "https://szkolenie.example", "ENTRA_TENANT_ID": "t", "ENTRA_CLIENT_ID": "c", "ENTRA_CLIENT_SECRET": "s", "SESSION_KEY": "krotki", "ORG_NAME": "O"})
	if _, err := LoadConfig(); err == nil {
		t.Fatal("krótki SESSION_KEY przyjęty")
	}
}
