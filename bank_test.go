package main

import (
	"testing"
	"time"
)

func loadT(t *testing.T) *Bank {
	b, err := LoadBank(questionsJSON)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Testy nie zakładają liczby pytań: ten sam kod działa z pełnym bankiem kursu i z pytaniami przykładowymi.
func TestBankShape(t *testing.T) {
	b := loadT(t)
	a, f := b.Part("A"), b.Part("B")
	if a == nil || f == nil || len(a.Questions) == 0 || len(f.Questions) == 0 {
		t.Fatal("wymagane części A (diagnoza) i B (test końcowy)")
	}
	if b.Threshold < 1 || b.Threshold > len(f.Questions) {
		t.Fatalf("próg %d poza zakresem 1–%d", b.Threshold, len(f.Questions))
	}
	seen := map[string]bool{}
	for _, p := range b.Parts {
		for _, q := range p.Questions {
			if seen[q.ID] {
				t.Fatalf("powtórzony identyfikator %s", q.ID)
			}
			seen[q.ID] = true
		}
	}
}

func keyAnswers(p *Part) map[string]int {
	ans := map[string]int{}
	for _, q := range p.Questions {
		ans[q.ID] = int(q.Key[0] - 'A')
	}
	return ans
}

func TestAllCorrect(t *testing.T) {
	b := loadT(t)
	p := b.Part("B")
	r := b.Score(p, keyAnswers(p))
	if r.Correct != len(p.Questions) || !r.MeetsTarget || len(r.Critical) != 0 || r.Percent != 100 {
		t.Fatalf("wynik %+v", r.Correct)
	}
}

func TestCriticalAndMissing(t *testing.T) {
	b := loadT(t)
	p := b.Part("B")
	var crit *Question
	for i := range p.Questions {
		if p.Questions[i].Critical {
			crit = &p.Questions[i]
			break
		}
	}
	if crit == nil || len(p.Questions) < 2 {
		t.Skip("brak pytania KLUCZOWEGO w części B")
	}
	ans := keyAnswers(p)
	ans[crit.ID] = (ans[crit.ID] + 1) % 4 // KLUCZOWE błędnie
	for _, q := range p.Questions {
		if q.ID != crit.ID {
			delete(ans, q.ID) // jedno pytanie bez odpowiedzi
			break
		}
	}
	r := b.Score(p, ans)
	if r.Correct != len(p.Questions)-2 || r.Unanswered != 1 {
		t.Fatalf("correct=%d unanswered=%d", r.Correct, r.Unanswered)
	}
	if len(r.Critical) != 1 || r.Critical[0].Code != crit.Module {
		t.Fatalf("krytyczne %+v", r.Critical)
	}
}

func TestThreshold(t *testing.T) {
	b := loadT(t)
	p := b.Part("B")
	score := func(correct int) Result {
		ans := keyAnswers(p)
		for i, q := range p.Questions {
			if i >= correct {
				ans[q.ID] = (ans[q.ID] + 1) % 4
			}
		}
		return b.Score(p, ans)
	}
	if r := score(b.Threshold - 1); r.Correct != b.Threshold-1 || r.MeetsTarget {
		t.Fatalf("%d/%d nie powinno spełniać progu", r.Correct, r.Total)
	}
	if r := score(b.Threshold); !r.MeetsTarget {
		t.Fatalf("%d/%d powinno spełniać próg", r.Correct, r.Total)
	}
}

func TestPublicHidesKey(t *testing.T) {
	b := loadT(t)
	pp := b.Part("A").Public()
	for _, q := range pp.Questions {
		_ = q // PublicQuestion nie ma pól Key ani Why – kontrola na poziomie typu
	}
	if len(pp.Questions) != len(b.Part("A").Questions) {
		t.Fatal("A")
	}
}

func TestStoreOwnerLimitAndFinishOnce(t *testing.T) {
	s := NewStore(2, time.Hour)
	a := s.New("u1", "A")
	if s.Get(a.ID, "u2") != nil {
		t.Fatal("próba widoczna dla innego użytkownika")
	}
	s.New("u1", "A")
	s.New("u1", "B")
	if s.Get(a.ID, "u1") != nil {
		t.Fatal("najstarsza próba powinna zostać usunięta")
	}
	b := s.New("u1", "A")
	calls, saves := 0, 0
	save := func(Result) (*Record, error) { saves++; return &Record{}, nil }
	b.Finish(func() Result { calls++; return Result{Correct: 1} }, save)
	b.Finish(func() Result { calls++; return Result{Correct: 2} }, save)
	if calls != 1 || saves != 1 || b.Result().Correct != 1 {
		t.Fatal("wynik ma być ustalony i zapisany raz")
	}
}
