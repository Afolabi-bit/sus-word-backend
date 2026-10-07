package game

import (
	"testing"

	"sus-word-backend/internal/domain"
)

func TestNewWordSelector_DefaultCatalog(t *testing.T) {
	ws := NewWordSelector(nil)
	if ws.TotalWords() == 0 {
		t.Fatalf("expected non-empty default word list")
	}

	cats := ws.Categories()
	if len(cats) == 0 {
		t.Fatalf("expected categories to be populated")
	}
}

func TestPickWord_AvoidsHistory(t *testing.T) {
	customWords := []domain.WordEntry{
		{Word: "Apple", Category: "Food"},
		{Word: "Banana", Category: "Food"},
		{Word: "Cherry", Category: "Food"},
	}

	ws := NewWordSelector(customWords)
	history := make(map[string]bool)

	// Pick 1
	w1, err := ws.PickWord(history)
	if err != nil {
		t.Fatalf("unexpected error picking word 1: %v", err)
	}
	if !history[w1.Word] {
		t.Errorf("expected w1 to be in history")
	}

	// Pick 2
	w2, err := ws.PickWord(history)
	if err != nil {
		t.Fatalf("unexpected error picking word 2: %v", err)
	}
	if w2.Word == w1.Word {
		t.Errorf("expected w2 (%s) to be different from w1 (%s)", w2.Word, w1.Word)
	}

	// Pick 3
	w3, err := ws.PickWord(history)
	if err != nil {
		t.Fatalf("unexpected error picking word 3: %v", err)
	}
	if w3.Word == w1.Word || w3.Word == w2.Word {
		t.Errorf("expected w3 to be distinct from prior picks")
	}
}

func TestPickWord_RecycleWhenExhausted(t *testing.T) {
	customWords := []domain.WordEntry{
		{Word: "Tiger", Category: "Animals"},
		{Word: "Lion", Category: "Animals"},
	}

	ws := NewWordSelector(customWords)
	history := map[string]bool{
		"Tiger": true,
		"Lion":  true,
	}

	// Should recycle history and still successfully pick
	w, err := ws.PickWord(history)
	if err != nil {
		t.Fatalf("unexpected error when history full: %v", err)
	}
	if w.Word != "Tiger" && w.Word != "Lion" {
		t.Errorf("unexpected word returned: %s", w.Word)
	}
}

func TestPickWord_EmptyCatalog(t *testing.T) {
	ws := &WordSelector{words: []domain.WordEntry{}}
	history := make(map[string]bool)

	_, err := ws.PickWord(history)
	if err != ErrNoWordsAvailable {
		t.Errorf("expected ErrNoWordsAvailable, got %v", err)
	}
}
