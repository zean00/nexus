package main

import (
	"context"
	"log"
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

	if err := application.WorkerLoop(ctx); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}
