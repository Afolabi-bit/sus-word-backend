package ws

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"sus-word-backend/internal/config"
	"sus-word-backend/internal/game"
	"sus-word-backend/internal/hub"
)

func TestWSHandler_ValidationErrors(t *testing.T) {
	cfg := &config.Config{
		Port:           "8080",
		AllowedOrigins: []string{"*"},
		MaxRooms:       50,
		RoomTTL:        30 * time.Minute,
	}
	ws := game.NewWordSelector(nil)
	h := hub.NewHub(cfg.MaxRooms, cfg.RoomTTL, ws)
	defer h.Close()

	wsHandler := NewHandler(h, cfg)

	// Missing room query param
	req1 := httptest.NewRequest(http.MethodGet, "/ws?name=Alice", nil)
	w1 := httptest.NewRecorder()
	wsHandler.ServeHTTP(w1, req1)
	if w1.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing room, got %d", w1.Code)
	}

	// Missing name query param
	req2 := httptest.NewRequest(http.MethodGet, "/ws?room=XK92PL", nil)
	w2 := httptest.NewRecorder()
	wsHandler.ServeHTTP(w2, req2)
	if w2.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing name, got %d", w2.Code)
	}

	// Room not found
	req3 := httptest.NewRequest(http.MethodGet, "/ws?room=NONEXT&name=Alice", nil)
	w3 := httptest.NewRecorder()
	wsHandler.ServeHTTP(w3, req3)
	if w3.Code != http.StatusNotFound {
		t.Errorf("expected 404 for missing room, got %d", w3.Code)
	}
}

func TestWSHandler_SuccessfulUpgrade(t *testing.T) {
	cfg := &config.Config{
		Port:           "8080",
		AllowedOrigins: []string{"*"},
		MaxRooms:       50,
		RoomTTL:        30 * time.Minute,
	}
	ws := game.NewWordSelector(nil)
	h := hub.NewHub(cfg.MaxRooms, cfg.RoomTTL, ws)
	defer h.Close()

	room, err := h.CreateRoom("Alice")
	if err != nil {
		t.Fatalf("failed to create room: %v", err)
	}

	wsHandler := NewHandler(h, cfg)
	server := httptest.NewServer(http.HandlerFunc(wsHandler.ServeHTTP))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?room=" + room.Code + "&name=Alice"

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to dial websocket: %v", err)
	}
	defer conn.Close()

	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Errorf("expected status 101 Switching Protocols, got %d", resp.StatusCode)
	}

	// Give the room actor a moment to process the join
	time.Sleep(50 * time.Millisecond)

	snap := room.Snapshot()
	if len(snap.Players) != 1 {
		t.Errorf("expected 1 player joined, got %d", len(snap.Players))
	}
}
