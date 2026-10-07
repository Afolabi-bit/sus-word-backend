package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"sus-word-backend/internal/config"
	"sus-word-backend/internal/domain"
	"sus-word-backend/internal/game"
	"sus-word-backend/internal/hub"
)

func setupTestApp() (*chi.Mux, *hub.Hub) {
	cfg := &config.Config{
		Port:           "8080",
		AllowedOrigins: []string{"*"},
		MaxRooms:       50,
		RoomTTL:        30 * time.Minute,
	}

	ws := game.NewWordSelector(nil)
	h := hub.NewHub(cfg.MaxRooms, cfg.RoomTTL, ws)
	handler := NewHandler(h, cfg)

	r := chi.NewRouter()
	handler.RegisterRoutes(r)

	return r, h
}

func TestHandler_CreateRoom(t *testing.T) {
	r, h := setupTestApp()
	defer h.Close()

	body := `{"hostName":"Alice"}`
	req := httptest.NewRequest(http.MethodPost, "/api/rooms", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d", w.Code)
	}

	var resp domain.CreateRoomResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp.RoomCode) != 6 {
		t.Errorf("expected 6-char room code, got %s", resp.RoomCode)
	}
	if !strings.Contains(resp.WSUrl, resp.RoomCode) {
		t.Errorf("expected wsUrl to contain room code, got %s", resp.WSUrl)
	}
}

func TestHandler_GetRoom_Success_And_NotFound(t *testing.T) {
	r, h := setupTestApp()
	defer h.Close()

	// 1. Non-existent room -> 404
	req404 := httptest.NewRequest(http.MethodGet, "/api/rooms/NONEXT", nil)
	w404 := httptest.NewRecorder()
	r.ServeHTTP(w404, req404)

	if w404.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w404.Code)
	}

	// 2. Create room
	room, err := h.CreateRoom("Alice")
	if err != nil {
		t.Fatalf("failed creating room: %v", err)
	}

	// 3. Existing room -> 200
	req200 := httptest.NewRequest(http.MethodGet, "/api/rooms/"+room.Code, nil)
	w200 := httptest.NewRecorder()
	r.ServeHTTP(w200, req200)

	if w200.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w200.Code)
	}

	var info domain.RoomInfoResponse
	if err := json.Unmarshal(w200.Body.Bytes(), &info); err != nil {
		t.Fatalf("failed decoding info: %v", err)
	}

	if info.RoomCode != room.Code {
		t.Errorf("expected code %s, got %s", room.Code, info.RoomCode)
	}
	if info.Phase != domain.PhaseLobby {
		t.Errorf("expected phase lobby, got %s", info.Phase)
	}
	if !info.Joinable {
		t.Errorf("expected joinable to be true in lobby")
	}
}

func TestHandler_Health(t *testing.T) {
	r, h := setupTestApp()
	defer h.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to parse json: %v", err)
	}

	if body["status"] != "ok" {
		t.Errorf("expected status ok, got %v", body["status"])
	}
}
