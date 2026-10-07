package player

import (
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/time/rate"

	"sus-word-backend/internal/domain"
)

const (
	// Time allowed to write a message to the peer.
	writeWait = 10 * time.Second

	// Time allowed to read the next pong message from the peer.
	pongWait = 60 * time.Second

	// Send pings to peer with this period. Must be less than pongWait.
	pingPeriod = (pongWait * 9) / 10

	// Maximum message size allowed from peer (4 KB).
	maxMessageSize = 4096

	// Channel buffer size for outgoing messages.
	sendBufferSize = 64

	// Maximum consecutive invalid actions before connection is terminated.
	maxConsecutiveErrors = 10
)

var (
	ErrBufferFull = errors.New("client send buffer is full")
	ErrClosed     = errors.New("player connection is closed")
)

// InboundDispatcher defines the callback interface for incoming events from a player.
type InboundDispatcher interface {
	HandleInbound(sender *Player, env domain.Envelope)
	HandleDisconnect(sender *Player)
}

// Player represents a connected client in a game room.
type Player struct {
	ID          string
	DisplayName string
	RoomCode    string
	ConnectedAt time.Time
	IsHost      bool
	IsReady     bool
	IsActive    bool

	conn       *websocket.Conn
	send       chan []byte
	dispatcher InboundDispatcher
	onDisconnect func()

	limiter         *rate.Limiter
	errorCount      atomic.Int32
	closed          atomic.Bool
	closeOnce       sync.Once
	mu              sync.Mutex
}

// NewPlayer creates a new Player connection handler.
func NewPlayer(
	id string,
	displayName string,
	roomCode string,
	isHost bool,
	conn *websocket.Conn,
	dispatcher InboundDispatcher,
) *Player {
	// Rate limiter: 20 messages per second, burst capacity of 20
	limiter := rate.NewLimiter(rate.Limit(20), 20)

	return &Player{
		ID:          id,
		DisplayName: displayName,
		RoomCode:    roomCode,
		ConnectedAt: time.Now(),
		IsHost:      isHost,
		IsActive:    true,
		IsReady:     false,
		conn:        conn,
		send:        make(chan []byte, sendBufferSize),
		dispatcher:  dispatcher,
		limiter:     limiter,
	}
}

// Send queues a byte slice message to be delivered by the write pump.
// Returns false if connection is closed or client buffer is full.
func (p *Player) Send(data []byte) bool {
	if p.closed.Load() {
		return false
	}

	select {
	case p.send <- data:
		return true
	default:
		slog.Warn("client send buffer full, dropping message and disconnecting slow client",
			"playerId", p.ID,
			"displayName", p.DisplayName,
			"roomCode", p.RoomCode,
		)
		p.Close()
		return false
	}
}

// SendEnvelope serializes and sends a typed domain message to the player.
func (p *Player) SendEnvelope(msgType string, payload any, reqID string) error {
	data, err := domain.NewEnvelope(msgType, payload, reqID)
	if err != nil {
		return err
	}
	if !p.Send(data) {
		return ErrClosed
	}
	return nil
}

// RecordError increments consecutive error count and closes connection if threshold exceeded.
func (p *Player) RecordError() int32 {
	count := p.errorCount.Add(1)
	if count >= maxConsecutiveErrors {
		slog.Warn("player exceeded maximum consecutive errors, disconnecting",
			"playerId", p.ID,
			"displayName", p.DisplayName,
			"errors", count,
		)
		_ = p.SendEnvelope(domain.MsgTypeError, domain.ErrorPayload{
			Code:    domain.ErrCodeRateLimited,
			Message: "Too many invalid actions: connection terminated",
		}, "")
		p.Close()
	}
	return count
}

// RecordSuccess resets the consecutive error counter.
func (p *Player) RecordSuccess() {
	p.errorCount.Store(0)
}

// Close closes the WebSocket connection and channel safely once.
func (p *Player) Close() {
	p.closeOnce.Do(func() {
		p.closed.Store(true)
		if p.conn != nil {
			_ = p.conn.Close()
		}
	})
}

// IsClosed returns whether the player connection is closed.
func (p *Player) IsClosed() bool {
	return p.closed.Load()
}

// SetOnDisconnect registers a cleanup callback executed when the socket closes.
func (p *Player) SetOnDisconnect(fn func()) {
	p.onDisconnect = fn
}

// StartPumps starts the read and write pump goroutines.
func (p *Player) StartPumps() {
	go p.writePump()
	go p.readPump()
}

// readPump pumps messages from the websocket connection to the room inbound dispatcher.
func (p *Player) readPump() {
	defer func() {
		p.Close()
		if p.onDisconnect != nil {
			p.onDisconnect()
		}
		if p.dispatcher != nil {
			p.dispatcher.HandleDisconnect(p)
		}
	}()

	p.conn.SetReadLimit(maxMessageSize)
	_ = p.conn.SetReadDeadline(time.Now().Add(pongWait))
	p.conn.SetPongHandler(func(string) error {
		_ = p.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, message, err := p.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				slog.Debug("websocket closed", "playerId", p.ID, "error", err)
			}
			break
		}

		// Rate limit enforcement: 20 msg/sec
		if !p.limiter.Allow() {
			slog.Warn("player rate limited", "playerId", p.ID, "roomCode", p.RoomCode)
			_ = p.SendEnvelope(domain.MsgTypeError, domain.ErrorPayload{
				Code:    domain.ErrCodeRateLimited,
				Message: "Message rate limit exceeded (max 20/sec)",
			}, "")
			continue
		}

		var env domain.Envelope
		if err := json.Unmarshal(message, &env); err != nil {
			slog.Debug("failed to parse message envelope", "playerId", p.ID, "error", err)
			p.RecordError()
			_ = p.SendEnvelope(domain.MsgTypeError, domain.ErrorPayload{
				Code:    domain.ErrCodeInvalidPayload,
				Message: "Malformed JSON message envelope",
			}, "")
			continue
		}

		if p.dispatcher != nil {
			p.dispatcher.HandleInbound(p, env)
		}
	}
}

// writePump pumps messages from the send channel to the websocket connection.
func (p *Player) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		p.Close()
	}()

	for {
		select {
		case message, ok := <-p.send:
			_ = p.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// Send channel closed
				_ = p.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			if err := p.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}

			// Flush any pending messages queued in buffer as individual frames
			n := len(p.send)
			for i := 0; i < n; i++ {
				nextMsg := <-p.send
				if err := p.conn.WriteMessage(websocket.TextMessage, nextMsg); err != nil {
					return
				}
			}

		case <-ticker.C:
			_ = p.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := p.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
