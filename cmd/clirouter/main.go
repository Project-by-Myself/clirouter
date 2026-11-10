package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/Project-by-Myself/clirouter/internal/auth"
	"github.com/Project-by-Myself/clirouter/internal/config"
	"github.com/Project-by-Myself/clirouter/internal/pricing"
	"github.com/Project-by-Myself/clirouter/internal/server"
	"github.com/Project-by-Myself/clirouter/internal/store"
	"github.com/Project-by-Myself/clirouter/web"
)

const sampleConfig = `# clirouter configuration. Edited by hand or from the dashboard.
listen: 127.0.0.1:8317
# Client keys. Empty = only requests from this machine are accepted.
# Keys added from the dashboard are stored as sha256:<hex> digests.
api_keys: []
# Admin password for the dashboard / admin API. Empty = admin only from
# localhost. Generated automatically when listen is not a loopback address.
# admin_password: env:CLIROUTER_ADMIN_PASSWORD
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
	// --listen overrides the address for this run only; cfg keeps the file's
	// value so a dashboard save does not persist the override.
	addr := cfg.Listen
	if *listen != "" {
		addr = *listen
	}
	fixPerms(log, *path, cfg.DataDir)
	exposed := !server.IsLoopbackListen(addr)
	if exposed && config.Secret(cfg.AdminPassword) == "" {
		if cfg.AdminPassword != "" {
			fatal(log, fmt.Errorf("admin_password refers to %s, which is empty; refusing to expose the admin API on %s", cfg.AdminPassword, addr))
		}
		pw, err := randomSecret()
		if err != nil {
			fatal(log, err)
		}
		if err := config.SetTopLevel(*path, "admin_password", pw); err != nil {
			fatal(log, fmt.Errorf("store generated admin_password: %w", err))
		}
		cfg.AdminPassword = pw
		log.Warn("listening on a non-loopback address: generated a random admin_password for the dashboard; read it from the config file", "config", *path)
	}
	if exposed && len(cfg.APIKeys) == 0 {
		log.Warn("no api_keys configured: the inference API only accepts requests from localhost; add client keys to serve other machines")
	}
	st, err := store.Open(cfg.DataDir, cfg.Logging.RetentionDays)
	if err != nil {
		fatal(log, fmt.Errorf("open store: %w", err))
	}
	// One-time: price existing request history and build the usage rollup.
	ptab := pricing.New(cfg.Pricing)
	if err := st.Backfill(context.Background(), func(provider, up, model string, in, out, cr, cw int64) float64 {
		return ptab.Cost(provider, up, model, pricing.Usage{Input: in, Output: out, CacheRead: cr, CacheWrite: cw})
	}); err != nil {
		log.Warn("usage backfill failed", "err", err)
	}
	am := auth.NewManager(cfg.DataDir, nil)
	am.Logger = log
	actx, acancel := context.WithCancel(context.Background())
	defer acancel()
	am.Start(actx)
	srv := server.New(*path, cfg, st, log, web.Handler(), am.TokenSource)
	srv.OAuthAdmin = am.AdminHandler()
	srv.Exposed = exposed
	srv.ListenAddr = addr
	stop := make(chan struct{})
	go srv.Watch(stop)

	hs := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	go func() {
		log.Info("clirouter listening", "addr", addr, "dashboard", "http://"+addr+"/", "config", *path)
		if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatal(log, err)
		}
	}()

	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Info("shutting down; waiting up to 30s for in-flight requests (signal again to force)")
	close(stop)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go func() {
		select {
		case <-sig:
			log.Warn("second signal: closing connections now")
			cancel()
		case <-ctx.Done():
		}
	}()
	if err := hs.Shutdown(ctx); err != nil {
		// Drain deadline passed (long streams): cut the remaining connections
		// so handlers observe cancellation and record their requests.
		_ = hs.Close()
		time.Sleep(200 * time.Millisecond)
	}
	acancel()
	_ = st.Close()
}

func randomSecret() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// fixPerms tightens permissions of files that hold secrets, in case they
// were created by hand or by an older version with a looser umask.
func fixPerms(log *slog.Logger, cfgPath, dataDir string) {
	if runtime.GOOS == "windows" {
		return
	}
	tighten := func(p string, mode os.FileMode) {
		fi, err := os.Stat(p)
		if err != nil || fi.Mode().Perm()&^mode == 0 {
			return
		}
		if err := os.Chmod(p, mode); err != nil {
			log.Warn("cannot restrict permissions", "path", p, "err", err)
			return
		}
		log.Warn("restricted permissions (file holds secrets)", "path", p, "from", fi.Mode().Perm(), "to", mode)
	}
	tighten(cfgPath, 0o600)
	tighten(dataDir, 0o700)
	tighten(filepath.Join(dataDir, "auth"), 0o700)
	for _, f := range []string{"clirouter.db", "clirouter.db-wal", "clirouter.db-shm"} {
		tighten(filepath.Join(dataDir, f), 0o600)
	}
}

func fatal(log *slog.Logger, err error) {
	log.Error("fatal", "err", err)
	os.Exit(1)
}
