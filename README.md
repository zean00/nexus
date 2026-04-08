# Nexus

Go ACP/OpenCode gateway with:

- DB-first webhook ingest
- outbox-driven worker processing
- Slack and Telegram channel adapters
- ACP/OpenCode agent routing and compatibility checks
- admin, runtime, and operator inspection endpoints

## Key Docs

- [Architecture](./acp_gateway_architecture_golang.md)
- [Operator Guide](./OPERATOR_GUIDE.md)
- [CLI Guide](./CLI_GUIDE.md)

## Entry Points

- [cmd/gateway/main.go](./cmd/gateway/main.go)
- [cmd/worker/main.go](./cmd/worker/main.go)
- [cmd/migrator/main.go](./cmd/migrator/main.go)
- [cmd/nexuscli/main.go](./cmd/nexuscli/main.go)

## Main Packages

- [internal/app](./internal/app)
- [internal/services](./internal/services)
- [internal/adapters](./internal/adapters)
- [internal/ports](./internal/ports)
- [internal/domain](./internal/domain)

## Basic Commands

```bash
go test ./...
go build ./...
go run ./cmd/migrator
go run ./cmd/gateway
go run ./cmd/worker
```

## Dev Webchat CLI

For local testing, enable dev webchat auth and start the gateway:

```bash
WEBCHAT_DEV_AUTH=true go run ./cmd/gateway
```

Then use the CLI against the existing webchat API:

```bash
go run ./cmd/nexuscli dev-login --base-url http://localhost:8080 --email dev@example.com
go run ./cmd/nexuscli send "hello from the CLI"
go run ./cmd/nexuscli history --limit 20
go run ./cmd/nexuscli respond --await-id await_123 --reply "approve"
```

`/webchat/dev/session` only works when `WEBCHAT_DEV_AUTH=true`, `NEXUS_ENV=development`, and the request host is loopback.

## Live OpenCode Stdio Validation

If `opencode` is available locally, the real stdio ACP integration suite can be run with:

```bash
NEXUS_INTEGRATION_OPENCODE=1 go test ./internal/adapters/acp -run 'TestStdioClientOpenCode(FileRead|FileWrite|Terminal)Integration' -v
```

This validates Nexus talking to `opencode acp` over native stdio JSON-RPC ACP for:

- file read
- file write
- terminal execution
