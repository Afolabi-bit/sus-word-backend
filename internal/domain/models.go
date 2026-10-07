package domain

import (
	"time"
)

// WordEntry represents a single word entry with its associated category.
type WordEntry struct {
	Word     string `json:"word"`
	Category string `json:"category"`
}

// EliminationRecord tracks a player eliminated during a round.
type EliminationRecord struct {
	PlayerID    string `json:"playerId"`
	DisplayName string `json:"displayName"`
	WasImposter bool   `json:"wasImposter"`
	RoundNumber int    `json:"round"`
}

// PublicPlayer represents the publicly safe player metadata visible to other players.
type PublicPlayer struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	IsHost      bool   `json:"isHost"`
	IsActive    bool   `json:"isActive"`
	IsReady     bool   `json:"isReady"`
}

// PublicRoomState is the sanitized snapshot of room state sent to clients.
// CRITICAL: Never contains SecretWord or ImposterID.
type PublicRoomState struct {
	RoomCode       string              `json:"roomCode"`
	Phase          Phase               `json:"phase"`
	HostID         string              `json:"hostId"`
	TimerDuration  int                 `json:"timerDuration"`
	Players        []PublicPlayer      `json:"players"`
	ActivePlayers  []string            `json:"activePlayers"`
	EliminationLog []EliminationRecord `json:"eliminationLog"`
	LastEliminated *EliminationRecord  `json:"lastEliminated"`
	Winner         *string             `json:"winner"`
	VotedPlayerIDs []string            `json:"votedPlayerIds"`
}

// BasePlayer stores player state common to game logic.
type BasePlayer struct {
	ID             string     `json:"id"`
	DisplayName    string     `json:"displayName"`
	RoomCode       string     `json:"roomCode"`
	ConnectedAt    time.Time  `json:"connectedAt"`
	DisconnectedAt *time.Time `json:"disconnectedAt,omitempty"`
	IsHost         bool       `json:"isHost"`
	IsReady        bool       `json:"isReady"`
	IsActive       bool       `json:"isActive"`
}

// ToPublic converts a BasePlayer to the client-safe PublicPlayer representation.
func (p *BasePlayer) ToPublic() PublicPlayer {
	return PublicPlayer{
		ID:          p.ID,
		DisplayName: p.DisplayName,
		IsHost:      p.IsHost,
		IsActive:    p.IsActive,
		IsReady:     p.IsReady,
	}
}

// RoomData holds the complete authoritative state for a game room.
type RoomData struct {
	Code            string                 `json:"code"`
	Players         map[string]*BasePlayer `json:"players"`
	PlayerOrder     []string               `json:"playerOrder"` // join order for host election
	HostID          string                 `json:"hostId"`
	Phase           Phase                  `json:"phase"`
	SecretWord      string                 `json:"-"` // Omit from JSON by default
	SecretCategory  string                 `json:"secretCategory"`
	ImposterID      string                 `json:"-"` // Omit from JSON by default
	ActivePlayerIDs []string               `json:"activePlayers"`
	EliminationLog  []EliminationRecord    `json:"eliminationLog"`
	LastEliminated  *EliminationRecord     `json:"lastEliminated"`
	Winner          string                 `json:"winner"` // "civilians" | "imposter" | ""
	TimerDuration   int                    `json:"timerDuration"`
	TimerEndsAt     *time.Time             `json:"timerEndsAt"`
	RevealOrder     []string               `json:"revealOrder"`
	RevealIndex     int                    `json:"revealIndex"`
	Votes           map[string]string      `json:"-"` // voterID -> targetID
	WordHistory     map[string]bool        `json:"-"`
	CreatedAt       time.Time              `json:"createdAt"`
}

// ToPublic creates a sanitized PublicRoomState snapshot without secret role information.
func (r *RoomData) ToPublic() PublicRoomState {
	publicPlayers := make([]PublicPlayer, 0, len(r.PlayerOrder))
	for _, pid := range r.PlayerOrder {
		if p, ok := r.Players[pid]; ok {
			publicPlayers = append(publicPlayers, p.ToPublic())
		}
	}

	var winnerPtr *string
	if r.Winner != "" {
		w := r.Winner
		winnerPtr = &w
	}

	activePlayersCopy := make([]string, len(r.ActivePlayerIDs))
	copy(activePlayersCopy, r.ActivePlayerIDs)

	eliminationLogCopy := make([]EliminationRecord, len(r.EliminationLog))
	copy(eliminationLogCopy, r.EliminationLog)

	votedPlayerIDs := make([]string, 0, len(r.Votes))
	for vid := range r.Votes {
		votedPlayerIDs = append(votedPlayerIDs, vid)
	}

	return PublicRoomState{
		RoomCode:       r.Code,
		Phase:          r.Phase,
		HostID:         r.HostID,
		TimerDuration:  r.TimerDuration,
		Players:        publicPlayers,
		ActivePlayers:  activePlayersCopy,
		EliminationLog: eliminationLogCopy,
		LastEliminated: r.LastEliminated,
		Winner:         winnerPtr,
		VotedPlayerIDs: votedPlayerIDs,
	}
}

