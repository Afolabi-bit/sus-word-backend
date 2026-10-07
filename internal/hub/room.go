package hub

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"sus-word-backend/internal/domain"
	"sus-word-backend/internal/game"
	"sus-word-backend/internal/player"
)

const (
	// AbandonGracePeriod: Room deleted if 0 connected players for this long.
	abandonGracePeriod = 5 * time.Minute
	// ReconnectGraceWindow: Window to reconnect without losing role state.
	reconnectGraceWindow = 60 * time.Second
)

// Room manages state synchronization and message routing for a game session.
type Room struct {
	Code         string
	state        *game.RoomDataWithState
	players      map[string]*player.Player // connected sockets keyed by PlayerID
	wordSelector *game.WordSelector
	inbound      chan roomEvent

	discussionTimer *time.Timer
	abandonTimer    *time.Timer
	onRoomClosed    func(code string)

	mu     sync.RWMutex
	closed bool
}

// NewRoom creates a Room actor instance.
func NewRoom(
	code string,
	hostID string,
	hostName string,
	ws *game.WordSelector,
	onRoomClosed func(code string),
) *Room {
	state := game.NewRoomData(code, hostID, hostName, 300)

	r := &Room{
		Code:         code,
		state:        state,
		players:      make(map[string]*player.Player),
		wordSelector: ws,
		inbound:      make(chan roomEvent, 128),
		onRoomClosed: onRoomClosed,
	}

	return r
}

// Start launches the Room actor goroutine.
func (r *Room) Start() {
	go r.run()
}

// HandleInbound implements player.InboundDispatcher.
func (r *Room) HandleInbound(sender *player.Player, env domain.Envelope) {
	r.inbound <- roomEvent{
		eventType: eventClientAction,
		player:    sender,
		envelope:  env,
	}
}

// HandleDisconnect implements player.InboundDispatcher.
func (r *Room) HandleDisconnect(sender *player.Player) {
	r.inbound <- roomEvent{
		eventType: eventPlayerLeave,
		player:    sender,
	}
}

// JoinPlayer queues a player join request and blocks until processed.
func (r *Room) JoinPlayer(p *player.Player) error {
	resp := make(chan error, 1)
	r.inbound <- roomEvent{
		eventType: eventPlayerJoin,
		player:    p,
		joinResp:  resp,
	}
	return <-resp
}

// Close terminates the room actor loop and cleans up connections.
func (r *Room) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	r.mu.Unlock()

	r.inbound <- roomEvent{eventType: eventStop}
}

// run is the single authoritative event loop goroutine for this room.
func (r *Room) run() {
	slog.Info("room actor started", "roomCode", r.Code)

	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("CRITICAL: room actor recovered from panic", "roomCode", r.Code, "panic", rec)
		}

		if r.discussionTimer != nil {
			r.discussionTimer.Stop()
		}
		if r.abandonTimer != nil {
			r.abandonTimer.Stop()
		}

		for _, p := range r.players {
			p.Close()
		}

		slog.Info("room actor stopped", "roomCode", r.Code)
		if r.onRoomClosed != nil {
			r.onRoomClosed(r.Code)
		}
	}()

	for ev := range r.inbound {
		switch ev.eventType {
		case eventPlayerJoin:
			r.handleJoin(ev.player, ev.joinResp)

		case eventPlayerLeave:
			r.handleLeave(ev.player)

		case eventClientAction:
			r.handleAction(ev.player, ev.envelope)

		case eventTimerExpired:
			r.handleTimerExpired()

		case eventAbandonTimeout:
			slog.Info("room abandonment grace period elapsed, closing room", "roomCode", r.Code)
			return

		case eventStop:
			return
		}
	}
}

// handleJoin processes new connections or reconnecting players.
func (r *Room) handleJoin(p *player.Player, resp chan error) {
	// Cancel abandonment timer if running
	if r.abandonTimer != nil {
		r.abandonTimer.Stop()
		r.abandonTimer = nil
	}

	// 1. Check for reconnecting player matching DisplayName
	var matchedBase *domain.BasePlayer
	for _, bp := range r.state.Players {
		if bp.DisplayName == p.DisplayName {
			matchedBase = bp
			break
		}
	}

	if matchedBase != nil {
		// Existing name found
		if existingConn, active := r.players[matchedBase.ID]; active && !existingConn.IsClosed() {
			resp <- fmt.Errorf("%s: %s", domain.ErrCodeNameTaken, "Display name already in use in this room")
			return
		}

		// Reconnect successful: inherit player identity
		p.ID = matchedBase.ID
		p.IsHost = matchedBase.IsHost
		p.IsActive = matchedBase.IsActive
		p.IsReady = matchedBase.IsReady
		matchedBase.DisconnectedAt = nil

		r.players[p.ID] = p

		slog.Info("player reconnected", "roomCode", r.Code, "playerId", p.ID, "displayName", p.DisplayName)

		// Broadcast reconnected notification
		r.broadcast(domain.MsgTypePlayerReconnected, domain.PlayerReconnectedPayload{
			PlayerID:    p.ID,
			DisplayName: p.DisplayName,
		})

		// Send full ROOM_STATE to the reconnected player
		_ = p.SendEnvelope(domain.MsgTypeRoomState, r.state.ToPublic(), "")

		// Re-send private ROLE_ASSIGNED if game is underway
		if r.state.Phase != domain.PhaseLobby {
			r.sendPrivateRole(p)
		}

		resp <- nil
		return
	}

	// 2. Joining as a new player
	if r.state.Phase != domain.PhaseLobby {
		resp <- fmt.Errorf("%s: %s", domain.ErrCodeGameInProgress, "Game is already in progress")
		return
	}

	if len(r.players) >= game.MaxPlayers {
		resp <- fmt.Errorf("%s: %s", domain.ErrCodeRoomFull, "Room is full (maximum 10 players)")
		return
	}

	// If this is the very first player joining, make them host
	if len(r.players) == 0 && r.state.HostID == "" {
		p.IsHost = true
		r.state.HostID = p.ID
	}

	baseP := &domain.BasePlayer{
		ID:          p.ID,
		DisplayName: p.DisplayName,
		RoomCode:    r.Code,
		ConnectedAt: time.Now(),
		IsHost:      p.IsHost,
		IsActive:    true,
		IsReady:     false,
	}

	r.state.Players[p.ID] = baseP
	r.state.PlayerOrder = append(r.state.PlayerOrder, p.ID)
	r.players[p.ID] = p

	slog.Info("new player joined", "roomCode", r.Code, "playerId", p.ID, "displayName", p.DisplayName, "isHost", p.IsHost)

	// Broadcast player joined to room
	r.broadcast(domain.MsgTypePlayerJoined, domain.PlayerJoinedPayload{
		ID:          p.ID,
		DisplayName: p.DisplayName,
		IsHost:      p.IsHost,
	})

	// Broadcast updated room state
	r.broadcastRoomState()

	resp <- nil
}

// handleLeave processes socket disconnects and elects new host if needed.
func (r *Room) handleLeave(p *player.Player) {
	if _, ok := r.players[p.ID]; !ok {
		return
	}

	delete(r.players, p.ID)

	now := time.Now()
	if bp, ok := r.state.Players[p.ID]; ok {
		bp.DisconnectedAt = &now
	}

	slog.Info("player disconnected", "roomCode", r.Code, "playerId", p.ID, "displayName", p.DisplayName)

	var newHostID *string

	// If the disconnecting player was host, elect next connected player
	if p.IsHost {
		p.IsHost = false
		if bp, ok := r.state.Players[p.ID]; ok {
			bp.IsHost = false
		}

		for _, pid := range r.state.PlayerOrder {
			if nextConn, ok := r.players[pid]; ok && !nextConn.IsClosed() {
				nextConn.IsHost = true
				if nextBase, exists := r.state.Players[pid]; exists {
					nextBase.IsHost = true
				}
				r.state.HostID = pid
				newHostID = &pid
				slog.Info("elected new room host", "roomCode", r.Code, "newHostId", pid)
				break
			}
		}
	}

	// Broadcast PLAYER_LEFT
	r.broadcast(domain.MsgTypePlayerLeft, domain.PlayerLeftPayload{
		PlayerID:    p.ID,
		DisplayName: p.DisplayName,
		NewHostID:   newHostID,
	})

	// Broadcast updated ROOM_STATE
	r.broadcastRoomState()

	// If no connected players remain, initiate abandonment countdown
	if len(r.players) == 0 {
		slog.Warn("room has 0 connected players, starting abandonment timer", "roomCode", r.Code, "duration", abandonGracePeriod)
		r.abandonTimer = time.AfterFunc(abandonGracePeriod, func() {
			r.inbound <- roomEvent{eventType: eventAbandonTimeout}
		})
	}
}

// handleAction routes client-sent actions to game state transitions.
func (r *Room) handleAction(p *player.Player, env domain.Envelope) {
	reqID := env.RequestID

	switch env.Type {
	case domain.MsgTypePing:
		_ = p.SendEnvelope(domain.MsgTypePong, nil, reqID)

	case domain.MsgTypeSetTimer:
		r.handleSetTimer(p, env.Payload, reqID)

	case domain.MsgTypeStartGame:
		r.handleStartGame(p, reqID)

	case domain.MsgTypePlayerReady:
		r.handlePlayerReady(p, reqID)

	case domain.MsgTypeStartDiscussion:
		r.handleStartDiscussion(p, reqID)

	case domain.MsgTypeEndDiscussion:
		r.handleEndDiscussion(p, reqID)

	case domain.MsgTypeEliminatePlayer:
		r.handleEliminatePlayer(p, env.Payload, reqID)

	case domain.MsgTypeCastVote:
		r.handleCastVote(p, env.Payload, reqID)

	case domain.MsgTypeNextRound:
		r.handleNextRound(p, reqID)

	case domain.MsgTypePlayAgain:
		r.handlePlayAgain(p, reqID)

	case domain.MsgTypeNewGame:
		r.handleNewGame(p, reqID)

	default:
		p.RecordError()
		r.sendError(p, domain.ErrCodeInvalidPayload, fmt.Sprintf("Unknown action type: %s", env.Type), reqID)
	}
}

func (r *Room) handleSetTimer(p *player.Player, payloadRaw json.RawMessage, reqID string) {
	if !p.IsHost {
		p.RecordError()
		r.sendError(p, domain.ErrCodeNotHost, "Only the host can set the timer", reqID)
		return
	}

	var payload domain.SetTimerPayload
	if err := json.Unmarshal(payloadRaw, &payload); err != nil {
		p.RecordError()
		r.sendError(p, domain.ErrCodeInvalidPayload, "Invalid timer payload", reqID)
		return
	}

	if err := r.state.SetTimerDuration(payload.Seconds); err != nil {
		p.RecordError()
		r.sendError(p, domain.ErrCodeInvalidPhase, err.Error(), reqID)
		return
	}

	p.RecordSuccess()
	r.broadcastRoomState()
}

func (r *Room) handleStartGame(p *player.Player, reqID string) {
	if !p.IsHost {
		p.RecordError()
		r.sendError(p, domain.ErrCodeNotHost, "Only the host can start the game", reqID)
		return
	}

	roles, err := r.state.StartGame(r.wordSelector)
	if err != nil {
		p.RecordError()
		r.sendError(p, domain.ErrCodeInvalidPhase, err.Error(), reqID)
		return
	}

	p.RecordSuccess()

	// 1. Send targeted ROLE_ASSIGNED messages to each player connection
	for pid, rolePayload := range roles {
		if targetConn, ok := r.players[pid]; ok {
			_ = targetConn.SendEnvelope(domain.MsgTypeRoleAssigned, rolePayload, "")
		}
	}

	// 2. Broadcast sanitized ROOM_STATE
	r.broadcastRoomState()

	// 3. Broadcast first REVEAL_TURN
	if playerBase, revealIdx, total, err := r.state.CurrentRevealPlayer(); err == nil {
		r.broadcast(domain.MsgTypeRevealTurn, domain.RevealTurnPayload{
			CurrentPlayerID:   playerBase.ID,
			CurrentPlayerName: playerBase.DisplayName,
			RevealIndex:       revealIdx,
			TotalPlayers:      total,
		})
	}
}

func (r *Room) handlePlayerReady(p *player.Player, reqID string) {
	allReady, nextPlayer, err := r.state.AdvanceReveal(p.ID)
	if err != nil {
		p.RecordError()
		r.sendError(p, domain.ErrCodeNotYourTurn, err.Error(), reqID)
		return
	}

	p.RecordSuccess()

	if allReady {
		// All players have viewed words; ready for host to start discussion
		r.broadcastRoomState()
	} else if nextPlayer != nil {
		// Announce next player's reveal turn
		r.broadcast(domain.MsgTypeRevealTurn, domain.RevealTurnPayload{
			CurrentPlayerID:   nextPlayer.ID,
			CurrentPlayerName: nextPlayer.DisplayName,
			RevealIndex:       r.state.RevealIndex,
			TotalPlayers:      len(r.state.RevealOrder),
		})
	}
}

func (r *Room) handleStartDiscussion(p *player.Player, reqID string) {
	if !p.IsHost {
		p.RecordError()
		r.sendError(p, domain.ErrCodeNotHost, "Only the host can start the discussion", reqID)
		return
	}

	endsAt, err := r.state.StartDiscussion(time.Now())
	if err != nil {
		p.RecordError()
		r.sendError(p, domain.ErrCodeInvalidPhase, err.Error(), reqID)
		return
	}

	p.RecordSuccess()

	// Schedule server-authoritative timer
	if r.discussionTimer != nil {
		r.discussionTimer.Stop()
	}
	duration := time.Duration(r.state.TimerDuration) * time.Second
	r.discussionTimer = time.AfterFunc(duration, func() {
		r.inbound <- roomEvent{eventType: eventTimerExpired}
	})

	// Broadcast DISCUSSION_STARTED and updated ROOM_STATE
	r.broadcast(domain.MsgTypeDiscussionStarted, domain.DiscussionStartedPayload{
		EndsAt:          endsAt,
		DurationSeconds: r.state.TimerDuration,
	})
	r.broadcastRoomState()
}

func (r *Room) handleEndDiscussion(p *player.Player, reqID string) {
	if !p.IsHost {
		p.RecordError()
		r.sendError(p, domain.ErrCodeNotHost, "Only the host can end discussion early", reqID)
		return
	}
	if r.state.Phase != domain.PhaseDiscussing {
		p.RecordError()
		r.sendError(p, domain.ErrCodeInvalidPhase, "Discussion can only end from discussing phase", reqID)
		return
	}

	p.RecordSuccess()
	r.handleTimerExpired()
}

func (r *Room) handleTimerExpired() {
	if r.state.Phase != domain.PhaseDiscussing {
		return
	}

	slog.Info("discussion timer expired server-side", "roomCode", r.Code)
	_ = r.state.EndDiscussion()

	r.broadcast(domain.MsgTypeDiscussionEnded, nil)
	r.broadcastRoomState()
}

func (r *Room) handleEliminatePlayer(p *player.Player, payloadRaw json.RawMessage, reqID string) {
	if !p.IsHost {
		p.RecordError()
		r.sendError(p, domain.ErrCodeNotHost, "Only the host can eliminate players", reqID)
		return
	}

	var payload domain.EliminatePlayerPayload
	if err := json.Unmarshal(payloadRaw, &payload); err != nil {
		p.RecordError()
		r.sendError(p, domain.ErrCodeInvalidPayload, "Invalid eliminate payload", reqID)
		return
	}

	record, gameOver, err := r.state.EliminatePlayer(payload.PlayerID)
	if err != nil {
		p.RecordError()
		r.sendError(p, domain.ErrCodeInvalidTarget, err.Error(), reqID)
		return
	}

	p.RecordSuccess()

	// Broadcast PLAYER_ELIMINATED
	r.broadcast(domain.MsgTypePlayerEliminated, domain.PlayerEliminatedPayload{
		PlayerID:      record.PlayerID,
		DisplayName:   record.DisplayName,
		WasImposter:   record.WasImposter,
		ActivePlayers: r.state.ActivePlayerIDs,
		Phase:         r.state.Phase,
	})

	if gameOver {
		// Imposter identity revealed ONLY on GAME_OVER
		imposterName := ""
		if imposterBase, ok := r.state.Players[r.state.ImposterID]; ok {
			imposterName = imposterBase.DisplayName
		}

		r.broadcast(domain.MsgTypeGameOver, domain.GameOverPayload{
			Winner:         r.state.Winner,
			ImposterName:   imposterName,
			SecretWord:     r.state.SecretWord,
			EliminationLog: r.state.EliminationLog,
		})
	}

	r.broadcastRoomState()
}

func (r *Room) handleCastVote(p *player.Player, payloadRaw json.RawMessage, reqID string) {
	var payload domain.CastVotePayload
	if err := json.Unmarshal(payloadRaw, &payload); err != nil {
		p.RecordError()
		r.sendError(p, domain.ErrCodeInvalidPayload, "Invalid cast vote payload", reqID)
		return
	}

	allVoted, err := r.state.CastVote(p.ID, payload.TargetPlayerID)
	if err != nil {
		p.RecordError()
		r.sendError(p, domain.ErrCodeInvalidTarget, err.Error(), reqID)
		return
	}

	p.RecordSuccess()

	// Broadcast VOTE_CAST progress to all players (without revealing target)
	r.broadcast(domain.MsgTypeVoteCast, domain.VoteCastPayload{
		VoterID:       p.ID,
		TotalVotes:    len(r.state.Votes),
		TotalExpected: len(r.state.ActivePlayerIDs),
	})
	r.broadcastRoomState()

	// If all active players have cast their ballots, finalize voting automatically
	if allVoted {
		r.finalizeVoting()
	}
}

func (r *Room) finalizeVoting() {
	if r.state.Phase != domain.PhaseVoting {
		return
	}

	winnerID, isTie, tally := r.state.TallyVotes()

	// Prepare votes breakdown map (voterName -> targetName) for reveal
	voteBreakdown := make(map[string]string)
	for voterID, targetID := range r.state.Votes {
		voterName := voterID
		if vp, ok := r.state.Players[voterID]; ok {
			voterName = vp.DisplayName
		}
		targetName := targetID
		if tp, ok := r.state.Players[targetID]; ok {
			targetName = tp.DisplayName
		}
		voteBreakdown[voterName] = targetName
	}

	r.broadcast(domain.MsgTypeVotingResults, domain.VotingResultsPayload{
		Tally:        tally,
		Votes:        voteBreakdown,
		EliminatedID: winnerID,
		IsTie:        isTie,
	})

	if winnerID != "" && !isTie {
		record, gameOver, err := r.state.EliminatePlayer(winnerID)
		if err == nil {
			r.broadcast(domain.MsgTypePlayerEliminated, domain.PlayerEliminatedPayload{
				PlayerID:      record.PlayerID,
				DisplayName:   record.DisplayName,
				WasImposter:   record.WasImposter,
				ActivePlayers: r.state.ActivePlayerIDs,
				Phase:         r.state.Phase,
			})

			if gameOver {
				imposterName := ""
				if imposterBase, ok := r.state.Players[r.state.ImposterID]; ok {
					imposterName = imposterBase.DisplayName
				}

				r.broadcast(domain.MsgTypeGameOver, domain.GameOverPayload{
					Winner:         r.state.Winner,
					ImposterName:   imposterName,
					SecretWord:     r.state.SecretWord,
					EliminationLog: r.state.EliminationLog,
				})
			}
		}
	} else if isTie {
		// On tie, transition to result phase with tie state
		r.state.Phase = domain.PhaseResult
	}

	r.broadcastRoomState()
}

func (r *Room) handleNextRound(p *player.Player, reqID string) {
	if !p.IsHost {
		p.RecordError()
		r.sendError(p, domain.ErrCodeNotHost, "Only the host can advance to next round", reqID)
		return
	}

	endsAt, err := r.state.NextRound(time.Now())
	if err != nil {
		p.RecordError()
		r.sendError(p, domain.ErrCodeInvalidPhase, err.Error(), reqID)
		return
	}

	p.RecordSuccess()

	if r.discussionTimer != nil {
		r.discussionTimer.Stop()
	}
	duration := time.Duration(r.state.TimerDuration) * time.Second
	r.discussionTimer = time.AfterFunc(duration, func() {
		r.inbound <- roomEvent{eventType: eventTimerExpired}
	})

	r.broadcast(domain.MsgTypeDiscussionStarted, domain.DiscussionStartedPayload{
		EndsAt:          endsAt,
		DurationSeconds: r.state.TimerDuration,
	})
	r.broadcastRoomState()
}

func (r *Room) handlePlayAgain(p *player.Player, reqID string) {
	if !p.IsHost {
		p.RecordError()
		r.sendError(p, domain.ErrCodeNotHost, "Only the host can start a new match", reqID)
		return
	}

	roles, err := r.state.PlayAgain(r.wordSelector)
	if err != nil {
		p.RecordError()
		r.sendError(p, domain.ErrCodeInvalidPhase, err.Error(), reqID)
		return
	}

	p.RecordSuccess()

	for pid, rolePayload := range roles {
		if targetConn, ok := r.players[pid]; ok {
			_ = targetConn.SendEnvelope(domain.MsgTypeRoleAssigned, rolePayload, "")
		}
	}

	r.broadcastRoomState()

	if playerBase, revealIdx, total, err := r.state.CurrentRevealPlayer(); err == nil {
		r.broadcast(domain.MsgTypeRevealTurn, domain.RevealTurnPayload{
			CurrentPlayerID:   playerBase.ID,
			CurrentPlayerName: playerBase.DisplayName,
			RevealIndex:       revealIdx,
			TotalPlayers:      total,
		})
	}
}

func (r *Room) handleNewGame(p *player.Player, reqID string) {
	if !p.IsHost {
		p.RecordError()
		r.sendError(p, domain.ErrCodeNotHost, "Only the host can reset to lobby", reqID)
		return
	}

	if err := r.state.NewGame(); err != nil {
		p.RecordError()
		r.sendError(p, domain.ErrCodeInvalidPhase, err.Error(), reqID)
		return
	}

	p.RecordSuccess()
	r.broadcastRoomState()
}

// sendPrivateRole retransmits individual role payload to a reconnecting player.
func (r *Room) sendPrivateRole(p *player.Player) {
	if p.ID == r.state.ImposterID {
		_ = p.SendEnvelope(domain.MsgTypeRoleAssigned, domain.RoleAssignedPayload{
			Role:           "imposter",
			SecretWord:     nil,
			SecretCategory: r.state.SecretCategory,
		}, "")
	} else {
		secret := r.state.SecretWord
		_ = p.SendEnvelope(domain.MsgTypeRoleAssigned, domain.RoleAssignedPayload{
			Role:           "civilian",
			SecretWord:     &secret,
			SecretCategory: r.state.SecretCategory,
		}, "")
	}
}

// broadcast fans out an envelope to all connected players.
func (r *Room) broadcast(msgType string, payload any) {
	data, err := domain.NewEnvelope(msgType, payload, "")
	if err != nil {
		slog.Error("failed to serialize broadcast", "type", msgType, "error", err)
		return
	}

	for _, p := range r.players {
		p.Send(data)
	}
}

// broadcastRoomState serializes sanitized PublicRoomState and broadcasts it.
func (r *Room) broadcastRoomState() {
	r.broadcast(domain.MsgTypeRoomState, r.state.ToPublic())
}

// sendError sends a targeted error envelope to a specific player connection.
func (r *Room) sendError(p *player.Player, code, message, reqID string) {
	_ = p.SendEnvelope(domain.MsgTypeError, domain.ErrorPayload{
		Code:    code,
		Message: message,
	}, reqID)
}

// Snapshot returns the current read-only snapshot of room data.
func (r *Room) Snapshot() domain.PublicRoomState {
	return r.state.ToPublic()
}

// EndDiscussion triggers transition to voting (e.g. host ended discussion or test automation).
func (r *Room) EndDiscussion() {
	r.inbound <- roomEvent{eventType: eventTimerExpired}
}
