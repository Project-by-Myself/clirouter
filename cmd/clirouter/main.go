package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Project-by-Myself/clirouter/internal/auth"
	"github.com/Project-by-Myself/clirouter/internal/config"
	"github.com/Project-by-Myself/clirouter/internal/server"
	"github.com/Project-by-Myself/clirouter/internal/store"
	"github.com/Project-by-Myself/clirouter/web"
)

const sampleConfig = `# clirouter configuration. Edited by hand or from the dashboard.
listen: 127.0.0.1:8317
# Client keys. Empty = no auth (fine for localhost only).
api_keys: []
#  - {name: me, key: sk-local-change-me}
timeouts:
  connect: 10s
  first_byte: 90s
  idle: 180s
  total: 60m
  early_ping: 2s
  keepalive: 15s
routing:
  strategy: round-robin
  max_attempts: 6
  session_affinity: true
  max_body_mb: 64
logging:
  level: info
  retention_days: 14
providers: []
#  - id: anthropic
#    type: anthropic
#    keys: [env:ANTHROPIC_API_KEY]
#  - id: openai
#    type: openai
#    keys: [env:OPENAI_API_KEY]
#  - id: openrouter
#    type: openai-compatible
#    base_url: https://openrouter.ai/api/v1
#    keys: [env:OPENROUTER_API_KEY]
#    models: [deepseek/deepseek-chat-v3.1]
combos: []
aliases: {}
`

func main() {
	home, _ := os.UserHomeDir()
	defPath := filepath.Join(home, ".clirouter", "config.yaml")
	path := flag.String("config", defPath, "path to config.yaml")
	listen := flag.String("listen", "", "override listen address")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if _, err := os.Stat(*path); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(*path), 0o700); err != nil {
			fatal(log, err)
		}
		if err := os.WriteFile(*path, []byte(sampleConfig), 0o600); err != nil {
			fatal(log, err)
		}
		log.Info("created default config", "path", *path)
	}
	cfg, err := config.Load(*path)
	if err != nil {
		fatal(log, err)
	}
	if *listen != "" {
		cfg.Listen = *listen
	}
	st, err := store.Open(cfg.DataDir, cfg.Logging.RetentionDays)
	if err != nil {
		fatal(log, fmt.Errorf("open store: %w", err))
	}
	am := auth.NewManager(cfg.DataDir, nil)
	am.Logger = log
	actx, acancel := context.WithCancel(context.Background())
	defer acancel()
	am.Start(actx)
	srv := server.New(*path, cfg, st, log, web.Handler(), am.TokenSource)
	srv.OAuthAdmin = am.AdminHandler()
	stop := make(chan struct{})
	go srv.Watch(stop)

	hs := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	go func() {
		log.Info("clirouter listening", "addr", cfg.Listen, "dashboard", "http://"+cfg.Listen+"/", "config", *path)
		if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatal(log, err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Info("shutting down; waiting for in-flight requests")
	close(stop)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = hs.Shutdown(ctx)
	_ = st.Close()
}

func fatal(log *slog.Logger, err error) {
	log.Error("fatal", "err", err)
	os.Exit(1)
}
