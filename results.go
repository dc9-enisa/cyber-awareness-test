package main

import (
	"bufio"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ModScore – wynik jednego modułu w oddanej próbie.
type ModScore struct {
	Code    string `json:"code"`
	Correct int    `json:"correct"`
	Total   int    `json:"total"`
}

// Record – zapisany wynik oddanej próby. Bez treści odpowiedzi: tylko liczby punktów (minimalizacja danych).
type Record struct {
	ID       string     `json:"id"`
	OID      string     `json:"oid"`
	Name     string     `json:"name"`
	UPN      string     `json:"upn"`
	Part     string     `json:"part"`
	Correct  int        `json:"correct"`
	Total    int        `json:"total"`
	Passed   bool       `json:"passed"`
	Critical []string   `json:"critical,omitempty"`
	Modules  []ModScore `json:"modules"`
	Finished time.Time  `json:"finished"`
	CertID   string     `json:"cert_id,omitempty"`
}

// Results – wyniki w pliku JSON Lines (dopisywanie + fsync). Plik jest w całości wczytywany do pamięci:
// przy kilku tysiącach uczestników to kilka MB.
type Results struct {
	mu   sync.RWMutex
	path string
	recs []Record
}

func OpenResults(dir string) (*Results, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	r := &Results{path: filepath.Join(dir, "wyniki.jsonl")}
	f, err := os.Open(r.path)
	if os.IsNotExist(err) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var rec Record
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			return nil, fmt.Errorf("%s, wiersz %d: %w", r.path, line, err)
		}
		r.recs = append(r.recs, rec)
	}
	return r, sc.Err()
}

func (r *Results) Add(rec Record) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(append(b, '\n')); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	r.recs = append(r.recs, rec)
	return nil
}

func (r *Results) All() []Record {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Record, len(r.recs))
	copy(out, r.recs)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Finished.After(out[j].Finished) })
	return out
}

func (r *Results) ByUser(oid string) []Record {
	var out []Record
	for _, rec := range r.All() {
		if rec.OID == oid {
			out = append(out, rec)
		}
	}
	return out
}

func (r *Results) ByCert(id string) *Record {
	id = NormalizeCertID(id)
	if id == "" {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for i := range r.recs {
		if r.recs[i].CertID == id {
			rec := r.recs[i]
			return &rec
		}
	}
	return nil
}

// Purge usuwa wyniki starsze niż podana liczba dni; plik jest przepisywany atomowo (plik tymczasowy + rename).
func (r *Results) Purge(days int, now time.Time) (int, error) {
	if days <= 0 {
		return 0, nil
	}
	limit := now.AddDate(0, 0, -days)
	r.mu.Lock()
	defer r.mu.Unlock()
	var keep []Record
	for _, rec := range r.recs {
		if rec.Finished.After(limit) {
			keep = append(keep, rec)
		}
	}
	removed := len(r.recs) - len(keep)
	if removed == 0 {
		return 0, nil
	}
	tmp := r.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	w := bufio.NewWriter(f)
	for _, rec := range keep {
		b, _ := json.Marshal(rec)
		w.Write(append(b, '\n'))
	}
	if err = w.Flush(); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, r.path)
	}
	if err != nil {
		os.Remove(tmp)
		return 0, err
	}
	r.recs = keep
	return removed, nil
}

// NewCertID – 80 losowych bitów w base32 (A–Z, 2–7: bez cyfr 0, 1, 8, 9 mylonych z literami): XXXX-XXXX-XXXX-XXXX.
func NewCertID() string {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	s := base32.StdEncoding.EncodeToString(b)
	return s[0:4] + "-" + s[4:8] + "-" + s[8:12] + "-" + s[12:16]
}

// NormalizeCertID przyjmuje numer wpisany ręcznie (małe litery, bez myślników) i zwraca postać kanoniczną.
func NormalizeCertID(s string) string {
	s = strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(s))
	if len(s) != 16 {
		return ""
	}
	for _, c := range s {
		if !(c >= 'A' && c <= 'Z' || c >= '2' && c <= '7') {
			return ""
		}
	}
	return s[0:4] + "-" + s[4:8] + "-" + s[8:12] + "-" + s[12:16]
}
