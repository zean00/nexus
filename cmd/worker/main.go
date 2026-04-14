package main

import (
	"context"
	"time"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"nexus/internal/app"
	"nexus/internal/config"
	"nexus/internal/tracex"
)

func runWorkerSupervisor(ctx context.Context, application *app.App) {
	delay := 2 * time.Second
	for {
		if err := application.WorkerLoop(ctx); err != nil && ctx.Err() == nil {
			slog.Error("worker.loop_failed", "error", err.Error())
			time.Sleep(delay)
			delay = minDuration(delay*2, 30*time.Second)
			continue
		}
		return
	}
}

func minDuration(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}

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

	runWorkerSupervisor(ctx, application)
}
