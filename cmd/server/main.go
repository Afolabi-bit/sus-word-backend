package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"golang.org/x/time/rate"

	"sus-word-backend/internal/api"
	"sus-word-backend/internal/config"
	"sus-word-backend/internal/game"
	"sus-word-backend/internal/hub"
	"sus-word-backend/internal/logger"
	"sus-word-backend/internal/middleware"
	"sus-word-backend/internal/ws"
)

func main() {
	cfg := config.Load()
	log := logger.Init(cfg)

	log.Info("starting SusWord backend server",
		"port", cfg.Port,
		"maxRooms", cfg.MaxRooms,
		"roomTTL", cfg.RoomTTL.String(),
		"maxConnPerIP", cfg.MaxConnPerIP,
		"allowedOrigins", cfg.AllowedOrigins,
	)

	// Domain services
	wordSelector := game.NewWordSelector(nil)
	hubInstance := hub.NewHub(cfg.MaxRooms, cfg.RoomTTL, wordSelector)
	hubInstance.StartReaper(1 * time.Minute)

	// Handlers
	apiHandler := api.NewHandler(hubInstance, cfg)
	wsHandler := ws.NewHandler(hubInstance, cfg)

	// Rate limiters (Spec section 12)
	// Room creation: 10 rooms per IP per hour
	createRoomLimiter := middleware.NewIPRateLimiter(rate.Limit(10.0/3600.0), 10)
	// Join inspection: 30 attempts per IP per hour
	joinRoomLimiter := middleware.NewIPRateLimiter(rate.Limit(30.0/3600.0), 30)

	r := chi.NewRouter()
	r.Use(chiMiddleware.RequestID)
	r.Use(chiMiddleware.RealIP)
	r.Use(chiMiddleware.Recoverer)
	r.Use(middleware.CORS(cfg))

	// WebSocket upgrade route
	r.Get("/ws", wsHandler.ServeHTTP)

	// REST API routes with per-IP rate limiting
	r.With(createRoomLimiter.LimitMiddleware("Room creation rate limit exceeded (max 10/hour)")).
		Post("/api/rooms", apiHandler.HandleCreateRoom)

	r.With(joinRoomLimiter.LimitMiddleware("Room inspection rate limit exceeded (max 30/hour)")).
		Get("/api/rooms/{code}", apiHandler.HandleGetRoom)

	r.Get("/api/health", apiHandler.HandleHealth)

	server := &http.Server{
		Addr:         fmt.Sprintf(":%s", cfg.Port),
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	serverErrors := make(chan error, 1)
	go func() {
		log.Info("HTTP and WebSocket server listening", "addr", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
	}()

	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-serverErrors:
		log.Error("server fatal error", "error", err)
		os.Exit(1)
	case sig := <-shutdown:
		log.Info("shutdown signal received", "signal", sig.String())

		// Stop hub and close connections
		hubInstance.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := server.Shutdown(ctx); err != nil {
			log.Error("graceful server shutdown failed", "error", err)
			_ = server.Close()
			os.Exit(1)
		}
		log.Info("server exited cleanly")
	}
}
