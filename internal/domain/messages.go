package domain

import (
	"encoding/json"
	"time"
)

// WebSocket message envelope
type Envelope struct {
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	RequestID string          `json:"requestId,omitempty"`
}

// Client -> Server Message Types
const (
	MsgTypeJoinRoom        = "JOIN_ROOM"
	MsgTypeSetTimer        = "SET_TIMER"
	MsgTypeStartGame       = "START_GAME"
	MsgTypePlayerReady     = "PLAYER_READY"
	MsgTypeStartDiscussion = "START_DISCUSSION"
	MsgTypeEndDiscussion   = "END_DISCUSSION"
	MsgTypeEliminatePlayer = "ELIMINATE_PLAYER"
	MsgTypeCastVote        = "CAST_VOTE"
	MsgTypeNextRound       = "NEXT_ROUND"
	MsgTypePlayAgain       = "PLAY_AGAIN"
	MsgTypeNewGame         = "NEW_GAME"
	MsgTypePing            = "PING"
)

// Server -> Client Message Types
const (
	MsgTypeRoomState         = "ROOM_STATE"
	MsgTypeRoleAssigned      = "ROLE_ASSIGNED"
	MsgTypeRevealTurn        = "REVEAL_TURN"
	MsgTypeDiscussionStarted = "DISCUSSION_STARTED"
	MsgTypeDiscussionEnded   = "DISCUSSION_ENDED"
	MsgTypeVoteCast          = "VOTE_CAST"
	MsgTypeVotingResults     = "VOTING_RESULTS"
	MsgTypePlayerEliminated  = "PLAYER_ELIMINATED"
	MsgTypeGameOver          = "GAME_OVER"
	MsgTypePlayerJoined      = "PLAYER_JOINED"
	MsgTypePlayerLeft        = "PLAYER_LEFT"
	MsgTypePlayerReconnected = "PLAYER_RECONNECTED"
	MsgTypeError             = "ERROR"
	MsgTypePong              = "PONG"
)

// Error codes
const (
	ErrCodeNotHost         = "NOT_HOST"
	ErrCodeInvalidPhase    = "INVALID_PHASE"
	ErrCodeRoomNotFound    = "ROOM_NOT_FOUND"
	ErrCodeRoomFull        = "ROOM_FULL"
	ErrCodeGameInProgress  = "GAME_IN_PROGRESS"
	ErrCodeNameTaken       = "NAME_TAKEN"
	ErrCodeInvalidTarget   = "INVALID_TARGET"
	ErrCodeNotYourTurn     = "NOT_YOUR_TURN"
	ErrCodeInvalidPayload  = "INVALID_PAYLOAD"
	ErrCodeRateLimited     = "RATE_LIMITED"
	ErrCodeNotEnoughPlayers = "NOT_ENOUGH_PLAYERS"
)

// Client Payloads

type JoinRoomPayload struct {
	RoomCode    string `json:"roomCode"`
	DisplayName string `json:"displayName"`
}

type SetTimerPayload struct {
	Seconds int `json:"seconds"`
}

type EliminatePlayerPayload struct {
	PlayerID string `json:"playerId"`
}

type CastVotePayload struct {
	TargetPlayerID string `json:"targetPlayerId"`
}

type VoteCastPayload struct {
	VoterID       string `json:"voterId"`
	TotalVotes    int    `json:"totalVotes"`
	TotalExpected int    `json:"totalExpected"`
}

type VotingResultsPayload struct {
	Tally        map[string]int    `json:"tally"`
	Votes        map[string]string `json:"votes,omitempty"`
	EliminatedID string            `json:"eliminatedId,omitempty"`
	IsTie        bool              `json:"isTie"`
}

// Server Payloads

type RoleAssignedPayload struct {
	Role           string  `json:"role"`           // "civilian" | "imposter"
	SecretWord     *string `json:"secretWord"`     // nil for imposter
	SecretCategory string  `json:"secretCategory"` // visible to all roles
}

type RevealTurnPayload struct {
	CurrentPlayerID   string `json:"currentPlayerId"`
	CurrentPlayerName string `json:"currentPlayerName"`
	RevealIndex       int    `json:"revealIndex"`
	TotalPlayers      int    `json:"totalPlayers"`
}

type DiscussionStartedPayload struct {
	EndsAt          time.Time `json:"endsAt"`
	DurationSeconds int       `json:"durationSeconds"`
}

type PlayerEliminatedPayload struct {
	PlayerID      string   `json:"playerId"`
	DisplayName   string   `json:"displayName"`
	WasImposter   bool     `json:"wasImposter"`
	ActivePlayers []string `json:"activePlayers"`
	Phase         Phase    `json:"phase"`
}

type GameOverPayload struct {
	Winner         string              `json:"winner"`
	ImposterName   string              `json:"imposterName"`
	SecretWord     string              `json:"secretWord"`
	EliminationLog []EliminationRecord `json:"eliminationLog"`
}

type PlayerJoinedPayload struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	IsHost      bool   `json:"isHost"`
}

type PlayerLeftPayload struct {
	PlayerID    string  `json:"playerId"`
	DisplayName string  `json:"displayName"`
	NewHostID   *string `json:"newHostId,omitempty"`
}

type PlayerReconnectedPayload struct {
	PlayerID    string `json:"playerId"`
	DisplayName string `json:"displayName"`
}

type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// REST Payloads

type CreateRoomRequest struct {
	HostName string `json:"hostName,omitempty"`
}

type CreateRoomResponse struct {
	RoomCode  string    `json:"roomCode"`
	WSUrl     string    `json:"wsUrl"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type RoomInfoResponse struct {
	RoomCode    string `json:"roomCode"`
	Phase       Phase  `json:"phase"`
	PlayerCount int    `json:"playerCount"`
	MaxPlayers  int    `json:"maxPlayers"`
	Joinable    bool   `json:"joinable"`
}

// Helper to construct serialized outgoing Envelope
func NewEnvelope(msgType string, payload any, reqID string) ([]byte, error) {
	var rawPayload json.RawMessage
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		rawPayload = data
	}

	env := Envelope{
		Type:      msgType,
		Payload:   rawPayload,
		RequestID: reqID,
	}

	return json.Marshal(env)
}
