package game

import (
	"fmt"
	"testing"
	"time"

	"sus-word-backend/internal/domain"
)

func createTestRoom(playerCount int) (*RoomDataWithState, []string) {
	room := NewRoomData("XK92PL", "host-id", "HostPlayer", 300)
	playerIDs := []string{"host-id"}

	for i := 2; i <= playerCount; i++ {
		pid := fmt.Sprintf("player-%d", i)
		pname := fmt.Sprintf("Player %d", i)
		room.Players[pid] = &domain.BasePlayer{
			ID:          pid,
			DisplayName: pname,
			RoomCode:    "XK92PL",
			ConnectedAt: time.Now(),
			IsHost:      false,
			IsActive:    true,
			IsReady:     false,
		}
		room.PlayerOrder = append(room.PlayerOrder, pid)
		playerIDs = append(playerIDs, pid)
	}

	return room, playerIDs
}

func TestRoomData_Initialization(t *testing.T) {
	room, _ := createTestRoom(1)

	if room.Code != "XK92PL" {
		t.Errorf("expected room code XK92PL, got %s", room.Code)
	}
	if room.Phase != domain.PhaseLobby {
		t.Errorf("expected phase lobby, got %s", room.Phase)
	}
	if room.TimerDuration != 300 {
		t.Errorf("expected timer 300, got %d", room.TimerDuration)
	}
	if len(room.Players) != 1 {
		t.Errorf("expected 1 player (host), got %d", len(room.Players))
	}
}

func TestCanStartGame(t *testing.T) {
	ws := NewWordSelector(nil)

	// Case 1: Less than 4 players
	room3, _ := createTestRoom(3)
	if err := room3.CanStartGame(); err == nil {
		t.Errorf("expected error with 3 players, got nil")
	}

	// Case 2: Exactly 4 players
	room4, _ := createTestRoom(4)
	if err := room4.CanStartGame(); err != nil {
		t.Errorf("expected 4 players to be valid, got %v", err)
	}

	// Case 3: More than 10 players
	room11, _ := createTestRoom(11)
	if err := room11.CanStartGame(); err == nil {
		t.Errorf("expected error with 11 players, got nil")
	}

	// Case 4: Cannot start if not in lobby
	_, _ = room4.StartGame(ws)
	if err := room4.CanStartGame(); err == nil {
		t.Errorf("expected error starting game when phase is not lobby")
	}
}

func TestSetTimerDuration(t *testing.T) {
	room, _ := createTestRoom(4)

	// Valid values
	for _, valid := range []int{60, 120, 180, 300, 600} {
		if err := room.SetTimerDuration(valid); err != nil {
			t.Errorf("expected timer %d to be valid, got %v", valid, err)
		}
		if room.TimerDuration != valid {
			t.Errorf("expected room timer to be %d, got %d", valid, room.TimerDuration)
		}
	}

	// Invalid value
	if err := room.SetTimerDuration(45); err == nil {
		t.Errorf("expected error for timer duration 45")
	}
}

func TestStartGame_RoleAssignment(t *testing.T) {
	room, _ := createTestRoom(5)
	ws := NewWordSelector(nil)

	roles, err := room.StartGame(ws)
	if err != nil {
		t.Fatalf("failed to start game: %v", err)
	}

	if room.Phase != domain.PhaseRevealing {
		t.Errorf("expected phase revealing, got %s", room.Phase)
	}
	if len(room.RevealOrder) != 5 {
		t.Errorf("expected reveal order length 5, got %d", len(room.RevealOrder))
	}

	imposterCount := 0
	civilianCount := 0

	for pid, rolePayload := range roles {
		if rolePayload.Role == "imposter" {
			imposterCount++
			if rolePayload.SecretWord != nil {
				t.Errorf("imposter secretWord must be nil")
			}
			if rolePayload.SecretCategory != room.SecretCategory {
				t.Errorf("imposter must receive secret category")
			}
			if pid != room.ImposterID {
				t.Errorf("imposter role player ID mismatch")
			}
		} else if rolePayload.Role == "civilian" {
			civilianCount++
			if rolePayload.SecretWord == nil || *rolePayload.SecretWord != room.SecretWord {
				t.Errorf("civilian must receive valid secret word")
			}
			if rolePayload.SecretCategory != room.SecretCategory {
				t.Errorf("civilian must receive valid secret category")
			}
		} else {
			t.Errorf("unknown role assigned: %s", rolePayload.Role)
		}
	}

	if imposterCount != 1 {
		t.Errorf("expected exactly 1 imposter, got %d", imposterCount)
	}
	if civilianCount != 4 {
		t.Errorf("expected 4 civilians, got %d", civilianCount)
	}
}

func TestAdvanceReveal_Sequence(t *testing.T) {
	room, _ := createTestRoom(4)
	ws := NewWordSelector(nil)
	_, _ = room.StartGame(ws)

	// Player acting out of order must be rejected
	wrongPlayerID := room.RevealOrder[1]
	_, _, err := room.AdvanceReveal(wrongPlayerID)
	if err == nil {
		t.Errorf("expected error when wrong player advances reveal")
	}

	// Step through reveal order sequentially
	for i := 0; i < len(room.RevealOrder)-1; i++ {
		currentPID := room.RevealOrder[i]
		allReady, nextPlayer, err := room.AdvanceReveal(currentPID)
		if err != nil {
			t.Fatalf("unexpected error at reveal index %d: %v", i, err)
		}
		if allReady {
			t.Fatalf("expected allReady to be false before last player")
		}
		if nextPlayer.ID != room.RevealOrder[i+1] {
			t.Errorf("expected next player %s, got %s", room.RevealOrder[i+1], nextPlayer.ID)
		}
	}

	// Last player signals ready
	lastPID := room.RevealOrder[len(room.RevealOrder)-1]
	allReady, nextPlayer, err := room.AdvanceReveal(lastPID)
	if err != nil {
		t.Fatalf("unexpected error on last reveal: %v", err)
	}
	if !allReady {
		t.Errorf("expected allReady to be true on last player")
	}
	if nextPlayer != nil {
		t.Errorf("expected nextPlayer to be nil when all done")
	}
	if room.Phase != domain.PhaseReady {
		t.Errorf("expected phase ready, got %s", room.Phase)
	}
}

func TestDiscussion_And_Voting_Flow(t *testing.T) {
	room, _ := createTestRoom(4)
	ws := NewWordSelector(nil)
	_, _ = room.StartGame(ws)

	// Complete reveal
	for _, pid := range room.RevealOrder {
		_, _, _ = room.AdvanceReveal(pid)
	}

	// Start discussion
	now := time.Now()
	endsAt, err := room.StartDiscussion(now)
	if err != nil {
		t.Fatalf("unexpected error starting discussion: %v", err)
	}
	if room.Phase != domain.PhaseDiscussing {
		t.Errorf("expected phase discussing, got %s", room.Phase)
	}
	expectedEnds := now.Add(time.Duration(room.TimerDuration) * time.Second)
	if !endsAt.Equal(expectedEnds) {
		t.Errorf("expected endsAt %v, got %v", expectedEnds, endsAt)
	}

	// End discussion -> voting
	if err := room.EndDiscussion(); err != nil {
		t.Fatalf("unexpected error ending discussion: %v", err)
	}
	if room.Phase != domain.PhaseVoting {
		t.Errorf("expected phase voting, got %s", room.Phase)
	}
}

func TestEliminatePlayer_CiviliansWin(t *testing.T) {
	room, _ := createTestRoom(4)
	ws := NewWordSelector(nil)
	_, _ = room.StartGame(ws)
	for _, pid := range room.RevealOrder {
		_, _, _ = room.AdvanceReveal(pid)
	}
	_, _ = room.StartDiscussion(time.Now())
	_ = room.EndDiscussion()

	// Eliminate the imposter directly
	record, gameOver, err := room.EliminatePlayer(room.ImposterID)
	if err != nil {
		t.Fatalf("unexpected error eliminating imposter: %v", err)
	}
	if !gameOver {
		t.Errorf("expected gameOver when imposter is eliminated")
	}
	if !record.WasImposter {
		t.Errorf("expected record.WasImposter to be true")
	}
	if room.Winner != "civilians" {
		t.Errorf("expected winner civilians, got %s", room.Winner)
	}
	if room.Phase != domain.PhaseGameOver {
		t.Errorf("expected phase gameOver, got %s", room.Phase)
	}
}

func TestEliminatePlayer_ImposterWins(t *testing.T) {
	room, _ := createTestRoom(4)
	ws := NewWordSelector(nil)
	_, _ = room.StartGame(ws)
	for _, pid := range room.RevealOrder {
		_, _, _ = room.AdvanceReveal(pid)
	}
	_, _ = room.StartDiscussion(time.Now())
	_ = room.EndDiscussion()

	// Find two civilians to eliminate
	var civilians []string
	for _, pid := range room.ActivePlayerIDs {
		if pid != room.ImposterID {
			civilians = append(civilians, pid)
		}
	}

	// Round 1: eliminate civilian 1 (4 players -> 3 players remain)
	rec1, gameOver1, err := room.EliminatePlayer(civilians[0])
	if err != nil {
		t.Fatalf("unexpected error eliminating civilian 1: %v", err)
	}
	if gameOver1 {
		t.Errorf("game should not be over with 3 players remaining")
	}
	if rec1.WasImposter {
		t.Errorf("wasImposter should be false")
	}
	if room.Phase != domain.PhaseResult {
		t.Errorf("expected phase result, got %s", room.Phase)
	}

	// Next round -> discussion -> voting
	_, _ = room.NextRound(time.Now())
	_ = room.EndDiscussion()

	// Round 2: eliminate civilian 2 (3 players -> 2 players remain: 1 imposter + 1 civilian)
	rec2, gameOver2, err := room.EliminatePlayer(civilians[1])
	if err != nil {
		t.Fatalf("unexpected error eliminating civilian 2: %v", err)
	}
	if !gameOver2 {
		t.Errorf("expected game over when down to 2 players")
	}
	if rec2.WasImposter {
		t.Errorf("wasImposter should be false")
	}
	if room.Winner != "imposter" {
		t.Errorf("expected winner imposter, got %s", room.Winner)
	}
	if room.Phase != domain.PhaseGameOver {
		t.Errorf("expected phase gameOver, got %s", room.Phase)
	}
}

func TestPlayAgain_And_NewGame(t *testing.T) {
	room, _ := createTestRoom(4)
	ws := NewWordSelector(nil)
	_, _ = room.StartGame(ws)
	for _, pid := range room.RevealOrder {
		_, _, _ = room.AdvanceReveal(pid)
	}
	_, _ = room.StartDiscussion(time.Now())
	_ = room.EndDiscussion()
	_, _, _ = room.EliminatePlayer(room.ImposterID) // game over

	// Test PlayAgain
	roles, err := room.PlayAgain(ws)
	if err != nil {
		t.Fatalf("failed to play again: %v", err)
	}
	if room.Phase != domain.PhaseRevealing {
		t.Errorf("expected phase revealing, got %s", room.Phase)
	}
	if len(roles) != 4 {
		t.Errorf("expected 4 roles, got %d", len(roles))
	}
	if room.Winner != "" {
		t.Errorf("expected winner reset to empty")
	}

	// Fast forward to game over again
	for _, pid := range room.RevealOrder {
		_, _, _ = room.AdvanceReveal(pid)
	}
	_, _ = room.StartDiscussion(time.Now())
	_ = room.EndDiscussion()
	_, _, _ = room.EliminatePlayer(room.ImposterID) // game over

	// Test NewGame
	if err := room.NewGame(); err != nil {
		t.Fatalf("failed to reset new game: %v", err)
	}
	if room.Phase != domain.PhaseLobby {
		t.Errorf("expected phase lobby, got %s", room.Phase)
	}
	if len(room.WordHistory) != 0 {
		t.Errorf("expected word history cleared, got %d", len(room.WordHistory))
	}
	for _, p := range room.Players {
		if !p.IsActive || p.IsReady {
			t.Errorf("player state not properly reset: %+v", p)
		}
	}
}
