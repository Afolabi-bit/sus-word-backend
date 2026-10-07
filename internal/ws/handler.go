package ws

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"sus-word-backend/internal/config"
	"sus-word-backend/internal/domain"
	"sus-word-backend/internal/hub"
	"sus-word-backend/internal/middleware"
	"sus-word-backend/internal/player"
)

// Handler handles WebSocket upgrades and connection handshakes.
type Handler struct {
	hub       *hub.Hub
	cfg       *config.Config
	upgrader  websocket.Upgrader
	ipTracker *middleware.IPConnectionTracker
}

// NewHandler creates a WebSocket handler with configured origins.
func NewHandler(h *hub.Hub, cfg *config.Config) *Handler {
	maxConns := cfg.MaxConnPerIP
	if maxConns <= 0 {
		maxConns = 5
	}
	ipTracker := middleware.NewIPConnectionTracker(maxConns)

	upgrader := websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin: func(r *http.Request) bool {
			// If wildcard origin allowed
			for _, allowed := range cfg.AllowedOrigins {
				if allowed == "*" {
					return true
				}
			}

			origin := r.Header.Get("Origin")
			if origin == "" {
				return true // Direct non-browser clients allowed
			}

			for _, allowed := range cfg.AllowedOrigins {
				if strings.EqualFold(origin, allowed) {
					return true
				}
			}

			slog.Warn("rejected websocket origin", "origin", origin)
			return false
		},
	}

	return &Handler{
		hub:       h,
		cfg:       cfg,
		upgrader:  upgrader,
		ipTracker: ipTracker,
	}
}

// ServeHTTP handles the /ws route.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	roomCode := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("room")))
	name := strings.TrimSpace(r.URL.Query().Get("name"))

	if roomCode == "" {
		http.Error(w, "Query parameter 'room' is required", http.StatusBadRequest)
		return
	}
	if name == "" {
		http.Error(w, "Query parameter 'name' is required", http.StatusBadRequest)
		return
	}
	if len(name) > 24 {
		http.Error(w, "Display name must be 24 characters or less", http.StatusBadRequest)
		return
	}

	room, ok := h.hub.GetRoom(roomCode)
	if !ok {
		http.Error(w, "Room not found", http.StatusNotFound)
		return
	}

	clientIP := middleware.ExtractIP(r)
	if !h.ipTracker.Acquire(clientIP) {
		slog.Warn("rejected connection: max connections per IP reached", "ip", clientIP)
		http.Error(w, "Maximum concurrent connections reached for your IP (max 5)", http.StatusTooManyRequests)
		return
	}

	wsConn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.ipTracker.Release(clientIP)
		slog.Error("failed to upgrade websocket connection", "error", err)
		return
	}

	playerID := uuid.NewString()
	p := player.NewPlayer(playerID, name, roomCode, false, wsConn, room)
	p.SetOnDisconnect(func() {
		h.ipTracker.Release(clientIP)
	})

	// Attempt joining the room actor
	if err := room.JoinPlayer(p); err != nil {
		slog.Warn("player rejected by room", "roomCode", roomCode, "error", err)
		// Extract error message
		parts := strings.SplitN(err.Error(), ": ", 2)
		code := domain.ErrCodeInvalidPhase
		msg := err.Error()
		if len(parts) == 2 {
			code = parts[0]
			msg = parts[1]
		}

		_ = p.SendEnvelope(domain.MsgTypeError, domain.ErrorPayload{
			Code:    code,
			Message: msg,
		}, "")
		p.Close()
		return
	}

	// Handshake successful: launch read and write pumps
	p.StartPumps()
}
