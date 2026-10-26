package main

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// PersonRow – jeden uczestnik w raporcie: ostatnia diagnoza i najlepszy test końcowy.
type PersonRow struct {
	Name, UPN  string
	Diag       *Record
	Final      *Record // najlepszy wynik (przy remisie – najwcześniejszy)
	FinalTries int
	Last       time.Time
}

// ModStat – odsetek poprawnych odpowiedzi w module (wszystkie oddane próby danej części).
type ModStat struct {
	Code, Title  string
	DiagPct      int
	FinalPct     int
	HasDiag      bool
	HasFinal     bool
	CriticalMiss int // liczba prób testu końcowego z błędem w pytaniu KLUCZOWYM tego modułu
}

type Report struct {
	People                                    []PersonRow
	Participants, DiagDone, FinalDone, Passed int
	Modules                                   []ModStat
}

func (a *App) buildReport(q string) Report {
	recs := a.results.All()
	people := map[string]*PersonRow{}
	type acc struct{ c, t int }
	diag, final := map[string]*acc{}, map[string]*acc{}
	crit := map[string]int{}
	for i := range recs {
		rec := &recs[i]
		p := people[rec.OID]
		if p == nil {
			p = &PersonRow{Name: rec.Name, UPN: rec.UPN}
			people[rec.OID] = p
		}
		if rec.Finished.After(p.Last) {
			p.Last, p.Name, p.UPN = rec.Finished, rec.Name, rec.UPN
		}
		target := diag
		if rec.Part == "B" {
			target = final
			p.FinalTries++
			if p.Final == nil || rec.Correct > p.Final.Correct || (rec.Correct == p.Final.Correct && rec.Finished.Before(p.Final.Finished)) {
				p.Final = rec
			}
			for _, c := range rec.Critical {
				crit[c]++
			}
		} else if p.Diag == nil || rec.Finished.After(p.Diag.Finished) {
			p.Diag = rec
		}
		for _, m := range rec.Modules {
			x := target[m.Code]
			if x == nil {
				x = &acc{}
				target[m.Code] = x
			}
			x.c += m.Correct
			x.t += m.Total
		}
	}
	var rep Report
	q = strings.ToLower(strings.TrimSpace(q))
	for _, p := range people {
		rep.Participants++
		if p.Diag != nil {
			rep.DiagDone++
		}
		if p.Final != nil {
			rep.FinalDone++
			if p.Final.Passed {
				rep.Passed++
			}
		}
		if q == "" || strings.Contains(strings.ToLower(p.Name+" "+p.UPN), q) {
			rep.People = append(rep.People, *p)
		}
	}
	sort.Slice(rep.People, func(i, j int) bool {
		return strings.ToLower(rep.People[i].Name) < strings.ToLower(rep.People[j].Name)
	})
	for _, m := range a.bank.Modules {
		st := ModStat{Code: m.Code, Title: m.Title, CriticalMiss: crit[m.Code]}
		if x := diag[m.Code]; x != nil && x.t > 0 {
			st.DiagPct, st.HasDiag = x.c*100/x.t, true
		}
		if x := final[m.Code]; x != nil && x.t > 0 {
			st.FinalPct, st.HasFinal = x.c*100/x.t, true
		}
		rep.Modules = append(rep.Modules, st)
	}
	return rep
}

func (a *App) handleAdmin(w http.ResponseWriter, r *http.Request, s *Session) {
	q := r.URL.Query().Get("szukaj")
	a.audit(r, "raport", "oid", s.OID)
	a.render(w, r, s, http.StatusOK, "admin.html", map[string]any{"Title": "Raport wyników", "R": a.buildReport(q), "Q": q,
		"Retention": a.cfg.Retention})
}

// csvCell chroni przed wstrzyknięciem formuł w arkuszu (=, +, -, @ na początku komórki).
func csvCell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// handleCSV – wszystkie oddane próby; UTF-8 z BOM i średnik jako separator (Excel w polskich ustawieniach).
// Wyniki modułów jako „3 z 3”, bo „3/3” Excel zamienia na datę.
func (a *App) handleCSV(w http.ResponseWriter, r *http.Request, s *Session) {
	a.audit(r, "eksport_csv", "oid", s.OID)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="wyniki_%s.csv"`, time.Now().In(warsaw).Format("2006-01-02")))
	w.Write([]byte("\xEF\xBB\xBF"))
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	head := []string{"data_oddania", "imie_nazwisko", "login", "czesc", "wynik", "maks", "zaliczone", "bledy_KLUCZOWE", "numer_zaswiadczenia"}
	for _, m := range a.bank.Modules {
		head = append(head, m.Code)
	}
	cw.Write(head)
	for _, rec := range a.results.All() {
		part := "diagnoza"
		passed := ""
		if rec.Part == "B" {
			part = "test_koncowy"
			passed = "nie"
			if rec.Passed {
				passed = "tak"
			}
		}
		row := []string{rec.Finished.In(warsaw).Format("2006-01-02 15:04"), csvCell(rec.Name), csvCell(rec.UPN), part,
			strconv.Itoa(rec.Correct), strconv.Itoa(rec.Total), passed, strings.Join(rec.Critical, ","), rec.CertID}
		byCode := map[string]ModScore{}
		for _, m := range rec.Modules {
			byCode[m.Code] = m
		}
		for _, m := range a.bank.Modules {
			if ms, ok := byCode[m.Code]; ok {
				row = append(row, fmt.Sprintf("%d z %d", ms.Correct, ms.Total))
			} else {
				row = append(row, "")
			}
		}
		cw.Write(row)
	}
	cw.Flush()
}
