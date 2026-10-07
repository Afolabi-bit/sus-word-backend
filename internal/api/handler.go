package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"sus-word-backend/internal/config"
	"sus-word-backend/internal/domain"
	"sus-word-backend/internal/game"
	"sus-word-backend/internal/hub"
)

// Handler serves HTTP REST endpoints.
type Handler struct {
	hub *hub.Hub
	cfg *config.Config
}

// NewHandler constructs an API Handler instance.
func NewHandler(h *hub.Hub, cfg *config.Config) *Handler {
	return &Handler{
		hub: h,
		cfg: cfg,
	}
}

// RegisterRoutes registers REST routes on the provided Chi router.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Post("/api/rooms", h.HandleCreateRoom)
	r.Get("/api/rooms/{code}", h.HandleGetRoom)
	r.Get("/api/health", h.HandleHealth)
}

func (h *Handler) HandleCreateRoom(w http.ResponseWriter, r *http.Request) {
	var req domain.CreateRoomRequest
	if r.Body != nil && r.ContentLength > 0 {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	hostName := strings.TrimSpace(req.HostName)
	if hostName == "" {
		hostName = "Host"
	}

	room, err := h.hub.CreateRoom(hostName)
	if err != nil {
		if err == hub.ErrMaxRoomsReached {
			respondError(w, http.StatusServiceUnavailable, "Server is currently at maximum room capacity")
			return
		}
		respondError(w, http.StatusInternalServerError, "Failed to create room")
		return
	}

	scheme := "ws"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "wss"
	}

	host := r.Host
	if host == "" {
		host = fmt.Sprintf("localhost:%s", h.cfg.Port)
	}

	wsUrl := fmt.Sprintf("%s://%s/ws?room=%s&name=%s",
		scheme,
		host,
		room.Code,
		url.QueryEscape(hostName),
	)

	resp := domain.CreateRoomResponse{
		RoomCode:  room.Code,
		WSUrl:     wsUrl,
		ExpiresAt: time.Now().Add(h.cfg.RoomTTL),
	}

	respondJSON(w, http.StatusCreated, resp)
}

func (h *Handler) HandleGetRoom(w http.ResponseWriter, r *http.Request) {
	code := strings.ToUpper(strings.TrimSpace(chi.URLParam(r, "code")))
	if code == "" {
		respondError(w, http.StatusBadRequest, "Room code is required")
		return
	}

	room, ok := h.hub.GetRoom(code)
	if !ok {
		respondError(w, http.StatusNotFound, "Room not found")
		return
	}

	snap := room.Snapshot()
	joinable := snap.Phase == domain.PhaseLobby && len(snap.Players) < game.MaxPlayers

	resp := domain.RoomInfoResponse{
		RoomCode:    code,
		Phase:       snap.Phase,
		PlayerCount: len(snap.Players),
		MaxPlayers:  game.MaxPlayers,
		Joinable:    joinable,
	}

	respondJSON(w, http.StatusOK, resp)
}

func (h *Handler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"rooms":  h.hub.RoomCount(),
	})
}
