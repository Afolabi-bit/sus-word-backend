package hub

import (
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"sync"
	"time"

	"sus-word-backend/internal/game"
)

// Ambiguous characters excluded: 0, O, 1, I, L
const roomCodeCharset = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"
const roomCodeLength = 6
const maxAbsoluteRoomAge = 2 * time.Hour

var (
	ErrMaxRoomsReached = errors.New("maximum concurrent rooms limit reached")
	ErrRoomNotFound    = errors.New("room not found")
)

// Hub maintains the global thread-safe registry of active rooms.
type Hub struct {
	rooms        map[string]*Room
	maxRooms     int
	roomTTL      time.Duration
	wordSelector *game.WordSelector

	mu       sync.RWMutex
	stopChan chan struct{}
	closed   bool
}

// NewHub constructs a new Hub instance.
func NewHub(maxRooms int, roomTTL time.Duration, ws *game.WordSelector) *Hub {
	if ws == nil {
		ws = game.NewWordSelector(nil)
	}

	return &Hub{
		rooms:        make(map[string]*Room),
		maxRooms:     maxRooms,
		roomTTL:      roomTTL,
		wordSelector: ws,
		stopChan:     make(chan struct{}),
	}
}

// CreateRoom generates a unique room code, creates a Room actor, and registers it.
func (h *Hub) CreateRoom(hostName string) (*Room, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return nil, errors.New("hub is closed")
	}

	if len(h.rooms) >= h.maxRooms {
		return nil, ErrMaxRoomsReached
	}

	// Generate unique collision-free code
	code, err := h.generateUniqueCodeLocked()
	if err != nil {
		return nil, err
	}

	room := NewRoom(code, "", hostName, h.wordSelector, func(closedCode string) {
		h.RemoveRoom(closedCode)
	})

	h.rooms[code] = room
	room.Start()

	slog.Info("created room in hub", "roomCode", code, "totalRooms", len(h.rooms))
	return room, nil
}

// GetRoom retrieves an active room by code.
func (h *Hub) GetRoom(code string) (*Room, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	room, ok := h.rooms[code]
	return room, ok
}

// RemoveRoom safely deletes a room from the registry.
func (h *Hub) RemoveRoom(code string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, ok := h.rooms[code]; ok {
		delete(h.rooms, code)
		slog.Info("removed room from hub", "roomCode", code, "remainingRooms", len(h.rooms))
	}
}

// RoomCount returns the current count of active rooms.
func (h *Hub) RoomCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.rooms)
}

// StartReaper initiates periodic cleanup of expired and empty rooms.
func (h *Hub) StartReaper(interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		for {
			select {
			case <-ticker.C:
				h.reapExpiredRooms()
			case <-h.stopChan:
				ticker.Stop()
				return
			}
		}
	}()
}

// reapExpiredRooms identifies and closes stale rooms.
func (h *Hub) reapExpiredRooms() {
	h.mu.RLock()
	var toClose []*Room
	now := time.Now()

	for _, room := range h.rooms {
		snapshot := room.Snapshot()
		// Absolute age check (2 hours)
		if now.Sub(room.state.CreatedAt) > maxAbsoluteRoomAge {
			toClose = append(toClose, room)
			continue
		}
		// Empty room check exceeding TTL (30 minutes)
		if len(snapshot.Players) == 0 && now.Sub(room.state.CreatedAt) > h.roomTTL {
			toClose = append(toClose, room)
		}
	}
	h.mu.RUnlock()

	for _, room := range toClose {
		slog.Info("reaper closing expired room", "roomCode", room.Code)
		room.Close()
	}
}

// Close gracefully stops the reaper and closes all rooms.
func (h *Hub) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	close(h.stopChan)

	var allRooms []*Room
	for _, r := range h.rooms {
		allRooms = append(allRooms, r)
	}
	h.mu.Unlock()

	for _, r := range allRooms {
		r.Close()
	}
}

// generateUniqueCodeLocked generates a random 6-character uppercase code not present in rooms.
func (h *Hub) generateUniqueCodeLocked() (string, error) {
	const maxAttempts = 100
	charsetLen := big.NewInt(int64(len(roomCodeCharset)))

	for attempt := 0; attempt < maxAttempts; attempt++ {
		b := make([]byte, roomCodeLength)
		for i := 0; i < roomCodeLength; i++ {
			idx, err := rand.Int(rand.Reader, charsetLen)
			if err != nil {
				return "", err
			}
			b[i] = roomCodeCharset[idx.Int64()]
		}
		code := string(b)
		if _, exists := h.rooms[code]; !exists {
			return code, nil
		}
	}

	return "", fmt.Errorf("failed to generate unique room code after %d attempts", maxAttempts)
}
