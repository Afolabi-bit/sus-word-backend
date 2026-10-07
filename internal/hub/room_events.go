package hub

import (
	"sus-word-backend/internal/domain"
	"sus-word-backend/internal/player"
)

// roomEvent represents an event processed by the Room actor event loop.
type roomEventType int

const (
	eventPlayerJoin roomEventType = iota
	eventPlayerLeave
	eventClientAction
	eventTimerExpired
	eventAbandonTimeout
	eventStop
)

type roomEvent struct {
	eventType roomEventType
	player    *player.Player
	envelope  domain.Envelope
	joinResp  chan error
}
