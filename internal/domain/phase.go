package domain

// Phase represents the current state of a game room.
type Phase string

const (
	PhaseLobby      Phase = "lobby"      // players joining, host configuring
	PhaseRevealing  Phase = "revealing"  // private role reveal in progress
	PhaseReady      Phase = "ready"      // all players have seen roles; ready for discussion
	PhaseDiscussing Phase = "discussing" // timer running, active debate
	PhaseVoting     Phase = "voting"     // discussion ended, host picks target for elimination
	PhaseResult     Phase = "result"     // elimination result announced
	PhaseGameOver   Phase = "gameOver"   // winner determined, full game reveal
)

// IsValid checks if the phase is a known game phase.
func (p Phase) IsValid() bool {
	switch p {
	case PhaseLobby, PhaseRevealing, PhaseReady, PhaseDiscussing, PhaseVoting, PhaseResult, PhaseGameOver:
		return true
	default:
		return false
	}
}
