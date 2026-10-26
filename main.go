// Aplikacja webowa „Cyberbezpieczeństwo w codziennej pracy” – test z logowaniem Microsoft Entra ID.
//
// Ta sama baza pytań co aplikacja offline i PDF (data/questions.json z content/questions.py).
// Uczestnik loguje się kontem służbowym, rozwiązuje diagnozę i test końcowy, po zaliczeniu
// pobiera zaświadczenie uczestnictwa (PDF). Wyniki (bez treści odpowiedzi) trafiają do pliku
// w DATA_DIR; osoby z rolą aplikacji ADMIN_ROLE widzą raport i eksport CSV.
// Tylko biblioteka standardowa Go. Konfiguracja: zmienne środowiskowe (WDROZENIE.md).
package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

//go:embed data/questions.json
var questionsJSON []byte

//go:embed web/templates/*.html
var templatesFS embed.FS

//go:embed web/static
var staticFS embed.FS

type App struct {
	cfg      *Config
	bank     *Bank
	tmpl     *template.Template
	attempts *Store
	results  *Results
	oidc     *OIDC // nil w trybie DEV_LOGIN
	signer   Signer
	fonts    *certFonts
	limits   *limits
	revoked  *Revoked
}

var funcs = template.FuncMap{
	"letter": func(i int) string { return string(rune('A' + i)) },
	"add":    func(a, b int) int { return a + b },
	"pct": func(a, b int) int {
		if b == 0 {
			return 0
		}
		return a * 100 / b
	},
	// długość łuku (obwód okręgu r=52 ≈ 326,7) – wykres wyniku w SVG, bez stylów inline
	"arc": func(a, b int) string {
		if b == 0 {
			return "0 327"
		}
		return fmt.Sprintf("%.1f 327", 326.7*float64(a)/float64(b))
	},
	// szerokość paska modułu (SVG, 0–200)
	"bar": func(a, b int) int {
		if b == 0 {
			return 0
		}
		return a * 200 / b
	},
	"date":     plDate,
	"datetime": func(t time.Time) string { return t.In(warsaw).Format("02.01.2006 15:04") },
}

func main() {
	// „web-app healthcheck” – dla HEALTHCHECK w obrazie bez powłoki i curl
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		addr := env("LISTEN", ":8080")
		if strings.HasPrefix(addr, ":") {
			addr = "127.0.0.1" + addr
		}
		c := &http.Client{Timeout: 3 * time.Second}
		resp, err := c.Get("http://" + addr + "/healthz")
		if err != nil || resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		os.Exit(0)
	}
	cfg, err := LoadConfig()
	if err != nil {
		log.Fatalf("konfiguracja: %v", err)
	}
	app, err := NewApp(cfg)
	if err != nil {
		log.Fatalf("start: %v", err)
	}
	srv := &http.Server{Addr: cfg.Listen, Handler: app.Handler(), ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 16 << 10}

	go app.retentionLoop()
	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()
	if cfg.DevLogin {
		log.Printf("UWAGA: DEV_LOGIN=1 – logowanie testowe bez Entra. Nie używaj w produkcji.")
	}
	log.Printf("wersja pytań %s, nasłuch %s, adres %s, dane %s, retencja %d dni", app.bank.Version, cfg.Listen, cfg.BaseURL, cfg.DataDir, cfg.Retention)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func NewApp(cfg *Config) (*App, error) {
	a := &App{cfg: cfg, signer: Signer{cfg.SessionKey}, attempts: NewStore(5000, 12*time.Hour),
		limits: newLimits(), revoked: &Revoked{ids: map[string]int64{}}}
	var err error
	raw := questionsJSON
	if cfg.Questions != "" {
		if raw, err = os.ReadFile(cfg.Questions); err != nil {
			return nil, fmt.Errorf("QUESTIONS_FILE: %w", err)
		}
	}
	if a.bank, err = LoadBank(raw); err != nil {
		return nil, fmt.Errorf("pytania: %w", err)
	}
	if a.tmpl, err = template.New("").Funcs(funcs).ParseFS(templatesFS, "web/templates/*.html"); err != nil {
		return nil, err
	}
	if a.fonts, err = loadCertFonts(); err != nil {
		return nil, err
	}
	if a.results, err = OpenResults(cfg.DataDir); err != nil {
		return nil, fmt.Errorf("wyniki: %w", err)
	}
	if !cfg.DevLogin {
		if a.oidc, err = NewOIDC(cfg); err != nil {
			return nil, err
		}
	}
	return a, nil
}

func (a *App) retentionLoop() {
	for {
		if n, err := a.results.Purge(a.cfg.Retention, time.Now()); err != nil {
			log.Printf("retencja: %v", err)
		} else if n > 0 {
			log.Printf("retencja: usunięto %d wyników starszych niż %d dni", n, a.cfg.Retention)
		}
		time.Sleep(6 * time.Hour)
	}
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "web/static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", noListing(http.FileServerFS(static))))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /{$}", a.handleIndex)
	mux.HandleFunc("GET /prywatnosc", a.handlePrivacy)
	mux.HandleFunc("GET /auth/login", a.handleLogin)
	mux.HandleFunc("POST /auth/login", a.handleDevLogin)
	mux.HandleFunc("GET /auth/callback", a.handleCallback)
	mux.HandleFunc("POST /auth/logout", a.handleLogout)
	mux.HandleFunc("GET /wylogowano", a.handleLoggedOut)

	mux.HandleFunc("POST /start/{part}", a.user(a.handleStart))
	mux.HandleFunc("GET /proba/{id}/podsumowanie", a.user(a.handleReview))
	mux.HandleFunc("GET /proba/{id}/wynik", a.user(a.handleResult))
	mux.HandleFunc("POST /proba/{id}/oddaj", a.user(a.handleFinish))
	mux.HandleFunc("GET /proba/{id}/{n}", a.user(a.handleQuestion))
	mux.HandleFunc("POST /proba/{id}/{n}", a.user(a.handleAnswer))
	mux.HandleFunc("GET /zaswiadczenie/{cert}", a.user(a.handleCertificate))
	mux.HandleFunc("GET /weryfikacja/{cert}", a.user(a.handleVerify))
	mux.HandleFunc("GET /weryfikacja", a.user(a.handleVerify))

	mux.HandleFunc("GET /admin", a.admin(a.handleAdmin))
	mux.HandleFunc("GET /admin/wyniki.csv", a.admin(a.handleCSV))
	return a.securityHeaders(a.rateLimit(mux))
}

type userHandler func(http.ResponseWriter, *http.Request, *Session)

// user wymaga zalogowania; dla POST sprawdza też token CSRF z formularza (warstwa 2).
func (a *App) user(h userHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := a.session(r)
		if s == nil {
			if r.Method == http.MethodGet {
				http.Redirect(w, r, "/auth/login?wroc="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			} else {
				http.Redirect(w, r, "/", http.StatusSeeOther)
			}
			return
		}
		if r.Method == http.MethodPost && subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf")), []byte(s.CSRF)) != 1 {
			a.audit(r, "csrf_zly_token", "oid", s.OID)
			http.Error(w, "Sesja formularza wygasła. Wróć i spróbuj ponownie.", http.StatusForbidden)
			return
		}
		h(w, r, s)
	}
}

func (a *App) admin(h userHandler) http.HandlerFunc {
	return a.user(func(w http.ResponseWriter, r *http.Request, s *Session) {
		if !s.Admin {
			a.audit(r, "odmowa_raportu", "oid", s.OID, "login", s.UPN)
			a.render(w, r, s, http.StatusForbidden, "forbidden.html", map[string]any{"Title": "Brak dostępu"})
			return
		}
		h(w, r, s)
	})
}

func (a *App) render(w http.ResponseWriter, r *http.Request, s *Session, status int, name string, data map[string]any) {
	data["Bank"] = a.bank
	data["S"] = s
	data["Org"] = a.cfg.OrgName
	data["Dev"] = a.cfg.DevLogin
	if s != nil {
		data["CSRF"] = s.CSRF
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := a.tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("szablon %s: %v", name, err)
	}
}

// ---------- logowanie ----------

func safeReturn(p string) string {
	// tylko ścieżki lokalne („/…”, bez „//” i „/\”) – brak otwartego przekierowania
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") {
		return "/"
	}
	return p
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	ret := safeReturn(r.URL.Query().Get("wroc"))
	if a.cfg.DevLogin {
		a.render(w, r, nil, http.StatusOK, "devlogin.html", map[string]any{"Title": "Logowanie testowe", "Return": ret})
		return
	}
	st := loginState{State: randomToken(), Nonce: randomToken(), Verifier: randomToken() + randomToken(), Exp: time.Now().Add(loginTTL).Unix()}
	v, err := a.signer.Seal("logowanie", struct {
		loginState
		Return string `json:"r"`
	}{st, ret})
	if err != nil {
		http.Error(w, "Błąd serwera", http.StatusInternalServerError)
		return
	}
	a.setCookie(w, a.cookieName(loginCookie), v, loginTTL)
	http.Redirect(w, r, a.oidc.AuthURL(st.State, st.Nonce, st.Verifier), http.StatusFound)
}

func (a *App) handleCallback(w http.ResponseWriter, r *http.Request) {
	if a.oidc == nil {
		http.NotFound(w, r)
		return
	}
	fail := func(msg string, err error) {
		if err != nil {
			log.Printf("logowanie: %s: %v", msg, err)
		}
		a.audit(r, "logowanie_nieudane", "powod", msg)
		a.render(w, r, nil, http.StatusUnauthorized, "loginerror.html", map[string]any{"Title": "Logowanie nieudane", "Msg": msg})
	}
	c, err := r.Cookie(a.cookieName(loginCookie))
	if err != nil {
		fail("Sesja logowania wygasła. Zaloguj się ponownie.", nil)
		return
	}
	a.clearCookie(w, a.cookieName(loginCookie))
	var st struct {
		loginState
		Return string `json:"r"`
	}
	if a.signer.Open("logowanie", c.Value, &st) != nil || time.Now().Unix() > st.Exp {
		fail("Sesja logowania wygasła. Zaloguj się ponownie.", nil)
		return
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		fail("Logowanie zostało przerwane lub odrzucone przez Microsoft Entra.", fmt.Errorf("%s: %s", e, firstLine(q.Get("error_description"))))
		return
	}
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(st.State)) != 1 || q.Get("code") == "" {
		fail("Nieprawidłowa odpowiedź logowania. Zaloguj się ponownie.", errors.New("state"))
		return
	}
	cl, err := a.oidc.Exchange(q.Get("code"), st.Verifier, st.Nonce)
	if err != nil {
		fail("Nie udało się potwierdzić logowania. Spróbuj ponownie.", err)
		return
	}
	upn := cl.PreferredUsername
	if upn == "" {
		upn = cl.Email
	}
	s := Session{OID: cl.Oid, Name: cl.Name, UPN: upn,
		Admin: contains(cl.Roles, a.cfg.AdminRole) || a.cfg.AdminUPNs[strings.ToLower(upn)]}
	if err := a.startSession(w, s); err != nil {
		fail("Błąd serwera.", err)
		return
	}
	a.audit(r, "logowanie", "oid", s.OID, "login", s.UPN, "admin", s.Admin)
	http.Redirect(w, r, safeReturn(st.Return), http.StatusSeeOther)
}

// handleDevLogin – tylko DEV_LOGIN=1 (dozwolone wyłącznie na localhost, sprawdza LoadConfig).
func (a *App) handleDevLogin(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.DevLogin {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	upn := strings.ToLower(strings.TrimSpace(r.PostFormValue("upn")))
	if name == "" || upn == "" {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}
	s := Session{OID: "dev-" + upn, Name: name, UPN: upn, Admin: r.PostFormValue("admin") == "1"}
	a.audit(r, "logowanie_testowe", "login", upn, "admin", s.Admin)
	if err := a.startSession(w, s); err != nil {
		http.Error(w, "Błąd serwera", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, safeReturn(r.PostFormValue("wroc")), http.StatusSeeOther)
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	s := a.session(r)
	if s != nil && subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf")), []byte(s.CSRF)) != 1 {
		http.Error(w, "Błędny token formularza", http.StatusForbidden)
		return
	}
	if s != nil {
		a.revoked.Add(s.ID, s.Exp)
		a.audit(r, "wylogowanie", "oid", s.OID)
	}
	a.clearCookie(w, a.cookieName(sessionCookie))
	if a.oidc != nil {
		http.Redirect(w, r, a.oidc.LogoutURL(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/wylogowano", http.StatusSeeOther)
}

func (a *App) handleLoggedOut(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, nil, http.StatusOK, "bye.html", map[string]any{"Title": "Wylogowano"})
}

func (a *App) handlePrivacy(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, a.session(r), http.StatusOK, "privacy.html", map[string]any{
		"Title": "Informacja o danych", "Retention": a.cfg.Retention, "PrivacyText": a.cfg.PrivacyText})
}

// ---------- strona startowa i test ----------

func (a *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	s := a.session(r)
	if s == nil {
		a.render(w, r, nil, http.StatusOK, "landing.html", map[string]any{"Title": "Start"})
		return
	}
	recs := a.results.ByUser(s.OID)
	var cert *Record
	for i := range recs {
		if recs[i].CertID != "" {
			cert = &recs[i]
			break
		}
	}
	a.render(w, r, s, http.StatusOK, "index.html", map[string]any{"Title": "Start", "History": recs, "Cert": cert})
}

func (a *App) handleStart(w http.ResponseWriter, r *http.Request, s *Session) {
	part := a.bank.Part(r.PathValue("part"))
	if part == nil {
		http.NotFound(w, r)
		return
	}
	at := a.attempts.New(s.OID, part.ID)
	http.Redirect(w, r, fmt.Sprintf("/proba/%s/1", at.ID), http.StatusSeeOther)
}

func (a *App) attemptFor(w http.ResponseWriter, r *http.Request, s *Session) (*Attempt, *Part) {
	at := a.attempts.Get(r.PathValue("id"), s.OID)
	if at == nil {
		a.render(w, r, s, http.StatusNotFound, "expired.html", map[string]any{"Title": "Próba niedostępna"})
		return nil, nil
	}
	return at, a.bank.Part(at.PartID)
}

func qIndex(r *http.Request, total int) (int, bool) {
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 1 || n > total {
		return 0, false
	}
	return n, true
}

// Nav – kafelek nawigacji między pytaniami.
type Nav struct {
	N        int
	Answered bool
	Current  bool
}

func navFor(part *Part, answers map[string]string, current int) []Nav {
	var out []Nav
	for i, q := range part.Questions {
		out = append(out, Nav{N: i + 1, Answered: answers[q.ID] != "", Current: i+1 == current})
	}
	return out
}

func (a *App) handleQuestion(w http.ResponseWriter, r *http.Request, s *Session) {
	at, part := a.attemptFor(w, r, s)
	if at == nil {
		return
	}
	if at.Result() != nil {
		http.Redirect(w, r, "/proba/"+at.ID+"/wynik", http.StatusSeeOther)
		return
	}
	n, ok := qIndex(r, len(part.Questions))
	if !ok {
		http.NotFound(w, r)
		return
	}
	pub := part.Public()
	answers := at.Answers()
	q := pub.Questions[n-1]
	a.render(w, r, s, http.StatusOK, "question.html", map[string]any{
		"Title": fmt.Sprintf("Pytanie %d z %d", n, len(pub.Questions)),
		"A":     at, "Part": pub, "N": n, "Total": len(pub.Questions), "Q": q,
		"Module": a.bank.Module(q.Module), "Given": answers[q.ID], "Answered": len(answers),
		"Nav": navFor(part, answers, n), "IsLast": n == len(pub.Questions),
	})
}

func (a *App) handleAnswer(w http.ResponseWriter, r *http.Request, s *Session) {
	at, part := a.attemptFor(w, r, s)
	if at == nil {
		return
	}
	total := len(part.Questions)
	n, ok := qIndex(r, total)
	if !ok {
		http.Error(w, "Błędne żądanie", http.StatusBadRequest)
		return
	}
	if at.Result() == nil {
		if v := r.PostFormValue("odp"); len(v) == 1 && v[0] >= 'A' && v[0] <= 'D' {
			at.Set(part.Questions[n-1].ID, v)
		}
	}
	next := n + 1
	switch mv := r.PostFormValue("ruch"); mv {
	case "wstecz":
		next = n - 1
	case "podsumowanie":
		next = total + 1
	case "", "dalej":
	default:
		if j, err := strconv.Atoi(mv); err == nil && j >= 1 && j <= total {
			next = j
		}
	}
	if next < 1 {
		next = 1
	}
	if next > total {
		http.Redirect(w, r, "/proba/"+at.ID+"/podsumowanie", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/proba/%s/%d", at.ID, next), http.StatusSeeOther)
}

func (a *App) handleReview(w http.ResponseWriter, r *http.Request, s *Session) {
	at, part := a.attemptFor(w, r, s)
	if at == nil {
		return
	}
	if at.Result() != nil {
		http.Redirect(w, r, "/proba/"+at.ID+"/wynik", http.StatusSeeOther)
		return
	}
	answers := at.Answers()
	var missing []int
	for i, q := range part.Questions {
		if answers[q.ID] == "" {
			missing = append(missing, i+1)
		}
	}
	a.render(w, r, s, http.StatusOK, "review.html", map[string]any{
		"Title": "Podsumowanie", "A": at, "Part": part.Public(), "Missing": missing, "Total": len(part.Questions),
		"Answered": len(answers), "Nav": navFor(part, answers, 0),
	})
}

func (a *App) handleFinish(w http.ResponseWriter, r *http.Request, s *Session) {
	at, part := a.attemptFor(w, r, s)
	if at == nil {
		return
	}
	answers := at.AnswerIndexes() // przed Finish: Finish trzyma blokadę próby
	err := at.Finish(
		func() Result { return a.bank.Score(part, answers) },
		func(res Result) (*Record, error) {
			rec := Record{ID: randomToken()[:16], OID: s.OID, Name: s.Name, UPN: s.UPN, Part: part.ID,
				Correct: res.Correct, Total: res.Total, Passed: res.MeetsTarget, Finished: time.Now().UTC()}
			for _, row := range res.Rows {
				rec.Modules = append(rec.Modules, ModScore{Code: row.Module.Code, Correct: row.Correct, Total: row.Total})
			}
			for _, m := range res.Critical {
				rec.Critical = append(rec.Critical, m.Code)
			}
			if res.IsFinal && (res.MeetsTarget || !a.cfg.CertNeedPass) {
				rec.CertID = NewCertID()
			}
			if err := a.results.Add(rec); err != nil {
				return nil, err
			}
			return &rec, nil
		})
	if err != nil {
		log.Printf("zapis wyniku: %v", err)
		http.Error(w, "Nie udało się zapisać wyniku. Spróbuj oddać test ponownie za chwilę.", http.StatusServiceUnavailable)
		return
	}
	http.Redirect(w, r, "/proba/"+at.ID+"/wynik", http.StatusSeeOther)
}

func (a *App) handleResult(w http.ResponseWriter, r *http.Request, s *Session) {
	at, _ := a.attemptFor(w, r, s)
	if at == nil {
		return
	}
	res := at.Result()
	if res == nil {
		http.Redirect(w, r, "/proba/"+at.ID+"/podsumowanie", http.StatusSeeOther)
		return
	}
	a.render(w, r, s, http.StatusOK, "result.html", map[string]any{"Title": "Wynik", "Result": res, "Rec": at.Record(),
		"CertNeedPass": a.cfg.CertNeedPass})
}

// ---------- zaświadczenia ----------

func (a *App) handleCertificate(w http.ResponseWriter, r *http.Request, s *Session) {
	rec := a.results.ByCert(strings.TrimSuffix(r.PathValue("cert"), ".pdf"))
	if rec == nil || (rec.OID != s.OID && !s.Admin) {
		http.NotFound(w, r)
		return
	}
	if rec.OID != s.OID {
		a.audit(r, "zaswiadczenie_innej_osoby", "oid", s.OID, "numer", rec.CertID)
	}
	pdf, err := a.CertificatePDF(rec)
	if err != nil {
		log.Printf("zaświadczenie %s: %v", rec.CertID, err)
		http.Error(w, "Nie udało się utworzyć zaświadczenia", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="zaswiadczenie_%s.pdf"`, rec.CertID))
	w.Write(pdf)
}

// handleVerify – sprawdzenie numeru zaświadczenia przez zalogowanego pracownika organizacji.
func (a *App) handleVerify(w http.ResponseWriter, r *http.Request, s *Session) {
	id := r.PathValue("cert")
	if id == "" {
		id = r.URL.Query().Get("numer")
	}
	data := map[string]any{"Title": "Weryfikacja zaświadczenia", "Query": id}
	if id != "" {
		data["Checked"] = true
		data["Rec"] = a.results.ByCert(id)
	}
	a.render(w, r, s, http.StatusOK, "verify.html", data)
}

func randomToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		log.Fatal(err)
	}
	return hex.EncodeToString(b)
}
