package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/config"
	"github.com/hostd/hostd/internal/controller"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/machines"
	"github.com/hostd/hostd/internal/runtime/docker"
)

func main() {
	cfg, err := startupConfig(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err = cfg.EnsureDataRoot(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	logger := newStructuredLogger(os.Stderr, cfg.LogLevel)
	db, err := database.Open(cfg.DataRoot)
	if err != nil {
		logger.Error("database startup failed", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	a := auth.New(db)
	if token, err := a.EnsureBootstrapToken(); err != nil {
		logger.Error("bootstrap token setup failed", "error", err)
		os.Exit(1)
	} else if token != "" {
		if err := writeBootstrapToken(os.Stdout, token); err != nil {
			logger.Error("bootstrap token console output failed", "error", err)
			os.Exit(1)
		}
	}
	m := machines.New(db)
	if _, err := m.EnsureLocal(); err != nil {
		logger.Error("local machine setup failed", "error", err)
		os.Exit(1)
	}
	diagnostic := docker.Check(context.Background(), cfg.CaddyManagement, cfg.DockerEndpoint, cfg.DataRoot)
	if err := m.UpdateLocalDiagnostics(diagnostic.DockerVersion, diagnostic.ComposeVersion, diagnostic.Resources); err != nil {
		logger.Error("local diagnostics persistence failed", "error", err)
		os.Exit(1)
	}
	j := jobs.New(db)
	if err := j.RecoverInterrupted(); err != nil {
		logger.Error("job recovery failed", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if cfg.FakeRuntime {
		go j.RunFakeWorker(ctx)
	}
	s := &http.Server{Addr: cfg.ListenAddress, Handler: (&controller.Server{Auth: a, Apps: apps.New(db), Jobs: j, Machines: m, Caddy: cfg.CaddyManagement, FakeRuntime: cfg.FakeRuntime, DockerEndpoint: cfg.DockerEndpoint, DataRoot: cfg.DataRoot, Logger: logger}).Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.Shutdown(shutdown)
	}()
	logger.Info("hostd listening", "address", cfg.ListenAddress, "fake_runtime", cfg.FakeRuntime)
	if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func startupConfig(args []string) (config.Config, error) {
	return config.FromFlags(args)
}

func newStructuredLogger(w io.Writer, logLevel string) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: parseLevel(logLevel)}))
}

func writeBootstrapToken(w io.Writer, token string) error {
	return auth.WriteBootstrapToken(w, token)
}

func parseLevel(v string) slog.Level {
	switch v {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
