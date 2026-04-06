package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"syscall"

	"nexus/internal/app"
	"nexus/internal/config"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	application, err := app.New(ctx, cfg)
	if err != nil {
		log.Fatal(err)
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
			log.Fatal(err)
		}
	}()
	go func() {
		if err := application.WorkerLoop(ctx); err != nil && ctx.Err() == nil {
			log.Fatal(err)
		}
	}()

	go func() {
		<-ctx.Done()
		_ = server.Shutdown(context.Background())
		_ = adminServer.Shutdown(context.Background())
	}()

	log.Printf("gateway listening on %s admin %s", cfg.HTTPAddr, cfg.AdminAddr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
