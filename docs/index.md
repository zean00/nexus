# Nexus

Nexus is a Go gateway that turns messaging channels into ACP-backed agent interactions.

It accepts inbound events from Slack, Telegram, WhatsApp, email, and webchat, normalizes them into a canonical session and message model, persists them first, and then processes work through an outbox-driven worker that talks to ACP-compatible runtimes.

## Start Here

If you are new to the project, read these in order:

1. [Getting Started](./GETTING_STARTED.md)
2. [Architecture](./ARCHITECTURE.md)
3. [Configuration](./CONFIGURATION.md)

## Read By Goal

- Local setup and first run: [Getting Started](./GETTING_STARTED.md)
- Runtime model and data flow: [Architecture](./ARCHITECTURE.md)
- What the system supports today: [Features](./FEATURES.md)
- Deployment and environment variables: [Configuration](./CONFIGURATION.md)
- ACP bridge behavior: [Supported ACP Protocols and Bridges](./ACP_PROTOCOL.md)
- Channel behavior tradeoffs: [Channel Behavior and Compatibility Matrix](./CHANNEL_MATRIX.md)
- Adding a new channel: [Extending Nexus With a New Channel](./EXTENDING_CHANNELS.md)
- Operating the system: [Operator Guide](./OPERATOR_GUIDE.md)
- Local CLI usage: [CLI Guide](./CLI_GUIDE.md)
- License: `Apache License 2.0` in `LICENSE`

## What Nexus Does

- DB-first inbound processing with idempotent receipts
- session resolution and queue-based run serialization
- ACP routing and compatibility validation
- await and resume handling across channels
- artifact ingest, persistence, and outbound delivery
- webchat UI with SSE updates and configurable interaction visibility
- admin and runtime inspection endpoints
- reconciler and retention background jobs

## Main Entry Points

- `cmd/gateway/main.go`
- `cmd/worker/main.go`
- `cmd/migrator/main.go`
- `cmd/nexuscli/main.go`

## Source Layout

- `internal/app`: HTTP handlers, composition root wiring, webchat surface
- `internal/services`: inbound, worker, reconciler, router, renderers
- `internal/adapters`: ACP bridges, channel adapters, Postgres, object storage
- `internal/ports`: service and adapter interfaces
- `internal/domain`: canonical types
- `migrations`: schema migrations
- `ui/webchat`: embedded webchat frontend
- `ui/trustadmin`: embedded admin UI

## Quick Start

```bash
createdb nexus
DATABASE_URL=postgres://postgres:postgres@localhost:5432/nexus?sslmode=disable go run ./cmd/migrator
DATABASE_URL=postgres://postgres:postgres@localhost:5432/nexus?sslmode=disable go run ./cmd/gateway
```

Open `http://localhost:8080/webchat`.

For the fuller local setup, including dev webchat auth, the CLI wrapper, OpenCode stdio, and verification steps, continue with [Getting Started](./GETTING_STARTED.md).
