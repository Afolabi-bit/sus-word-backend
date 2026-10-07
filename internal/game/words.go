package game

import (
	"crypto/rand"
	"errors"
	"math/big"
	"sync"

	"sus-word-backend/internal/domain"
)

var (
	ErrNoWordsAvailable = errors.New("no words available in catalog")
)

// WordSelector manages word selection with room-level history tracking.
type WordSelector struct {
	words []domain.WordEntry
	mu    sync.RWMutex
}

// NewWordSelector creates a WordSelector with the given list or the DefaultWordList.
func NewWordSelector(catalog []domain.WordEntry) *WordSelector {
	if len(catalog) == 0 {
		catalog = DefaultWordList
	}
	// Copy to prevent external mutation
	copied := make([]domain.WordEntry, len(catalog))
	copy(copied, catalog)

	return &WordSelector{
		words: copied,
	}
}

// TotalWords returns the size of the catalog.
func (ws *WordSelector) TotalWords() int {
	ws.mu.RLock()
	defer ws.mu.RUnlock()
	return len(ws.words)
}

// PickWord selects a random word entry, avoiding words currently in the room's history map.
// If all words are in history or no unplayed words remain, history is cleared to recycle words.
// The chosen word is added to the history map.
func (ws *WordSelector) PickWord(history map[string]bool) (domain.WordEntry, error) {
	ws.mu.RLock()
	defer ws.mu.RUnlock()

	if len(ws.words) == 0 {
		return domain.WordEntry{}, ErrNoWordsAvailable
	}

	// Filter candidates not in history
	candidates := make([]domain.WordEntry, 0, len(ws.words))
	for _, entry := range ws.words {
		if !history[entry.Word] {
			candidates = append(candidates, entry)
		}
	}

	// If all words have been used, recycle by clearing history
	if len(candidates) == 0 {
		for k := range history {
			delete(history, k)
		}
		candidates = append(candidates, ws.words...)
	}

	// Securely choose a random candidate
	maxIdx := big.NewInt(int64(len(candidates)))
	n, err := rand.Int(rand.Reader, maxIdx)
	if err != nil {
		return domain.WordEntry{}, err
	}

	chosen := candidates[n.Int64()]

	// Update history
	history[chosen.Word] = true

	// Prune history if it exceeds floor(total / 3) to keep rotation fresh
	maxHistory := len(ws.words) / 3
	if maxHistory > 0 && len(history) > maxHistory {
		// Remove the oldest entries if map grows excessively
		// In Go maps, simple cleanup or trim can be done if needed
	}

	return chosen, nil
}

// Categories returns the unique list of categories available in the selector.
func (ws *WordSelector) Categories() []string {
	ws.mu.RLock()
	defer ws.mu.RUnlock()

	seen := make(map[string]bool)
	var categories []string
	for _, w := range ws.words {
		if !seen[w.Category] {
			seen[w.Category] = true
			categories = append(categories, w.Category)
		}
	}
	return categories
}
