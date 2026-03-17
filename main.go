package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/avvvet/semma-api/config"
	"github.com/avvvet/semma-api/handler"
	"github.com/avvvet/semma-api/service"
	"github.com/avvvet/semma-api/store"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/go-chi/httprate"
	"github.com/joho/godotenv"
)

func main() {
	// load .env file (ignores error if file doesn't exist — safe for production)
	_ = godotenv.Load()

	// load config
	cfg := config.Load()

	// ensure db directory exists
	os.MkdirAll(filepath.Dir(cfg.DBPath), 0755)

	// init db
	store.Init(cfg.DBPath)
	defer store.Close()

	// router
	r := chi.NewRouter()

	// middleware
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))
	r.Use(middleware.CleanPath)

	// cors — allow Svelte dev and production
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: []string{
			"http://localhost:5173", // svelte dev
			"http://localhost:4173", // svelte preview
			"http://localhost:3000",
		},
		AllowedMethods: []string{"GET", "POST", "OPTIONS"},
		AllowedHeaders: []string{"Content-Type"},
		MaxAge:         300,
	}))

	// global rate limit — protect GPU
	r.Use(middleware.ThrottleBacklog(10, 20, 30*time.Second))

	// per IP rate limit — 5 requests per minute
	r.Use(httprate.LimitByIP(5, 1*time.Minute))

	// routes
	r.Post("/api/transcribe", handler.Transcribe(cfg))
	r.Get("/api/recent", handler.Recent)
	r.Get("/api/health", handler.Health(nil))

	// ── Telegram bot ──────────────────────────────────────────
	if cfg.BotToken == "" {
		log.Printf("─────────────────────────────────────────")
		log.Printf("bot: ✗ skipped — BOT_TOKEN not set in .env")
		log.Printf("─────────────────────────────────────────")
	} else {
		webhookHandler, err := service.StartBot(cfg)
		if err != nil {
			log.Fatalf("bot: %v", err)
		}
		if webhookHandler != nil {
			// production: register webhook route
			r.Post("/telegram/webhook", webhookHandler)
			log.Printf("bot: webhook registered at /telegram/webhook")
		}
	}
	// ─────────────────────────────────────────────────────────

	log.Printf("semma-api: starting on port %s", cfg.APIPort)
	log.Printf("semma-api: forwarding to %s", cfg.RuachURL)

	if err := http.ListenAndServe(":"+cfg.APIPort, r); err != nil {
		log.Fatalf("semma-api: server error: %v", err)
	}
}
