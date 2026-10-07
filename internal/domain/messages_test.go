package domain

import (
	"encoding/json"
	"testing"
)

func TestNewEnvelope(t *testing.T) {
	payload := JoinRoomPayload{
		RoomCode:    "XK92PL",
		DisplayName: "Alice",
	}

	data, err := NewEnvelope(MsgTypeJoinRoom, payload, "req-123")
	if err != nil {
		t.Fatalf("failed to serialize envelope: %v", err)
	}

	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("failed to deserialize envelope: %v", err)
	}

	if env.Type != MsgTypeJoinRoom {
		t.Errorf("expected type %s, got %s", MsgTypeJoinRoom, env.Type)
	}
	if env.RequestID != "req-123" {
		t.Errorf("expected requestID req-123, got %s", env.RequestID)
	}

	var parsed JoinRoomPayload
	if err := json.Unmarshal(env.Payload, &parsed); err != nil {
		t.Fatalf("failed to deserialize payload: %v", err)
	}
	if parsed.RoomCode != "XK92PL" || parsed.DisplayName != "Alice" {
		t.Errorf("mismatched payload fields: %+v", parsed)
	}
}

func TestPublicRoomState_ZeroLeakage(t *testing.T) {
	// Ensure PublicRoomState JSON output never contains secretWord or imposterId
	state := PublicRoomState{
		RoomCode:      "XK92PL",
		Phase:         PhaseLobby,
		HostID:        "host-uuid",
		TimerDuration: 300,
		Players: []PublicPlayer{
			{ID: "p1", DisplayName: "Alice", IsHost: true, IsActive: true, IsReady: false},
		},
		ActivePlayers:  []string{"p1"},
		EliminationLog: []EliminationRecord{},
	}

	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("failed to marshal public room state: %v", err)
	}

	raw := string(data)
	if stringContains(raw, "secretWord") {
		t.Errorf("CRITICAL SECURITY ERROR: PublicRoomState serialized secretWord")
	}
	if stringContains(raw, "imposterId") || stringContains(raw, "imposterID") {
		t.Errorf("CRITICAL SECURITY ERROR: PublicRoomState serialized imposterID")
	}
}

func stringContains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
