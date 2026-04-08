package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"nexus/internal/app"
	"nexus/internal/config"
	"nexus/internal/tracex"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "worker"))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		slog.Error("startup.config_load_failed", "error", err.Error())
		os.Exit(1)
	}
	shutdownTrace, err := tracex.Setup(ctx, "worker", cfg.OTLPEndpoint, cfg.OTELSampleRatio)
	if err != nil {
		slog.Error("startup.trace_init_failed", "error", err.Error())
		os.Exit(1)
	}
	defer func() { _ = shutdownTrace(context.Background()) }()
	application, err := app.New(ctx, cfg)
	if err != nil {
		slog.Error("startup.app_init_failed", "error", err.Error())
		os.Exit(1)
	}
	defer application.Close()

	if err := application.WorkerLoop(ctx); err != nil && ctx.Err() == nil {
		slog.Error("worker.loop_failed", "error", err.Error())
		os.Exit(1)
	}
}
