package main

import (
	"encoding/json"
	"fmt"
)

// Question – pytanie z kluczem (dane osadzone w programie, niewysyłane do przeglądarki przed oceną).
type Question struct {
	ID          string   `json:"id"`
	Module      string   `json:"module"`
	Kind        string   `json:"kind"`
	Critical    bool     `json:"critical"`
	Stem        string   `json:"stem"`
	Options     []string `json:"options"`
	Key         string   `json:"key"`
	Why         []string `json:"why"`
	ReviewSlide int      `json:"review_slide"`
	ReviewTitle string   `json:"review_title"`
}

type Part struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Minutes   int        `json:"minutes"`
	When      string     `json:"when"`
	Questions []Question `json:"questions"`
}

type Module struct {
	Code       string `json:"code"`
	Title      string `json:"title"`
	FirstSlide int    `json:"first_slide"`
	LastSlide  int    `json:"last_slide"`
	// cel i zagadnienia modułu – zakres na zaświadczeniu w aplikacji webowej
	Goal  string   `json:"goal,omitempty"`
	Items []string `json:"items,omitempty"`
}

type Bank struct {
	Version   string   `json:"version"`
	Funding   string   `json:"funding"`
	Threshold int      `json:"threshold"`
	Modules   []Module `json:"modules"`
	Parts     []Part   `json:"parts"`
}

// PublicQuestion – to, co widzi uczestnik przed oddaniem odpowiedzi (bez klucza i uzasadnień).
type PublicQuestion struct {
	ID, Module, Kind, Stem string
	Critical               bool
	Options                []string
}

type PublicPart struct {
	ID, Title, When string
	Minutes         int
	Questions       []PublicQuestion
}

func LoadBank(raw []byte) (*Bank, error) {
	var b Bank
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, err
	}
	mods := map[string]bool{}
	for _, m := range b.Modules {
		mods[m.Code] = true
	}
	for _, p := range b.Parts {
		for _, q := range p.Questions {
			if len(q.Options) != 4 || len(q.Why) != 4 {
				return nil, fmt.Errorf("pytanie %s: wymagane 4 odpowiedzi i 4 uzasadnienia", q.ID)
			}
			if len(q.Key) != 1 || q.Key[0] < 'A' || q.Key[0] > 'D' {
				return nil, fmt.Errorf("pytanie %s: błędny klucz %q", q.ID, q.Key)
			}
			if !mods[q.Module] {
				return nil, fmt.Errorf("pytanie %s: nieznany moduł %s", q.ID, q.Module)
			}
		}
	}
	return &b, nil
}

func (b *Bank) Part(id string) *Part {
	for i := range b.Parts {
		if b.Parts[i].ID == id {
			return &b.Parts[i]
		}
	}
	return nil
}

func (b *Bank) Module(code string) Module {
	for _, m := range b.Modules {
		if m.Code == code {
			return m
		}
	}
	return Module{Code: code}
}

func (p *Part) Public() PublicPart {
	pp := PublicPart{ID: p.ID, Title: p.Title, When: p.When, Minutes: p.Minutes}
	for _, q := range p.Questions {
		pp.Questions = append(pp.Questions, PublicQuestion{ID: q.ID, Module: q.Module, Kind: q.Kind, Stem: q.Stem, Critical: q.Critical, Options: q.Options})
	}
	return pp
}

type OptionView struct {
	Letter, Text, Why string
	IsKey, IsGiven    bool
}

type ItemResult struct {
	Q        Question
	Given    string // litera lub "" (brak odpowiedzi)
	Correct  bool
	Options  []OptionView
	ModTitle string
}

type ModuleRow struct {
	Module   Module
	Correct  int
	Total    int
	Review   bool // moduł do powtórki (błąd w części A lub B)
	Critical bool // błąd w pytaniu KLUCZOWYM
}

type Result struct {
	Part        PublicPart
	IsFinal     bool
	Total       int
	Correct     int
	Percent     int
	Threshold   int
	MeetsTarget bool
	Critical    []Module
	Rows        []ModuleRow
	Items       []ItemResult
	Unanswered  int
}

// Score ocenia odpowiedzi: answers[ID] = indeks 0–3; brak wpisu = brak odpowiedzi (liczony jako błędny).
func (b *Bank) Score(p *Part, answers map[string]int) Result {
	r := Result{Part: p.Public(), IsFinal: p.ID == "B", Total: len(p.Questions), Threshold: b.Threshold}
	rows := map[string]*ModuleRow{}
	var order []string
	critSeen := map[string]bool{}
	for _, q := range p.Questions {
		row, ok := rows[q.Module]
		if !ok {
			row = &ModuleRow{Module: b.Module(q.Module)}
			rows[q.Module] = row
			order = append(order, q.Module)
		}
		row.Total++
		item := ItemResult{Q: q, ModTitle: b.Module(q.Module).Title}
		given, has := answers[q.ID]
		if has && given >= 0 && given < 4 {
			item.Given = string(rune('A' + given))
		} else {
			r.Unanswered++
		}
		item.Correct = item.Given == q.Key
		for i, o := range q.Options {
			L := string(rune('A' + i))
			item.Options = append(item.Options, OptionView{Letter: L, Text: o, Why: q.Why[i], IsKey: L == q.Key, IsGiven: L == item.Given})
		}
		if item.Correct {
			r.Correct++
			row.Correct++
		} else {
			row.Review = true
			if q.Critical {
				row.Critical = true
				if !critSeen[q.Module] {
					critSeen[q.Module] = true
					r.Critical = append(r.Critical, b.Module(q.Module))
				}
			}
		}
		r.Items = append(r.Items, item)
	}
	for _, m := range order {
		r.Rows = append(r.Rows, *rows[m])
	}
	if r.Total > 0 {
		r.Percent = r.Correct * 100 / r.Total
	}
	r.MeetsTarget = r.IsFinal && r.Correct >= b.Threshold
	return r
}
