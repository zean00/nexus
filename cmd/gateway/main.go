package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"nexus/internal/app"
	"nexus/internal/config"
	"nexus/internal/tracex"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "gateway"))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		slog.Error("startup.config_load_failed", "error", err.Error())
		os.Exit(1)
	}
	shutdownTrace, err := tracex.Setup(ctx, "gateway", cfg.OTLPEndpoint, cfg.OTELSampleRatio)
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

	server := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: application.GatewayHandler(),
	}
	adminServer := &http.Server{
		Addr:    cfg.AdminAddr,
		Handler: application.AdminHandler(),
	}

	go func() {
		if err := adminServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server.admin_failed", "error", err.Error())
			os.Exit(1)
		}
	}()
	go func() {
		if err := application.WorkerLoop(ctx); err != nil && ctx.Err() == nil {
			slog.Error("worker.loop_failed", "error", err.Error())
			os.Exit(1)
		}
	}()

	go func() {
		<-ctx.Done()
		_ = server.Shutdown(context.Background())
		_ = adminServer.Shutdown(context.Background())
	}()

	slog.Info("server.started", "http_addr", cfg.HTTPAddr, "admin_addr", cfg.AdminAddr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server.gateway_failed", "error", err.Error())
		os.Exit(1)
	}
}
