package player

import (
	"testing"
	"time"

	"sus-word-backend/internal/domain"
)

type mockDispatcher struct {
	inboundCalls    int
	disconnectCalls int
	lastEnvelope    domain.Envelope
}

func (m *mockDispatcher) HandleInbound(sender *Player, env domain.Envelope) {
	m.inboundCalls++
	m.lastEnvelope = env
}

func (m *mockDispatcher) HandleDisconnect(sender *Player) {
	m.disconnectCalls++
}

func TestPlayer_ErrorThresholdDisconnect(t *testing.T) {
	p := NewPlayer("p-1", "Alice", "XK92PL", true, nil, nil)

	for i := 1; i <= 9; i++ {
		count := p.RecordError()
		if count != int32(i) {
			t.Errorf("expected count %d, got %d", i, count)
		}
		if p.IsClosed() {
			t.Errorf("player should not be closed at error count %d", i)
		}
	}

	// 10th error triggers close
	count := p.RecordError()
	if count != 10 {
		t.Errorf("expected count 10, got %d", count)
	}
	if !p.IsClosed() {
		t.Errorf("player should be closed when reaching threshold of 10 errors")
	}
}

func TestPlayer_SuccessResetsErrors(t *testing.T) {
	p := NewPlayer("p-1", "Alice", "XK92PL", true, nil, nil)

	p.RecordError()
	p.RecordError()
	if p.errorCount.Load() != 2 {
		t.Errorf("expected error count 2, got %d", p.errorCount.Load())
	}

	p.RecordSuccess()
	if p.errorCount.Load() != 0 {
		t.Errorf("expected error count reset to 0, got %d", p.errorCount.Load())
	}
}

func TestPlayer_SendBufferFull(t *testing.T) {
	p := NewPlayer("p-1", "Alice", "XK92PL", true, nil, nil)

	// Fill send buffer (capacity 64)
	for i := 0; i < sendBufferSize; i++ {
		ok := p.Send([]byte("test"))
		if !ok {
			t.Fatalf("expected send to succeed for item %d", i)
		}
	}

	// 65th send should fail and mark player closed
	ok := p.Send([]byte("overflow"))
	if ok {
		t.Errorf("expected overflow send to fail")
	}
	if !p.IsClosed() {
		t.Errorf("expected player to be marked closed on buffer overflow")
	}
}

func TestPlayer_RateLimiter(t *testing.T) {
	p := NewPlayer("p-1", "Alice", "XK92PL", true, nil, nil)

	// Rate limiter allows 20 tokens burst
	allowedCount := 0
	for i := 0; i < 30; i++ {
		if p.limiter.Allow() {
			allowedCount++
		}
	}

	if allowedCount != 20 {
		t.Errorf("expected burst of 20 allowed, got %d", allowedCount)
	}

	// Wait 100ms for tokens to refill (at 20/s, 100ms gives ~2 tokens)
	time.Sleep(120 * time.Millisecond)
	if !p.limiter.Allow() {
		t.Errorf("expected token replenishment after sleep")
	}
}
