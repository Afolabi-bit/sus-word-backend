package ws

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"sus-word-backend/internal/api"
	"sus-word-backend/internal/config"
	"sus-word-backend/internal/domain"
	"sus-word-backend/internal/game"
	"sus-word-backend/internal/hub"
)

type testClient struct {
	name     string
	conn     *websocket.Conn
	received chan domain.Envelope
	roles    chan domain.RoleAssignedPayload
	closed   bool
	mu       sync.Mutex
}

func newTestClient(t *testing.T, serverURL, roomCode, name string) *testClient {
	wsURL := "ws" + strings.TrimPrefix(serverURL, "http") + "/ws?room=" + roomCode + "&name=" + name

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("[%s] failed to connect websocket: %v (resp: %v)", name, err, resp)
	}

	tc := &testClient{
		name:     name,
		conn:     conn,
		received: make(chan domain.Envelope, 100),
		roles:    make(chan domain.RoleAssignedPayload, 10),
	}

	go tc.listen(t)
	return tc
}

func (c *testClient) listen(t *testing.T) {
	for {
		_, msg, err := c.conn.ReadMessage()
		if err != nil {
			return
		}

		var env domain.Envelope
		if err := json.Unmarshal(msg, &env); err != nil {
			t.Errorf("[%s] error parsing envelope: %v", c.name, err)
			continue
		}

		// Security assertion: Verify zero role leakage on any broadcast message
		raw := string(msg)
		if env.Type == domain.MsgTypeRoomState {
			if strings.Contains(raw, "secretWord") {
				t.Errorf("SECURITY VIOLATION: secretWord found in ROOM_STATE broadcast to %s: %s", c.name, raw)
			}
			if strings.Contains(raw, "imposterId") || strings.Contains(raw, "imposterID") {
				t.Errorf("SECURITY VIOLATION: imposterId found in ROOM_STATE broadcast to %s: %s", c.name, raw)
			}
		}

		if env.Type == domain.MsgTypeRoleAssigned {
			var rolePayload domain.RoleAssignedPayload
			_ = json.Unmarshal(env.Payload, &rolePayload)
			c.roles <- rolePayload
		}

		c.received <- env
	}
}

func (c *testClient) send(t *testing.T, msgType string, payload any) {
	data, err := domain.NewEnvelope(msgType, payload, "")
	if err != nil {
		t.Fatalf("[%s] failed to serialize message: %v", c.name, err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("[%s] failed to write message: %v", c.name, err)
	}
}

func (c *testClient) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		_ = c.conn.Close()
	}
}

func (c *testClient) waitForMessageType(t *testing.T, expectedType string, timeout time.Duration) domain.Envelope {
	timer := time.After(timeout)
	for {
		select {
		case env := <-c.received:
			if env.Type == expectedType {
				return env
			}
		case <-timer:
			t.Fatalf("[%s] timed out waiting for message type %s", c.name, expectedType)
		}
	}
}

func TestEndToEnd_GameSession_FullFlow(t *testing.T) {
	cfg := &config.Config{
		Port:           "8080",
		AllowedOrigins: []string{"*"},
		MaxRooms:       50,
		RoomTTL:        30 * time.Minute,
		MaxConnPerIP:   10,
	}

	ws := game.NewWordSelector(nil)
	h := hub.NewHub(cfg.MaxRooms, cfg.RoomTTL, ws)
	defer h.Close()

	apiHandler := api.NewHandler(h, cfg)
	wsHandler := NewHandler(h, cfg)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", wsHandler.ServeHTTP)
	mux.HandleFunc("/api/rooms", apiHandler.HandleCreateRoom)

	server := httptest.NewServer(mux)
	defer server.Close()

	// 1. Create a room
	room, err := h.CreateRoom("Alice")
	if err != nil {
		t.Fatalf("failed to create room: %v", err)
	}

	// 2. Connect 4 players
	clientAlice := newTestClient(t, server.URL, room.Code, "Alice")
	clientBob := newTestClient(t, server.URL, room.Code, "Bob")
	clientCharlie := newTestClient(t, server.URL, room.Code, "Charlie")
	clientDave := newTestClient(t, server.URL, room.Code, "Dave")
	defer clientAlice.close()
	defer clientBob.close()
	defer clientCharlie.close()
	defer clientDave.close()

	clients := []*testClient{clientAlice, clientBob, clientCharlie, clientDave}

	// Allow all connections to settle and join
	time.Sleep(100 * time.Millisecond)

	snap := room.Snapshot()
	if len(snap.Players) != 4 {
		t.Fatalf("expected 4 players in room snapshot, got %d", len(snap.Players))
	}

	// 3. Alice (host) starts the game
	clientAlice.send(t, domain.MsgTypeStartGame, nil)

	// 4. Verify private roles distributed
	imposterCount := 0
	civilianCount := 0
	var imposterClient *testClient
	var civilianClients []*testClient

	for _, c := range clients {
		select {
		case role := <-c.roles:
			if role.Role == "imposter" {
				imposterCount++
				imposterClient = c
				if role.SecretWord != nil {
					t.Fatalf("imposter must have nil secretWord, got %v", *role.SecretWord)
				}
				if role.SecretCategory == "" {
					t.Fatalf("imposter must receive valid secretCategory")
				}
			} else if role.Role == "civilian" {
				civilianCount++
				civilianClients = append(civilianClients, c)
				if role.SecretWord == nil || *role.SecretWord == "" {
					t.Fatalf("civilian must receive non-empty secretWord")
				}
				if role.SecretCategory == "" {
					t.Fatalf("civilian must receive secretCategory")
				}
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("[%s] timed out waiting for ROLE_ASSIGNED", c.name)
		}
	}

	if imposterCount != 1 || civilianCount != 3 {
		t.Fatalf("expected 1 imposter and 3 civilians, got %d and %d", imposterCount, civilianCount)
	}

	// 5. Sequential Reveal Phase: Follow REVEAL_TURN sequence
	for step := 0; step < 4; step++ {
		turnEnv := clientAlice.waitForMessageType(t, domain.MsgTypeRevealTurn, 2*time.Second)
		var turnPayload domain.RevealTurnPayload
		_ = json.Unmarshal(turnEnv.Payload, &turnPayload)

		// Find the client matching turnPayload.CurrentPlayerName
		var currentClient *testClient
		for _, c := range clients {
			if c.name == turnPayload.CurrentPlayerName {
				currentClient = c
				break
			}
		}
		if currentClient == nil {
			t.Fatalf("no client matches reveal turn: %s", turnPayload.CurrentPlayerName)
		}

		// That client signals ready
		currentClient.send(t, domain.MsgTypePlayerReady, nil)
	}

	// After all 4 ready, room transitions to PhaseReady
	readyEnv := clientAlice.waitForMessageType(t, domain.MsgTypeRoomState, 2*time.Second)
	var readyState domain.PublicRoomState
	_ = json.Unmarshal(readyEnv.Payload, &readyState)
	if readyState.Phase != domain.PhaseReady {
		t.Fatalf("expected phase ready after reveals, got %s", readyState.Phase)
	}

	// 6. Alice (host) starts Discussion
	clientAlice.send(t, domain.MsgTypeStartDiscussion, nil)

	discEnv := clientAlice.waitForMessageType(t, domain.MsgTypeDiscussionStarted, 2*time.Second)
	var discPayload domain.DiscussionStartedPayload
	_ = json.Unmarshal(discEnv.Payload, &discPayload)
	if discPayload.DurationSeconds <= 0 {
		t.Fatalf("invalid discussion duration: %d", discPayload.DurationSeconds)
	}

	// Fast forward discussion -> voting (by having host end discussion or eliminate)
	room.EndDiscussion()
	clientAlice.waitForMessageType(t, domain.MsgTypeRoomState, 2*time.Second)

	// 7. Host eliminates a civilian first
	civToEliminate := civilianClients[0]
	// Find their player ID from room snapshot
	civID := ""
	for _, p := range room.Snapshot().Players {
		if p.DisplayName == civToEliminate.name {
			civID = p.ID
			break
		}
	}

	clientAlice.send(t, domain.MsgTypeEliminatePlayer, domain.EliminatePlayerPayload{PlayerID: civID})

	elimEnv := clientAlice.waitForMessageType(t, domain.MsgTypePlayerEliminated, 2*time.Second)
	var elimPayload domain.PlayerEliminatedPayload
	_ = json.Unmarshal(elimEnv.Payload, &elimPayload)
	if elimPayload.WasImposter {
		t.Fatalf("expected civilian elimination to have wasImposter = false")
	}

	// Result phase -> Host advances to next round
	clientAlice.send(t, domain.MsgTypeNextRound, nil)
	clientAlice.waitForMessageType(t, domain.MsgTypeDiscussionStarted, 2*time.Second)

	// Fast forward to voting again
	room.EndDiscussion()
	clientAlice.waitForMessageType(t, domain.MsgTypeRoomState, 2*time.Second)

	// 8. Host eliminates the imposter!
	imposterID := ""
	for _, p := range room.Snapshot().Players {
		if p.DisplayName == imposterClient.name {
			imposterID = p.ID
			break
		}
	}

	clientAlice.send(t, domain.MsgTypeEliminatePlayer, domain.EliminatePlayerPayload{PlayerID: imposterID})

	// Civilians win!
	gameOverEnv := clientAlice.waitForMessageType(t, domain.MsgTypeGameOver, 2*time.Second)
	var gameOverPayload domain.GameOverPayload
	_ = json.Unmarshal(gameOverEnv.Payload, &gameOverPayload)

	if gameOverPayload.Winner != "civilians" {
		t.Errorf("expected winner civilians, got %s", gameOverPayload.Winner)
	}
	if gameOverPayload.ImposterName != imposterClient.name {
		t.Errorf("expected imposter name %s, got %s", imposterClient.name, gameOverPayload.ImposterName)
	}
	if gameOverPayload.SecretWord == "" {
		t.Errorf("expected revealed secret word in GAME_OVER")
	}
}

func TestEndToEnd_IPConnectionLimit(t *testing.T) {
	// Set MaxConnPerIP = 2
	cfg := &config.Config{
		Port:           "8080",
		AllowedOrigins: []string{"*"},
		MaxRooms:       50,
		RoomTTL:        30 * time.Minute,
		MaxConnPerIP:   2,
	}

	ws := game.NewWordSelector(nil)
	h := hub.NewHub(cfg.MaxRooms, cfg.RoomTTL, ws)
	defer h.Close()

	wsHandler := NewHandler(h, cfg)
	server := httptest.NewServer(http.HandlerFunc(wsHandler.ServeHTTP))
	defer server.Close()

	room, _ := h.CreateRoom("Host")

	// Connect 2 clients (allowed)
	c1 := newTestClient(t, server.URL, room.Code, "Client1")
	c2 := newTestClient(t, server.URL, room.Code, "Client2")
	defer c1.close()
	defer c2.close()

	// 3rd client from same IP should be rejected with 429 Too Many Requests
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?room=" + room.Code + "&name=Client3"
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		t.Fatalf("expected 3rd connection to be rejected by IP rate limit")
	}
	if resp != nil && resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected status 429 Too Many Requests, got %d", resp.StatusCode)
	}
}
