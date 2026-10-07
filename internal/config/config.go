package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds runtime configuration loaded from environment variables.
type Config struct {
	Port           string
	AllowedOrigins []string
	MaxRooms       int
	RoomTTL        time.Duration
	MaxConnPerIP   int
	LogLevel       string
	LogFormat      string
}

// Load reads configuration from the environment with default fallbacks.
func Load() *Config {
	port := getEnv("PORT", "8080")
	originsStr := getEnv("ALLOWED_ORIGINS", "*")
	var allowedOrigins []string
	for _, o := range strings.Split(originsStr, ",") {
		trimmed := strings.TrimSpace(o)
		if trimmed != "" {
			allowedOrigins = append(allowedOrigins, trimmed)
		}
	}
	if len(allowedOrigins) == 0 {
		allowedOrigins = []string{"*"}
	}

	maxRooms := getEnvInt("MAX_ROOMS", 500)
	roomTTLMinutes := getEnvInt("ROOM_TTL_MINUTES", 30)
	maxConnPerIP := getEnvInt("MAX_CONN_PER_IP", 5)
	logLevel := strings.ToLower(getEnv("LOG_LEVEL", "info"))
	logFormat := strings.ToLower(getEnv("LOG_FORMAT", "text"))

	return &Config{
		Port:           port,
		AllowedOrigins: allowedOrigins,
		MaxRooms:       maxRooms,
		RoomTTL:        time.Duration(roomTTLMinutes) * time.Minute,
		MaxConnPerIP:   maxConnPerIP,
		LogLevel:       logLevel,
		LogFormat:      logFormat,
	}
}

func getEnv(key, defaultVal string) string {
	if val, ok := os.LookupEnv(key); ok && val != "" {
		return val
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	if val, ok := os.LookupEnv(key); ok && val != "" {
		if parsed, err := strconv.Atoi(val); err == nil {
			return parsed
		}
	}
	return defaultVal
}
