package hub

import (
	"strings"
	"testing"
	"time"

	"sus-word-backend/internal/domain"
	"sus-word-backend/internal/game"
	"sus-word-backend/internal/player"
)

func TestHub_CreateRoom_CodeFormat(t *testing.T) {
	ws := game.NewWordSelector(nil)
	h := NewHub(10, 30*time.Minute, ws)
	defer h.Close()

	room, err := h.CreateRoom("Alice")
	if err != nil {
		t.Fatalf("failed to create room: %v", err)
	}

	if len(room.Code) != 6 {
		t.Errorf("expected 6 char code, got %s", room.Code)
	}

	// Verify unambiguous characters
	for _, ch := range room.Code {
		if !strings.ContainsRune(roomCodeCharset, ch) {
			t.Errorf("unexpected character %c in room code %s", ch, room.Code)
		}
	}

	retrieved, ok := h.GetRoom(room.Code)
	if !ok || retrieved != room {
		t.Errorf("failed to retrieve room by code")
	}
}

func TestHub_MaxRoomsEnforcement(t *testing.T) {
	ws := game.NewWordSelector(nil)
	h := NewHub(2, 30*time.Minute, ws)
	defer h.Close()

	_, err1 := h.CreateRoom("Host1")
	if err1 != nil {
		t.Fatalf("failed creating room 1: %v", err1)
	}
	_, err2 := h.CreateRoom("Host2")
	if err2 != nil {
		t.Fatalf("failed creating room 2: %v", err2)
	}

	// Third room should be rejected
	_, err3 := h.CreateRoom("Host3")
	if err3 != ErrMaxRoomsReached {
		t.Errorf("expected ErrMaxRoomsReached, got %v", err3)
	}
}

func TestRoom_HostElection_OnDisconnect(t *testing.T) {
	ws := game.NewWordSelector(nil)
	h := NewHub(10, 30*time.Minute, ws)
	defer h.Close()

	room, err := h.CreateRoom("Alice")
	if err != nil {
		t.Fatalf("failed creating room: %v", err)
	}

	// Create 2 mock players
	p1 := player.NewPlayer("p-1", "Alice", room.Code, true, nil, room)
	p2 := player.NewPlayer("p-2", "Bob", room.Code, false, nil, room)

	if err := room.JoinPlayer(p1); err != nil {
		t.Fatalf("failed to join player 1: %v", err)
	}
	if err := room.JoinPlayer(p2); err != nil {
		t.Fatalf("failed to join player 2: %v", err)
	}

	snap1 := room.Snapshot()
	if snap1.HostID != "p-1" {
		t.Errorf("expected p-1 to be host, got %s", snap1.HostID)
	}

	// Disconnect host (p1)
	room.HandleDisconnect(p1)

	// Allow event loop to process
	time.Sleep(50 * time.Millisecond)

	snap2 := room.Snapshot()
	if snap2.HostID != "p-2" {
		t.Errorf("expected host failover to p-2, got %s", snap2.HostID)
	}
}

func TestRoom_Reconnection(t *testing.T) {
	ws := game.NewWordSelector(nil)
	h := NewHub(10, 30*time.Minute, ws)
	defer h.Close()

	room, err := h.CreateRoom("Alice")
	if err != nil {
		t.Fatalf("failed creating room: %v", err)
	}

	p1 := player.NewPlayer("p-1", "Alice", room.Code, true, nil, room)
	p2 := player.NewPlayer("p-2", "Bob", room.Code, false, nil, room)

	_ = room.JoinPlayer(p1)
	_ = room.JoinPlayer(p2)

	// Bob disconnects
	room.HandleDisconnect(p2)
	time.Sleep(50 * time.Millisecond)

	// Bob reconnects with a new connection object
	p2Reconnect := player.NewPlayer("new-conn-id", "Bob", room.Code, false, nil, room)
	if err := room.JoinPlayer(p2Reconnect); err != nil {
		t.Fatalf("expected Bob to reconnect successfully: %v", err)
	}

	// Verify Bob retained original identity
	if p2Reconnect.ID != "p-2" {
		t.Errorf("expected reconnected player to have original ID p-2, got %s", p2Reconnect.ID)
	}
}

func TestRoom_GameFlow_PrivateRoleAssignment(t *testing.T) {
	ws := game.NewWordSelector(nil)
	h := NewHub(10, 30*time.Minute, ws)
	defer h.Close()

	room, _ := h.CreateRoom("Host")

	players := []*player.Player{
		player.NewPlayer("p-1", "Alice", room.Code, true, nil, room),
		player.NewPlayer("p-2", "Bob", room.Code, false, nil, room),
		player.NewPlayer("p-3", "Charlie", room.Code, false, nil, room),
		player.NewPlayer("p-4", "Dave", room.Code, false, nil, room),
	}

	for _, p := range players {
		if err := room.JoinPlayer(p); err != nil {
			t.Fatalf("failed to join player: %v", err)
		}
	}

	// Alice (host) starts the game
	room.HandleInbound(players[0], domain.Envelope{
		Type: domain.MsgTypeStartGame,
	})

	time.Sleep(50 * time.Millisecond)

	snap := room.Snapshot()
	if snap.Phase != domain.PhaseRevealing {
		t.Errorf("expected phase revealing, got %s", snap.Phase)
	}
	if len(snap.ActivePlayers) != 4 {
		t.Errorf("expected 4 active players, got %d", len(snap.ActivePlayers))
	}
}
