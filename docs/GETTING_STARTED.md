# Getting Started

This guide gets Nexus running locally with Postgres, the gateway HTTP server, the embedded worker loop, and the webchat UI.

## At a Glance

For most local work, the shortest path is:

1. create a Postgres database
2. run migrations
3. start `cmd/gateway`
4. open `/webchat`

You only need the CLI wrapper, OpenCode stdio, or a separate worker process if you are testing those specific paths.

## Prerequisites

- Go 1.22+ with module support
- PostgreSQL 15+ (or any recent Postgres compatible with `pgx`)
- Node.js 20+ if you want to rebuild the web UIs
- `opencode` installed locally only if you want the stdio ACP path

Optional but useful:

- `curl` for health checks
- `psql` / `createdb` for local database setup

## Quick Local Start

1. Create a local database:

```bash
createdb nexus
```

2. Run migrations:

```bash
DATABASE_URL=postgres://postgres:postgres@localhost:5432/nexus?sslmode=disable \
go run ./cmd/migrator
```

3. Start the gateway:

```bash
DATABASE_URL=postgres://postgres:postgres@localhost:5432/nexus?sslmode=disable \
go run ./cmd/gateway
```

4. Open webchat:

```text
http://localhost:8080/webchat
```

By default the gateway process also runs the worker loop, so you do not need a separate worker process for local development.

## What Starts When You Run `cmd/gateway`

```mermaid
flowchart LR
    A[cmd/gateway] --> B[Load config]
    B --> C[Connect Postgres]
    C --> D[Build App]
    D --> E[Gateway HTTP server :8080]
    D --> F[Admin HTTP server :8081]
    D --> G[Embedded worker loop]
```

## Local Webchat Testing

The simplest local path is webchat plus the dev-only session bootstrap.

Enable the dev session endpoint:

```bash
DATABASE_URL=postgres://postgres:postgres@localhost:5432/nexus?sslmode=disable \
WEBCHAT_DEV_AUTH=true \
NEXUS_ENV=development \
go run ./cmd/gateway
```

The endpoint `POST /webchat/dev/session` only works when all of these are true:

- `WEBCHAT_DEV_AUTH=true`
- `NEXUS_ENV=development`
- the request host is loopback (`localhost`, `127.0.0.1`, or another loopback address)

This is intentionally not available in production or on non-local hosts.

In practice, this gives you a fast local loop without weakening non-local environments.

## Webchat UI

The embedded webchat is the easiest way to verify session handling, awaits, streaming updates, artifacts, and identity-link flows.

![Webchat shell](./images/webchat-shell.png)

## CLI Wrapper for Webchat

Nexus also includes a dev-only CLI wrapper around the existing webchat API.

Start the gateway with dev auth enabled:

```bash
DATABASE_URL=postgres://postgres:postgres@localhost:5432/nexus?sslmode=disable \
WEBCHAT_DEV_AUTH=true \
NEXUS_ENV=development \
go run ./cmd/gateway
```

Then use the CLI:

```bash
go run ./cmd/nexuscli dev-login --base-url http://localhost:8080 --email dev@example.com
go run ./cmd/nexuscli send "hello from Nexus CLI"
go run ./cmd/nexuscli history --limit 20
```

Full CLI usage is documented in [CLI_GUIDE.md](./CLI_GUIDE.md).

The CLI is intentionally small. It is a convenience wrapper for the existing webchat API, not a second channel implementation.

## Running a Separate Worker

If you want the HTTP server and worker as separate processes:

1. Start the gateway without relying on the embedded worker as your only worker process.
2. Start a second process with the same configuration:

```bash
DATABASE_URL=postgres://postgres:postgres@localhost:5432/nexus?sslmode=disable \
go run ./cmd/worker
```

This is closer to how you would split responsibilities in production.

## Running Against OpenCode Stdio

To use the stdio ACP bridge with local `opencode`:

```bash
DATABASE_URL=postgres://postgres:postgres@localhost:5432/nexus?sslmode=disable \
ACP_IMPLEMENTATION=stdio \
ACP_COMMAND=opencode \
ACP_WORKDIR="$PWD" \
DEFAULT_ACP_AGENT_NAME=build \
go run ./cmd/gateway
```

To validate the real stdio integration:

```bash
NEXUS_INTEGRATION_OPENCODE=1 \
go test ./internal/adapters/acp -run 'TestStdioClientOpenCode(FileRead|FileWrite|Terminal)Integration' -v
```

This is the best local path when you want to test real agent execution without standing up an HTTP ACP service.

## Health and Runtime Checks

Gateway endpoints:

- `GET /healthz`
- `GET /readyz`
- `GET /metrics`
- `GET /webchat`

Admin endpoints:

- `GET http://localhost:8081/healthz`
- `GET http://localhost:8081/readyz`

Example:

```bash
curl -s http://localhost:8080/healthz | jq
curl -s http://localhost:8081/readyz | jq
```

## Rebuilding the Frontend Bundles

The repository includes checked-in built assets for `ui/webchat` and `ui/trustadmin`.

Rebuild `webchat`:

```bash
cd ui/webchat
npm install
npm run build
```

Rebuild `trustadmin`:

```bash
cd ui/trustadmin
npm install
npm run build
```

## Running Tests

Go tests:

```bash
go test ./...
go vet ./...
```

Frontend build verification:

```bash
cd ui/webchat && npm run build
cd ../trustadmin && npm run build
```

## Common Local Issues

### Postgres authentication fails

Check `DATABASE_URL`. Nexus does not manage Postgres credentials for you.

### Webchat dev session returns `404`

Check:

- `WEBCHAT_DEV_AUTH=true`
- `NEXUS_ENV=development`
- you are calling the gateway through a loopback host

### SSE connections close unexpectedly

The gateway server intentionally leaves `WriteTimeout` unset so long-lived `/webchat/events` streams can stay open.

### The worker does not pick up queued work

Check:

- the gateway or worker process is running
- `GET /readyz`
- ACP connectivity and manifest compatibility
- the admin runtime and outbox endpoints in [OPERATOR_GUIDE.md](./OPERATOR_GUIDE.md)

## Where To Go Next

- To understand how the queue, worker, outbox, and ACP bridge fit together, read [Architecture](./ARCHITECTURE.md).
- To tune runtime behavior, read [Configuration](./CONFIGURATION.md).
- To compare channel behavior, read [Channel Behavior and Compatibility Matrix](./CHANNEL_MATRIX.md).
