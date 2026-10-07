package middleware

import (
	"net/http"
	"strings"

	"sus-word-backend/internal/config"
)

// CORS returns a middleware that manages Cross-Origin Resource Sharing.
func CORS(cfg *config.Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			allowOrigin := ""
			for _, allowed := range cfg.AllowedOrigins {
				if allowed == "*" {
					allowOrigin = "*"
					break
				}
				if strings.EqualFold(origin, allowed) {
					allowOrigin = origin
					break
				}
			}

			if allowOrigin != "" {
				w.Header().Set("Access-Control-Allow-Origin", allowOrigin)
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
				w.Header().Set("Access-Control-Max-Age", "86400")
				if allowOrigin != "*" {
					w.Header().Set("Access-Control-Allow-Credentials", "true")
				}
			}

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
