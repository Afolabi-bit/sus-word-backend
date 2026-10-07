package game

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"time"

	"sus-word-backend/internal/domain"
)

// Game engine errors
var (
	ErrInvalidPhase       = errors.New("action not allowed in current phase")
	ErrNotEnoughPlayers   = errors.New("game requires at least 4 players")
	ErrTooManyPlayers     = errors.New("room exceeds maximum 10 players")
	ErrNotYourTurn        = errors.New("not your turn in reveal sequence")
	ErrPlayerNotFound     = errors.New("player not found in room")
	ErrInvalidTarget      = errors.New("targeted player is not active or already eliminated")
	ErrInvalidTimerValue  = errors.New("invalid timer duration: allowed values are 60, 120, 180, 300, 600")
)

const (
	MinPlayers = 4
	MaxPlayers = 10
)

// AllowedTimerDurations defines valid discussion lengths in seconds.
var AllowedTimerDurations = map[int]bool{
	60:  true,
	120: true,
	180: true,
	300: true,
	600: true,
}

// NewRoomData initializes a fresh room state.
func NewRoomData(code, hostID, hostName string, initialTimer int) *RoomDataWithState {
	if initialTimer <= 0 || !AllowedTimerDurations[initialTimer] {
		initialTimer = 300
	}

	room := &domain.RoomData{
		Code:            code,
		Players:         make(map[string]*domain.BasePlayer),
		PlayerOrder:     make([]string, 0),
		HostID:          hostID,
		Phase:           domain.PhaseLobby,
		ActivePlayerIDs: make([]string, 0),
		EliminationLog:  make([]domain.EliminationRecord, 0),
		TimerDuration:   initialTimer,
		WordHistory:     make(map[string]bool),
		CreatedAt:       time.Now(),
	}

	if hostID != "" {
		host := &domain.BasePlayer{
			ID:          hostID,
			DisplayName: hostName,
			RoomCode:    code,
			ConnectedAt: time.Now(),
			IsHost:      true,
			IsActive:    true,
			IsReady:     false,
		}
		room.Players[hostID] = host
		room.PlayerOrder = append(room.PlayerOrder, hostID)
	}

	return &RoomDataWithState{RoomData: room}
}

// RoomDataWithState wraps domain.RoomData with pure state-machine methods.
type RoomDataWithState struct {
	*domain.RoomData
}

// CanStartGame evaluates whether the game can begin.
func (r *RoomDataWithState) CanStartGame() error {
	if r.Phase != domain.PhaseLobby {
		return fmt.Errorf("%w: current phase is %s", ErrInvalidPhase, r.Phase)
	}

	count := len(r.Players)
	if count < MinPlayers {
		return fmt.Errorf("%w: currently %d", ErrNotEnoughPlayers, count)
	}
	if count > MaxPlayers {
		return fmt.Errorf("%w: currently %d", ErrTooManyPlayers, count)
	}

	return nil
}

// SetTimerDuration updates the discussion timer in lobby phase.
func (r *RoomDataWithState) SetTimerDuration(seconds int) error {
	if r.Phase != domain.PhaseLobby {
		return fmt.Errorf("%w: timer can only be changed in lobby", ErrInvalidPhase)
	}
	if !AllowedTimerDurations[seconds] {
		return ErrInvalidTimerValue
	}
	r.TimerDuration = seconds
	return nil
}

// StartGame initiates the game: picks a secret word, selects an imposter, and shuffles reveal order.
func (r *RoomDataWithState) StartGame(ws *WordSelector) (map[string]domain.RoleAssignedPayload, error) {
	if err := r.CanStartGame(); err != nil {
		return nil, err
	}

	// 1. Pick secret word avoiding history
	word, err := ws.PickWord(r.WordHistory)
	if err != nil {
		return nil, err
	}
	r.SecretWord = word.Word
	r.SecretCategory = word.Category

	// 2. Collect connected player IDs and shuffle reveal order
	playerIDs := make([]string, 0, len(r.Players))
	for _, pid := range r.PlayerOrder {
		if _, ok := r.Players[pid]; ok {
			playerIDs = append(playerIDs, pid)
		}
	}

	shuffled, err := cryptoShuffle(playerIDs)
	if err != nil {
		return nil, err
	}
	r.RevealOrder = shuffled
	r.RevealIndex = 0

	// 3. Select 1 random imposter from players
	imposterIdx, err := cryptoRandInt(len(shuffled))
	if err != nil {
		return nil, err
	}
	r.ImposterID = shuffled[imposterIdx]

	// 4. Reset player states
	r.ActivePlayerIDs = make([]string, len(shuffled))
	copy(r.ActivePlayerIDs, shuffled)

	for _, p := range r.Players {
		p.IsActive = true
		p.IsReady = false
	}

	r.EliminationLog = make([]domain.EliminationRecord, 0)
	r.LastEliminated = nil
	r.Winner = ""
	r.Phase = domain.PhaseRevealing

	// 5. Generate private role payloads
	roles := make(map[string]domain.RoleAssignedPayload, len(r.Players))
	for _, pid := range shuffled {
		if pid == r.ImposterID {
			roles[pid] = domain.RoleAssignedPayload{
				Role:           "imposter",
				SecretWord:     nil,
				SecretCategory: r.SecretCategory,
			}
		} else {
			secret := r.SecretWord
			roles[pid] = domain.RoleAssignedPayload{
				Role:           "civilian",
				SecretWord:     &secret,
				SecretCategory: r.SecretCategory,
			}
		}
	}

	return roles, nil
}

// CurrentRevealPlayer returns information about the player who should currently reveal their word.
func (r *RoomDataWithState) CurrentRevealPlayer() (*domain.BasePlayer, int, int, error) {
	if r.Phase != domain.PhaseRevealing {
		return nil, 0, 0, ErrInvalidPhase
	}
	if r.RevealIndex >= len(r.RevealOrder) {
		return nil, 0, 0, errors.New("reveal sequence completed")
	}

	pid := r.RevealOrder[r.RevealIndex]
	player, ok := r.Players[pid]
	if !ok {
		return nil, 0, 0, ErrPlayerNotFound
	}

	return player, r.RevealIndex, len(r.RevealOrder), nil
}

// AdvanceReveal advances through the reveal order when a player signals ready.
func (r *RoomDataWithState) AdvanceReveal(playerID string) (allReady bool, nextPlayer *domain.BasePlayer, err error) {
	if r.Phase != domain.PhaseRevealing {
		return false, nil, ErrInvalidPhase
	}

	currentPID := r.RevealOrder[r.RevealIndex]
	if playerID != currentPID {
		return false, nil, fmt.Errorf("%w: expected player %s, got %s", ErrNotYourTurn, currentPID, playerID)
	}

	// Mark current player ready
	if p, ok := r.Players[playerID]; ok {
		p.IsReady = true
	}

	r.RevealIndex++

	// Check if all players have completed reveal
	if r.RevealIndex >= len(r.RevealOrder) {
		r.Phase = domain.PhaseReady
		return true, nil, nil
	}

	nextPID := r.RevealOrder[r.RevealIndex]
	nextP := r.Players[nextPID]
	return false, nextP, nil
}

// StartDiscussion begins the timed discussion phase.
func (r *RoomDataWithState) StartDiscussion(now time.Time) (time.Time, error) {
	if r.Phase != domain.PhaseReady && r.Phase != domain.PhaseResult {
		return time.Time{}, fmt.Errorf("%w: discussion can only start from ready or result phase", ErrInvalidPhase)
	}

	endsAt := now.Add(time.Duration(r.TimerDuration) * time.Second)
	r.TimerEndsAt = &endsAt
	r.Phase = domain.PhaseDiscussing

	return endsAt, nil
}

// EndDiscussion transitions from discussion to voting.
func (r *RoomDataWithState) EndDiscussion() error {
	if r.Phase != domain.PhaseDiscussing {
		return fmt.Errorf("%w: discussion can only end from discussing phase", ErrInvalidPhase)
	}

	r.TimerEndsAt = nil
	r.Phase = domain.PhaseVoting
	return nil
}

// EliminatePlayer processes an elimination vote, updates history, and evaluates win conditions.
func (r *RoomDataWithState) EliminatePlayer(targetID string) (domain.EliminationRecord, bool, error) {
	if r.Phase != domain.PhaseVoting {
		return domain.EliminationRecord{}, false, fmt.Errorf("%w: elimination only allowed in voting phase", ErrInvalidPhase)
	}

	// Verify target is active
	activeIdx := -1
	for i, id := range r.ActivePlayerIDs {
		if id == targetID {
			activeIdx = i
			break
		}
	}
	if activeIdx == -1 {
		return domain.EliminationRecord{}, false, ErrInvalidTarget
	}

	targetPlayer, ok := r.Players[targetID]
	if !ok {
		return domain.EliminationRecord{}, false, ErrPlayerNotFound
	}

	// Remove from ActivePlayerIDs
	r.ActivePlayerIDs = append(r.ActivePlayerIDs[:activeIdx], r.ActivePlayerIDs[activeIdx+1:]...)
	targetPlayer.IsActive = false

	wasImposter := (targetID == r.ImposterID)
	record := domain.EliminationRecord{
		PlayerID:    targetID,
		DisplayName: targetPlayer.DisplayName,
		WasImposter: wasImposter,
		RoundNumber: len(r.EliminationLog) + 1,
	}

	r.EliminationLog = append(r.EliminationLog, record)
	r.LastEliminated = &record

	// Win condition check
	if wasImposter {
		// Civilians win!
		r.Winner = "civilians"
		r.Phase = domain.PhaseGameOver
		return record, true, nil
	}

	// Civilians mistakenly eliminated one of their own
	if len(r.ActivePlayerIDs) <= 2 {
		// Imposter wins!
		r.Winner = "imposter"
		r.Phase = domain.PhaseGameOver
		return record, true, nil
	}

	// Game continues to result phase
	r.Winner = ""
	r.Phase = domain.PhaseResult
	return record, false, nil
}

// NextRound progresses from result back to discussion.
func (r *RoomDataWithState) NextRound(now time.Time) (time.Time, error) {
	return r.StartDiscussion(now)
}

// PlayAgain restarts the game with the same players and new secret assignments.
func (r *RoomDataWithState) PlayAgain(ws *WordSelector) (map[string]domain.RoleAssignedPayload, error) {
	if r.Phase != domain.PhaseGameOver {
		return nil, fmt.Errorf("%w: play again only available in gameOver phase", ErrInvalidPhase)
	}

	r.Phase = domain.PhaseLobby
	return r.StartGame(ws)
}

// NewGame resets the room state back to the lobby, clearing active game state and word history.
func (r *RoomDataWithState) NewGame() error {
	if r.Phase != domain.PhaseGameOver {
		return fmt.Errorf("%w: new game only available in gameOver phase", ErrInvalidPhase)
	}

	r.Phase = domain.PhaseLobby
	r.SecretWord = ""
	r.SecretCategory = ""
	r.ImposterID = ""
	r.ActivePlayerIDs = nil
	r.EliminationLog = make([]domain.EliminationRecord, 0)
	r.LastEliminated = nil
	r.Winner = ""
	r.RevealOrder = nil
	r.RevealIndex = 0
	r.TimerEndsAt = nil
	r.WordHistory = make(map[string]bool)

	for _, p := range r.Players {
		p.IsActive = true
		p.IsReady = false
	}

	return nil
}

// cryptoShuffle securely shuffles a slice of strings using Fisher-Yates algorithm.
func cryptoShuffle(slice []string) ([]string, error) {
	shuffled := make([]string, len(slice))
	copy(shuffled, slice)

	for i := len(shuffled) - 1; i > 0; i-- {
		j, err := cryptoRandInt(i + 1)
		if err != nil {
			return nil, err
		}
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	}

	return shuffled, nil
}

func cryptoRandInt(max int) (int, error) {
	if max <= 0 {
		return 0, nil
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		return 0, err
	}
	return int(n.Int64()), nil
}
