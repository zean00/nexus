package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"nexus/internal/config"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "migrator"))
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		slog.Error("startup.config_load_failed", "error", err.Error())
		os.Exit(1)
	}
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("startup.db_init_failed", "error", err.Error())
		os.Exit(1)
	}
	defer pool.Close()

	if _, err := pool.Exec(ctx, `
		create table if not exists schema_migrations (
			version text primary key,
			applied_at timestamptz not null default now()
		)
	`); err != nil {
		slog.Error("migrations.bootstrap_failed", "error", err.Error())
		os.Exit(1)
	}

	entries, err := os.ReadDir("migrations")
	if err != nil {
		slog.Error("migrations.readdir_failed", "error", err.Error())
		os.Exit(1)
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		files = append(files, entry.Name())
	}
	sort.Strings(files)
	for _, name := range files {
		var applied bool
		if err := pool.QueryRow(ctx, `select exists(select 1 from schema_migrations where version=$1)`, name).Scan(&applied); err != nil {
			slog.Error("migrations.check_failed", "error", err.Error(), "name", name)
			os.Exit(1)
		}
		if applied {
			slog.Info("migrations.already_applied", "name", name)
			continue
		}
		path := filepath.Join("migrations", name)
		migration, err := os.ReadFile(path)
		if err != nil {
			slog.Error("migrations.read_failed", "error", err.Error(), "name", name, "path", path)
			os.Exit(1)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			slog.Error("migrations.begin_failed", "error", err.Error(), "name", name)
			os.Exit(1)
		}
		if _, err := tx.Exec(ctx, string(migration)); err != nil {
			_ = tx.Rollback(ctx)
			slog.Error("migrations.apply_failed", "error", err.Error(), "name", name)
			os.Exit(1)
		}
		if _, err := tx.Exec(ctx, `insert into schema_migrations (version) values ($1)`, name); err != nil {
			_ = tx.Rollback(ctx)
			slog.Error("migrations.record_failed", "error", err.Error(), "name", name)
			os.Exit(1)
		}
		if err := tx.Commit(ctx); err != nil {
			slog.Error("migrations.commit_failed", "error", err.Error(), "name", name)
			os.Exit(1)
		}
		slog.Info("migrations.applied", "name", name, "message", fmt.Sprintf("migration applied: %s", name))
	}
}
