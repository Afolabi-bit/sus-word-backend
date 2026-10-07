package config

import (
	"os"
	"testing"
	"time"
)

func TestLoad_Defaults(t *testing.T) {
	// Clear relevant env vars
	os.Unsetenv("PORT")
	os.Unsetenv("ALLOWED_ORIGINS")
	os.Unsetenv("MAX_ROOMS")
	os.Unsetenv("ROOM_TTL_MINUTES")
	os.Unsetenv("MAX_CONN_PER_IP")

	cfg := Load()

	if cfg.Port != "8080" {
		t.Errorf("expected default Port to be 8080, got %s", cfg.Port)
	}
	if len(cfg.AllowedOrigins) != 1 || cfg.AllowedOrigins[0] != "*" {
		t.Errorf("expected default AllowedOrigins to be [*], got %v", cfg.AllowedOrigins)
	}
	if cfg.MaxRooms != 500 {
		t.Errorf("expected default MaxRooms to be 500, got %d", cfg.MaxRooms)
	}
	if cfg.RoomTTL != 30*time.Minute {
		t.Errorf("expected default RoomTTL to be 30m, got %v", cfg.RoomTTL)
	}
	if cfg.MaxConnPerIP != 5 {
		t.Errorf("expected default MaxConnPerIP to be 5, got %d", cfg.MaxConnPerIP)
	}
}

func TestLoad_CustomEnv(t *testing.T) {
	os.Setenv("PORT", "9000")
	os.Setenv("ALLOWED_ORIGINS", "http://localhost:3000, https://example.com")
	os.Setenv("MAX_ROOMS", "100")
	os.Setenv("ROOM_TTL_MINUTES", "45")
	os.Setenv("MAX_CONN_PER_IP", "10")
	defer func() {
		os.Unsetenv("PORT")
		os.Unsetenv("ALLOWED_ORIGINS")
		os.Unsetenv("MAX_ROOMS")
		os.Unsetenv("ROOM_TTL_MINUTES")
		os.Unsetenv("MAX_CONN_PER_IP")
	}()

	cfg := Load()

	if cfg.Port != "9000" {
		t.Errorf("expected Port 9000, got %s", cfg.Port)
	}
	if len(cfg.AllowedOrigins) != 2 || cfg.AllowedOrigins[0] != "http://localhost:3000" || cfg.AllowedOrigins[1] != "https://example.com" {
		t.Errorf("expected parsed origins, got %v", cfg.AllowedOrigins)
	}
	if cfg.MaxRooms != 100 {
		t.Errorf("expected MaxRooms 100, got %d", cfg.MaxRooms)
	}
	if cfg.RoomTTL != 45*time.Minute {
		t.Errorf("expected RoomTTL 45m, got %v", cfg.RoomTTL)
	}
	if cfg.MaxConnPerIP != 10 {
		t.Errorf("expected MaxConnPerIP 10, got %d", cfg.MaxConnPerIP)
	}
}
