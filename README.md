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

## Entry Points

- [cmd/gateway/main.go](/home/sahal/workspace/nexus/cmd/gateway/main.go)
- [cmd/worker/main.go](/home/sahal/workspace/nexus/cmd/worker/main.go)
- [cmd/migrator/main.go](/home/sahal/workspace/nexus/cmd/migrator/main.go)

## Main Packages

- [internal/app](/home/sahal/workspace/nexus/internal/app)
- [internal/services](/home/sahal/workspace/nexus/internal/services)
- [internal/adapters](/home/sahal/workspace/nexus/internal/adapters)
- [internal/ports](/home/sahal/workspace/nexus/internal/ports)
- [internal/domain](/home/sahal/workspace/nexus/internal/domain)

## Basic Commands

```bash
go test ./...
go build ./...
go run ./cmd/migrator
go run ./cmd/gateway
go run ./cmd/worker
```

## Live OpenCode Stdio Validation

If `opencode` is available locally, the real stdio ACP integration suite can be run with:

```bash
NEXUS_INTEGRATION_OPENCODE=1 go test ./internal/adapters/acp -run 'TestStdioClientOpenCode(FileRead|FileWrite|Terminal)Integration' -v
```

This validates Nexus talking to `opencode acp` over native stdio JSON-RPC ACP for:

- file read
- file write
- terminal execution
