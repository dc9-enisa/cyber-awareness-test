package main

import (
	"sync"
	"time"
)

// Attempt – próba w toku, przypisana do zalogowanego uczestnika. Odpowiedzi są tylko w pamięci;
// po oddaniu do pliku wyników trafia sam wynik (Record). Restart serwera przerywa próby w toku.
type Attempt struct {
	ID      string
	Owner   string // oid z Entra
	PartID  string
	Created time.Time

	mu      sync.Mutex
	answers map[string]string // ID pytania -> litera
	result  *Result
	record  *Record
}

func (a *Attempt) Set(qid, letter string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.answers[qid] = letter
}

func (a *Attempt) Answers() map[string]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[string]string, len(a.answers))
	for k, v := range a.answers {
		out[k] = v
	}
	return out
}

func (a *Attempt) AnswerIndexes() map[string]int {
	out := map[string]int{}
	for k, v := range a.Answers() {
		out[k] = int(v[0] - 'A')
	}
	return out
}

// Finish ocenia próbę i zapisuje wynik tylko raz; ponowne oddanie (np. podwójne kliknięcie) niczego nie zmienia.
// save wywoływane jest pod blokadą, więc wynik nie zostanie zapisany dwa razy.
func (a *Attempt) Finish(score func() Result, save func(Result) (*Record, error)) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.result != nil {
		return nil
	}
	r := score()
	rec, err := save(r)
	if err != nil {
		return err
	}
	a.result, a.record = &r, rec
	return nil
}

func (a *Attempt) Result() *Result {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.result
}

func (a *Attempt) Record() *Record {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.record
}

// Store – ograniczony magazyn prób w toku; próby starsze niż maxAge i nadmiarowe są usuwane.
type Store struct {
	mu     sync.Mutex
	limit  int
	maxAge time.Duration
	items  map[string]*Attempt
	order  []string
}

func NewStore(limit int, maxAge time.Duration) *Store {
	return &Store{limit: limit, maxAge: maxAge, items: map[string]*Attempt{}}
}

func (s *Store) New(owner, partID string) *Attempt {
	a := &Attempt{ID: randomToken()[:16], Owner: owner, PartID: partID, Created: time.Now(), answers: map[string]string{}}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[a.ID] = a
	s.order = append(s.order, a.ID)
	for len(s.order) > s.limit || (len(s.order) > 0 && time.Since(s.items[s.order[0]].Created) > s.maxAge) {
		delete(s.items, s.order[0])
		s.order = s.order[1:]
	}
	return a
}

// Get zwraca próbę tylko jej właścicielowi.
func (s *Store) Get(id, owner string) *Attempt {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.items[id]
	if a == nil || a.Owner != owner || time.Since(a.Created) > s.maxAge {
		return nil
	}
	return a
}
