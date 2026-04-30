# Nexus

Nexus is a Go gateway that turns messaging channels into ACP-backed agent interactions.

It accepts inbound events from Slack, Telegram, WhatsApp, email, and webchat, normalizes them into a canonical session/message model, persists them first, and then processes work through an outbox-driven worker that talks to ACP-compatible runtimes.

If you are new to the project, start with:

1. [Getting Started](./docs/GETTING_STARTED.md) to run it locally
2. [Architecture](./docs/ARCHITECTURE.md) to understand the runtime model
3. [Configuration](./docs/CONFIGURATION.md) before changing deployment behavior

## Documentation

- [Getting Started](./docs/GETTING_STARTED.md)
- [Architecture](./docs/ARCHITECTURE.md)
- [Features](./docs/FEATURES.md)
- [Configuration](./docs/CONFIGURATION.md)
- [Extending Nexus With a New Channel](./docs/EXTENDING_CHANNELS.md)
- [Supported ACP Protocols and Bridges](./docs/ACP_PROTOCOL.md)
- [Channel Behavior and Compatibility Matrix](./docs/CHANNEL_MATRIX.md)
- [Operator Guide](./docs/OPERATOR_GUIDE.md)
- [CLI Guide](./docs/CLI_GUIDE.md)
- [License](./LICENSE)

## Read By Goal

- Local setup and first run: [Getting Started](./docs/GETTING_STARTED.md)
- Runtime model and data flow: [Architecture](./docs/ARCHITECTURE.md)
- What the system supports today: [Features](./docs/FEATURES.md)
- Deployment and environment variables: [Configuration](./docs/CONFIGURATION.md)
- ACP bridge behavior: [Supported ACP Protocols and Bridges](./docs/ACP_PROTOCOL.md)
- Channel behavior tradeoffs: [Channel Behavior and Compatibility Matrix](./docs/CHANNEL_MATRIX.md)
- Adding a new channel: [Extending Nexus With a New Channel](./docs/EXTENDING_CHANNELS.md)
- Operating the system: [Operator Guide](./docs/OPERATOR_GUIDE.md)
- Local CLI usage: [CLI Guide](./docs/CLI_GUIDE.md)

## What Nexus Does

- DB-first inbound processing with idempotent receipts
- session resolution and queue-based run serialization
- ACP routing and compatibility validation
- await/resume handling across channels
- artifact ingest, persistence, and outbound delivery
- inbound location normalization for Telegram, WhatsApp, and WhatsApp Web
- webchat UI with SSE updates and configurable interaction visibility
- admin and runtime inspection endpoints
- reconciler and retention background jobs

## Main Entry Points

- [cmd/gateway/main.go](./cmd/gateway/main.go)
- [cmd/worker/main.go](./cmd/worker/main.go)
- [cmd/migrator/main.go](./cmd/migrator/main.go)
- [cmd/nexuscli/main.go](./cmd/nexuscli/main.go)

## Source Layout

- [internal/app](./internal/app): HTTP handlers, composition root wiring, webchat surface
- [internal/services](./internal/services): inbound, worker, reconciler, router, renderers
- [internal/adapters](./internal/adapters): ACP bridges, channel adapters, Postgres, object storage
- [internal/ports](./internal/ports): service and adapter interfaces
- [internal/domain](./internal/domain): canonical types
- [migrations](./migrations): schema migrations
- [ui/webchat](./ui/webchat): embedded webchat frontend
- [ui/trustadmin](./ui/trustadmin): embedded admin UI

## Quick Start

```bash
createdb nexus
DATABASE_URL=postgres://postgres:postgres@localhost:5432/nexus?sslmode=disable go run ./cmd/migrator
DATABASE_URL=postgres://postgres:postgres@localhost:5432/nexus?sslmode=disable go run ./cmd/gateway
```

Open `http://localhost:8080/webchat`.

For a fuller local setup, including dev webchat auth, the CLI wrapper, OpenCode stdio, and verification steps, see [Getting Started](./docs/GETTING_STARTED.md).
