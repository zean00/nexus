# Supported ACP Protocols and Bridges

Nexus talks to downstream agent runtimes through the `ports.ACPBridge` interface.

The project currently ships multiple bridge implementations behind one factory.

## At a Glance

Nexus does not bind itself to one ACP transport. Instead, it normalizes several runtime styles behind one internal bridge so the worker and reconciler can stay stable.

In practice:

- `strict` is the highest-fidelity behavioral target
- `stdio` is the best local development path
- the OpenCode bridge is a pragmatic compatibility layer, not a claim of native strict semantics

## ACP Bridge Contract

The internal ACP abstraction is:

```go
type ACPBridge interface {
    DiscoverAgents(ctx context.Context) ([]domain.AgentManifest, error)
    EnsureSession(ctx context.Context, session domain.Session) (string, error)
    StartRun(ctx context.Context, req domain.StartRunRequest) (domain.Run, domain.RunEventStream, error)
    ResumeRun(ctx context.Context, await domain.Await, payload []byte) (domain.RunEventStream, error)
    GetRun(ctx context.Context, acpRunID string) (domain.RunStatusSnapshot, error)
    FindRunByIdempotencyKey(ctx context.Context, session domain.Session, idempotencyKey string) (domain.RunStatusSnapshot, bool, error)
    FindLatestRunForSession(ctx context.Context, session domain.Session) (domain.RunStatusSnapshot, bool, error)
    CancelRun(ctx context.Context, run domain.Run) error
}
```

That interface is what the worker, reconciler, and catalog depend on. Each runtime-specific bridge is responsible for mapping its protocol into this contract.

## Factory Selection

`internal/adapters/acp/factory.go` selects the bridge using `ACP_IMPLEMENTATION`.

| `ACP_IMPLEMENTATION` | Bridge | Transport |
| --- | --- | --- |
| `stdio` | `StdioClient` | JSON-RPC ACP over a local subprocess |
| `parmesan` | `ParmesanClient` | Parmesan HTTP API mapped into ACPBridge |
| `strict`, `acp`, `native` | `StrictClient` | strict/native ACP-style HTTP API |
| anything else | `Client` | OpenCode HTTP bridge |

## Supported Bridge Types

### 1. OpenCode HTTP bridge

Implementation: `internal/adapters/acp/client.go`

Use when:

- you are talking to an OpenCode-compatible HTTP endpoint
- you want the default non-strict bridge path

Manifest behavior:

- protocol: `opencode`
- streaming: supported
- artifacts: supported
- await/resume: not treated as native strict ACP

Compatibility mode in Nexus:

- `opencode_bridge`

Important consequence:

- structured await/resume is not considered native
- if a route relies on native structured awaits, Nexus may stop the interaction instead of leaving a broken pending approval

This bridge is useful, but it should be read as “compatible enough for many tasks,” not “equivalent to strict ACP.”

### 2. Strict / native ACP HTTP bridge

Implementation: `internal/adapters/acp/strict_client.go`

Use when:

- the backend exposes a native ACP-like HTTP API with manifests, sessions, runs, snapshots, and resume endpoints

Manifest behavior is taken directly from the backend.

Compatibility mode in Nexus:

- `strict_acp`

This is the highest-fidelity path for:

- await/resume
- structured awaits
- agent compatibility checks

If you want behavior closest to the canonical Nexus runtime model, this is the bridge to prefer.

### 3. Stdio ACP bridge

Implementation: `internal/adapters/acp/stdio_client.go`

Use when:

- the agent runtime is local
- ACP is available over stdio JSON-RPC
- you want to talk to `opencode acp` or a similar local runtime

Key behavior:

- launches the subprocess configured by `ACP_COMMAND`, `ACP_ARGS`, `ACP_ENV_*`
- keeps local replay state for recovery helpers
- supports streaming run events
- surfaces callback counts for file and terminal tool usage

Current practical notes:

- input artifact mapping is implemented for ACP content blocks such as `resource_link`, `image`, `audio`, and generic `resource`
- canonical location message parts are rendered into prompt text with coordinates and a maps link
- output artifacts depend on what the underlying ACP server actually emits for a given run
- replay and latest-run lookup are backed by local stdio client state

This is the most convenient path for local agent testing because it removes the separate ACP service from the setup.

### 4. Parmesan bridge

Implementation: `internal/adapters/acp/parmesan_client.go`

Use when:

- you are integrating with the Parmesan execution/session/event model
- you still want Nexus to consume it through the standard ACP bridge interface

This bridge maps Parmesan sessions, executions, events, and approvals into:

- agent discovery
- run snapshots
- await prompts
- approval resume flows

## Compatibility Validation

Nexus does not blindly trust any discovered agent manifest.

`internal/services/acpvalidate.go` validates each candidate using:

- health
- session reload support
- await/resume support
- streaming support
- artifact support
- protocol-specific warnings

Validation modes:

| Mode | Meaning |
| --- | --- |
| `strict_acp` | The backend is treated as native ACP |
| `opencode_bridge` | The backend is usable, but structured await semantics are bridged and may be downgraded |

The point of validation is to protect the queue and approval UX from backends that look compatible at discovery time but are missing behavior Nexus actually depends on.

## Compatibility Rules

### Required for strict/native compatibility

- agent must be healthy
- session reload must be supported
- await/resume must be supported
- streaming must be supported
- artifacts must be supported

### OpenCode bridge behavior

If the manifest protocol is `opencode`:

- Nexus switches validation mode to `opencode_bridge`
- missing await/resume produces warnings instead of immediate incompatibility
- missing structured await support also produces warnings
- missing streaming or artifact support is still incompatible

## Run Event Model

All bridges eventually emit `domain.RunEvent` values.

Important fields:

- `RunID`
- `MessageKey`
- `Status`
- `Text`
- `IsPartial`
- `AwaitSchema`
- `AwaitPrompt`
- `Artifacts`

This is what the worker persists and what renderers consume.

## ACP Session Model

Nexus uses `EnsureSession()` before a run starts.

The session key is not provider-native; it is the Nexus `domain.Session` resolved from the channel surface. The bridge is responsible for mapping that to an ACP runtime session ID and returning it so Nexus can persist it as `Session.ACPSessionID`.

## Operational Notes

### Agent catalog

The agent catalog caches manifests using `ACP_MANIFEST_CACHE_TTL_SECONDS`.

It supports:

- list agents
- validate a specific agent
- return only compatible agents

### Startup validation

If `VALIDATE_ACP_ON_STARTUP=true`, Nexus validates the default ACP agent during startup and fails fast when it is incompatible.

### Stdio runtime metrics

When the stdio bridge is active, runtime surfaces expose:

- whether the subprocess is running
- whether initialization succeeded
- callback counts for permissions, file reads/writes, and terminal operations

## Capability Summary

| Bridge | Discovery | Streaming | Await/Resume | Artifacts | Session reload |
| --- | --- | --- | --- | --- | --- |
| OpenCode HTTP | Yes | Yes | Bridged / warning-based | Yes | Usually no |
| Strict/native ACP | Yes | Yes | Yes | Yes | Yes, if backend advertises it |
| Stdio ACP | Yes | Yes | Depends on runtime capabilities | Yes, subject to runtime output behavior | Depends on runtime capabilities |
| Parmesan | Yes | Yes | Yes | Yes | Yes |

## Choosing a Bridge

| Situation | Best Choice |
| --- | --- |
| Local development with a real agent binary | `stdio` |
| Production backend with strong ACP semantics | `strict` |
| Existing OpenCode-compatible HTTP service | OpenCode HTTP bridge |
| Parmesan-native runtime | `parmesan` |

## Strict ACP Email Context

For `email` sessions, Nexus passes email-specific metadata as trusted structured context instead of merging it only into user text. The strict/native ACP request includes a `structured_data` part with `data.kind: "email_context"` and fields such as `subject`, `from`, `from_name`, `message_id`, `thread_id`, `in_reply_to`, and `references`. Hydrated email attachments are still sent in `artifacts`, and Nexus also adds `artifact_ref` parts so compatible runtimes can process those attachments during the run.

## Related Docs

- [Architecture](./ARCHITECTURE.md)
- [Channel Behavior and Compatibility Matrix](./CHANNEL_MATRIX.md)
- [Operator Guide](./OPERATOR_GUIDE.md)
