# Configuration

Nexus is configured entirely through environment variables.

This document describes every configuration group, the defaults loaded in code, and the important behavior behind each setting.

## How To Read This Page

Use this document in two ways:

- as a reference when you already know the setting name
- as a deployment checklist when you are preparing a non-local environment

The source of truth for parsing and defaults is `internal/config/config.go`. This document is meant to explain the operational meaning behind those values.

## Configuration Loading Model

Configuration is loaded from `internal/config/config.go`.

Important rules:

- defaults are applied when an environment variable is missing
- `NEXUS_ENV=production` enables stricter validation
- `ADMIN_BEARER_TOKEN` is trimmed before use
- `WEBCHAT_INTERACTION_VISIBILITY` is validated against a fixed set of modes

## Core Runtime

| Variable | Default | Purpose | Notes |
| --- | --- | --- | --- |
| `SERVICE_NAME` | `nexus-gateway` | Service label for logs/telemetry | Mostly informational |
| `NEXUS_ENV` | `development` | Environment mode | `production` enables strict validation |
| `DATABASE_URL` | `postgres://postgres:postgres@localhost:5432/nexus?sslmode=disable` | Postgres connection string | Required in most real deployments |
| `DEFAULT_TENANT_ID` | `tenant_default` | Default tenant ID | The codebase currently assumes a single default tenant |
| `DEFAULT_AGENT_PROFILE_ID` | `agent_profile_default` | Default gateway agent profile ID | Used by routing and session bootstrap |

## HTTP Servers

| Variable | Default | Purpose | Notes |
| --- | --- | --- | --- |
| `HTTP_ADDR` | `:8080` | Public gateway bind address | Webhooks, webchat, probes |
| `ADMIN_ADDR` | `:8081` | Admin bind address | Admin APIs, metrics, trust admin |
| `ADMIN_BEARER_TOKEN` | empty | Bearer token for admin access | Required in production |
| `HTTP_READ_HEADER_TIMEOUT_SECONDS` | `5` | Header read timeout | Applied to gateway and admin servers |
| `HTTP_READ_TIMEOUT_SECONDS` | `30` | Request read timeout | Applied to gateway and admin servers |
| `HTTP_WRITE_TIMEOUT_SECONDS` | `120` | Response write timeout | Applied to the admin server only |
| `HTTP_IDLE_TIMEOUT_SECONDS` | `120` | Idle keepalive timeout | Applied to gateway and admin servers |

Why the gateway does not use `WriteTimeout`:

- `/webchat/events` is a long-lived SSE stream
- Go `WriteTimeout` is an absolute deadline, not an idle timeout
- a finite gateway write timeout would cut off live streams

This is easy to miss when hardening HTTP servers. The admin server can use a normal write timeout; the public gateway cannot, because webchat streaming would break.

## ACP Bridge

| Variable | Default | Purpose | Notes |
| --- | --- | --- | --- |
| `ACP_IMPLEMENTATION` | `strict` | ACP bridge implementation | See [ACP_PROTOCOL.md](./ACP_PROTOCOL.md) |
| `ACP_BASE_URL` | `http://localhost:8090` | Base URL for HTTP-based ACP bridges | Used by `strict`, `opencode`, `parmesan` |
| `ACP_TOKEN` | empty | Bearer/API token for ACP backend | Sent by HTTP bridges |
| `ACP_COMMAND` | `opencode` | Subprocess command for `stdio` bridge | Example: `opencode` |
| `ACP_ARGS` | empty | CSV list of stdio bridge args | Example: `acp,--pure,--cwd,/repo` |
| `ACP_ENV_*` | none | Prefixed environment variables passed to stdio subprocess | Every `ACP_ENV_FOO=bar` becomes `FOO=bar` in the child |
| `ACP_WORKDIR` | current working directory | Working directory for stdio ACP subprocess | Usually the repo root |
| `DEFAULT_ACP_AGENT_NAME` | `default-agent` | Default target agent name | Routing fallback |
| `VALIDATE_ACP_ON_STARTUP` | `false` | Fail startup if default ACP agent is incompatible | Good for production |
| `ACP_MANIFEST_CACHE_TTL_SECONDS` | `60` | Catalog cache TTL | Used by the agent catalog |
| `ACP_STARTUP_TIMEOUT_SECONDS` | `15` | stdio bridge startup timeout | Only relevant to `stdio` |
| `ACP_RPC_TIMEOUT_SECONDS` | `120` | ACP RPC timeout | Used by ACP clients and stdio RPC calls |

Choose `ACP_IMPLEMENTATION` based on the runtime you are actually talking to, not by preference alone. The bridge affects await semantics, compatibility validation, artifact behavior, and local test ergonomics.

## Slack

| Variable | Default | Purpose | Notes |
| --- | --- | --- | --- |
| `SLACK_SIGNING_SECRET` | `dev-secret` | Inbound webhook signature validation | Must be changed in production |
| `SLACK_BOT_TOKEN` | empty | Bot token for outbound sends and downloads | Required for real Slack delivery |

Behavior notes:

- inbound verification uses `X-Slack-Request-Timestamp` and `X-Slack-Signature`
- Slack file references are treated as inbound artifacts
- outbound artifacts are uploaded separately from text/status deliveries

## Telegram

| Variable | Default | Purpose | Notes |
| --- | --- | --- | --- |
| `TELEGRAM_BOT_TOKEN` | empty | Telegram Bot API token | Required for outbound Telegram sends |
| `TELEGRAM_WEBHOOK_SECRET` | `dev-telegram-secret` | Inbound secret token validation | Must be changed in production |
| `TELEGRAM_ALLOWED_USER_IDS` | empty | Optional allowlist of Telegram user IDs | Useful for controlled deployments |

Behavior notes:

- Telegram is the only channel with built-in session commands in the current codebase
- Telegram outbound artifacts are file-backed uploads routed by MIME type to native photo, audio, video, or document sends
- inbound Telegram file/media messages are hydrated through the Bot API `getFile` flow
- inbound Telegram `location` and `venue` messages are normalized into canonical location parts with a maps-link text fallback
- raw outbound Telegram payloads containing `latitude` and `longitude` are sent through `sendLocation`

## WhatsApp

| Variable | Default | Purpose | Notes |
| --- | --- | --- | --- |
| `WHATSAPP_VERIFY_TOKEN` | `dev-whatsapp-verify` | Webhook verify token | Must be changed in production |
| `WHATSAPP_ACCESS_TOKEN` | empty | WhatsApp Cloud API access token | Required for outbound sends and media hydration |
| `WHATSAPP_APP_SECRET` | empty | Signature verification secret | Enables request signature checking |
| `WHATSAPP_PHONE_NUMBER_ID` | empty | Expected phone number ID | Used for outbound path and inbound filtering |
| `WHATSAPP_API_BASE_URL` | `https://graph.facebook.com/v20.0` | WhatsApp Graph API base URL | Override only for testing |
| `WHATSAPP_MEDIA_MAX_BYTES` | `10485760` | Maximum inbound media size | 10 MiB default |
| `WHATSAPP_ENFORCE_24H_WINDOW` | `true` | Enforce official WhatsApp customer service window before free-form sends | Applies only to `whatsapp`, not `whatsapp_web` |
| `WHATSAPP_CUSTOMER_SERVICE_WINDOW_HOURS` | `24` | Free-form reply window after each inbound WhatsApp message | Set only if Meta policy changes or for tests |
| `WHATSAPP_CLOSED_WINDOW_TEMPLATE_JSON` | empty | Default approved template object for closed-window fallback | Optional; explicit outbound template payloads take precedence |

Behavior notes:

- inbound media hydration supports image, document, and audio
- inbound location messages are normalized into canonical location parts with a maps-link text fallback
- outbound artifact delivery requires a reachable `http(s)` artifact URL
- raw outbound payloads can use the WhatsApp Cloud `location` message shape for native location sends
- every inbound official WhatsApp message resets the customer service window
- free-form official WhatsApp sends outside the window are converted to an explicit/default template when available, otherwise blocked
- `STOP`, `UNSUBSCRIBE`, `CANCEL`, and `END` mark a contact opted out; `START`, `UNSTOP`, and `RESUME` restore opt-in state

## WhatsApp Web

| Variable | Default | Purpose | Notes |
| --- | --- | --- | --- |
| `WHATSAPP_WEB_ENABLED` | `false` | Enable the WAHA-backed `whatsapp_web` channel | Separate from official WhatsApp Cloud API |
| `WHATSAPP_WEB_BASE_URL` | `http://localhost:3000` | WAHA API base URL | Required when enabled |
| `WHATSAPP_WEB_API_KEY` | empty | WAHA API key | Recommended in production |
| `WHATSAPP_WEB_SESSION` | `default` | WAHA session name | Nexus manages this configured session |
| `WHATSAPP_WEB_ENGINE` | empty | Preferred WAHA engine | Optional |
| `WHATSAPP_WEB_WEBHOOK_SECRET` | empty | HMAC secret for WAHA webhooks | Should be set in production |
| `WHATSAPP_WEB_ENABLE_ANTI_BLOCK` | `true` | Enable seen / typing / delay send flow | Applies only to `whatsapp_web` |
| `WHATSAPP_WEB_ENABLE_SEEN` | `true` | Send `seen` before message sends | Applies only when anti-block is enabled |
| `WHATSAPP_WEB_ENABLE_TYPING` | `true` | Send typing presence before sends | Applies only when anti-block is enabled |
| `WHATSAPP_WEB_SET_OFFLINE_AFTER_SEND` | `true` | Reset WAHA session presence to `offline` after successful sends | Keeps the session from looking permanently active |
| `WHATSAPP_WEB_REQUIRE_RECENT_INBOUND` | `true` | Require a recent inbound message before outbound sends are allowed | Enforces reply-oriented behavior for `whatsapp_web` |
| `WHATSAPP_WEB_MIN_DELAY_MS` | `800` | Minimum delay before sends | Anti-block pacing floor |
| `WHATSAPP_WEB_MAX_DELAY_MS` | `2500` | Maximum delay before sends | Anti-block pacing ceiling |
| `WHATSAPP_WEB_HOURLY_MESSAGE_CAP` | `120` | Rolling hourly delivery cap per Nexus session | Uses sent delivery history |
| `WHATSAPP_WEB_RECENT_INBOUND_WINDOW_MINUTES` | `30` | How recent the last inbound message must be when recent-inbound gating is enabled | Per Nexus session |
| `WHATSAPP_WEB_BURST_WINDOW_MINUTES` | `2` | Short rolling window used for burst throttling | Per Nexus session |
| `WHATSAPP_WEB_BURST_MESSAGE_CAP` | `4` | Maximum sent deliveries allowed within the burst window | Set `0` to disable burst throttling |
| `NEXUS_PUBLIC_BASE_URL` | empty | Public gateway base URL | Required for WAHA webhook sync and QR/session lifecycle flows |

Behavior notes:

- `whatsapp_web` is a separate channel from `whatsapp`
- outbound artifacts are sent through WAHA directly and do not require public URLs
- inbound WAHA location payloads are normalized into canonical location parts with a maps-link text fallback
- Nexus exposes admin session lifecycle endpoints for the configured WAHA session
- phone-based identity linking is shared with the official WhatsApp channel

## Email

| Variable | Default | Purpose | Notes |
| --- | --- | --- | --- |
| `EMAIL_WEBHOOK_SECRET` | `dev-email-secret` | Inbound email webhook authentication | Must be changed in production |
| `EMAIL_SMTP_ADDR` | empty | SMTP server address | Required for outbound email |
| `EMAIL_SMTP_USERNAME` | empty | SMTP username | Optional depending on provider |
| `EMAIL_SMTP_PASSWORD` | empty | SMTP password | Optional depending on provider |
| `EMAIL_FROM_ADDRESS` | `nexus@example.com` | Sender address for outbound email | Used by email adapter |
| `EMAIL_WEBHOOK_MAX_SKEW_SECONDS` | `300` | Allowed webhook timestamp skew | Used with HMAC timestamp mode |
| `EMAIL_MAX_ATTACHMENT_BYTES` | `10485760` | Maximum decoded attachment size | 10 MiB default |
| `EMAIL_MAX_ATTACHMENTS` | `10` | Maximum attachments per inbound email | Rejects larger payloads |

Behavior notes:

- inbound email accepts text/html plus attachments
- await responses can be recovered from subject/body markers
- outbound artifacts are attached to the email message

## Webchat

| Variable | Default | Purpose | Notes |
| --- | --- | --- | --- |
| `WEBCHAT_COOKIE_NAME` | `nexus_webchat_session` | Session cookie name | Shared by auth/bootstrap/message endpoints |
| `WEBCHAT_DEV_AUTH` | `false` | Enables local dev login endpoint | Only works in local development |
| `WEBCHAT_INTERACTION_VISIBILITY` | `full` | Webchat interaction presentation mode | `full`, `simple`, `minimal`, `off` |
| `WEBCHAT_HISTORY_SCOPE` | `session` | Webchat timeline query scope | `session`, `user`, `linked_channels` |
| `WEBCHAT_SESSION_HOURS` | `24` | Webchat auth session TTL | Controls cookie-backed session lifetime |
| `WEBCHAT_OTP_MINUTES` | `10` | OTP challenge TTL | Email login code expiry |

### `WEBCHAT_INTERACTION_VISIBILITY`

Supported values:

| Mode | Behavior |
| --- | --- |
| `full` | Show the normal timeline, including partial streamed assistant text |
| `simple` | Hide internal activity detail and show human-style transient signals such as `Thinking...`, `Typing...`, `Working...` |
| `minimal` | Hide partial assistant text and collapse activity to `Typing...` |
| `off` | Hide transient activity signals and partial assistant text until final output appears |

This setting only changes presentation. It does not change the underlying worker, run persistence, or audit behavior.

### `WEBCHAT_HISTORY_SCOPE`

Supported values:

| Mode | Behavior |
| --- | --- |
| `session` | Show only the active webchat session history |
| `user` | Show the active webchat session plus sessions owned by the authenticated webchat user or one of their linked identities |
| `linked_channels` | Show the active webchat session plus sessions for linked external channel identities |

In `user` and `linked_channels` modes, external-channel items are read-only inside webchat. Sending from webchat still writes to the active webchat session only, while inbound messages and agent responses from linked external sessions can refresh the webchat SSE timeline.

### `WEBCHAT_DEV_AUTH`

The dev login endpoint `POST /webchat/dev/session` is only enabled when:

- `WEBCHAT_DEV_AUTH=true`
- `NEXUS_ENV=development`
- the request host is loopback

This endpoint intentionally returns `404` outside that local-only scope.

## Identity and Trust

| Variable | Default | Purpose | Notes |
| --- | --- | --- | --- |
| `IDENTITY_LINK_MINUTES` | `10` | Link challenge validity window | Used for cross-channel identity link flow |
| `STEP_UP_OTP_MINUTES` | `10` | Step-up OTP validity window | Used for sensitive operations |
| `STEP_UP_WINDOW_MINUTES` | `15` | Recent step-up trust window | Determines how long step-up remains fresh |
| `REQUIRE_LINKED_IDENTITY` | `false` | Require linked identity for approvals | Applied through fallback trust policy |
| `REQUIRE_RECENT_STEP_UP` | `false` | Require recent step-up for approvals | Applied through fallback trust policy |
| `ALLOWED_APPROVAL_CHANNELS` | empty | CSV list of allowed approval channels | Example: `slack,webchat` |

## Resilience and Delivery

| Variable | Default | Purpose | Notes |
| --- | --- | --- | --- |
| `RETRY_MAX_ATTEMPTS` | `3` | Retry attempts for resilient HTTP operations | Used by resilience policy |
| `RETRY_BASE_DELAY_MS` | `200` | Base retry delay | Exponential backoff starts here |
| `CIRCUIT_BREAKER_FAILURES` | `5` | Failure threshold before opening breaker | External integration protection |
| `CIRCUIT_BREAKER_COOLDOWN_SECONDS` | `30` | Breaker cooldown before retry | External integration protection |
| `DELIVERY_SENDING_TIMEOUT_SECONDS` | `120` | Delivery stale threshold for reconciler | Used to retry stuck sends |
| `DELIVERY_MAX_ATTEMPTS` | `5` | Max delivery retry attempts | Used by reconciler |

## Worker, Queue, and Recovery

| Variable | Default | Purpose | Notes |
| --- | --- | --- | --- |
| `WORKER_POLL_SECONDS` | `2` | Worker loop poll interval | Applies in `cmd/gateway` embedded worker and `cmd/worker` |
| `RECONCILER_INTERVAL_SECONDS` | `30` | Reconciler loop interval | Used by the runtime loop |
| `OUTBOX_CLAIM_TIMEOUT_SECONDS` | `120` | Stale claimed outbox threshold | Requeue after this timeout |
| `QUEUE_STARTING_TIMEOUT_SECONDS` | `120` | Stuck queue-start threshold | Used to repair queue items |
| `RUN_STALE_TIMEOUT_SECONDS` | `300` | Stale running run threshold | Used to refresh ACP run status |

## Object Storage and Retention

| Variable | Default | Purpose | Notes |
| --- | --- | --- | --- |
| `OBJECT_STORAGE_BASE_URL` | `file:///tmp/nexus-objects` | Artifact blob storage base | File-backed by default |
| `RETENTION_ENABLED` | `false` | Enable retention loop | Disabled by default |
| `RETENTION_INTERVAL_SECONDS` | `3600` | Retention run interval | One hour default |
| `RETENTION_BATCH_SIZE` | `500` | Batch size per retention run | Controls deletion pressure |
| `RETENTION_DEFAULT_PAYLOAD_DAYS` | `30` | Payload retention window | Message/outbox/await payloads |
| `RETENTION_DEFAULT_ARTIFACT_DAYS` | `30` | Artifact blob retention window | Object store cleanup cutoff |
| `RETENTION_DEFAULT_AUDIT_DAYS` | `30` | Audit retention window | Audit row cleanup cutoff |
| `RETENTION_RELATIONAL_GRACE_DAYS` | `30` | Relational delete grace period | Protects linked relational rows |

## Telemetry

| Variable | Default | Purpose | Notes |
| --- | --- | --- | --- |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | empty | OTLP trace export endpoint | If empty, tracing stays local/no-op depending on tracer setup |
| `OTEL_SAMPLE_RATIO` | `1.0` | Trace sample ratio | Full sampling by default |

## Production Validation

When `NEXUS_ENV=production`, startup rejects unsafe configuration.

Currently required:

- `ADMIN_BEARER_TOKEN` must be non-empty
- `SLACK_SIGNING_SECRET` must not equal the dev default
- `WHATSAPP_VERIFY_TOKEN` must not equal the dev default
- `EMAIL_WEBHOOK_SECRET` must not equal the dev default
- `TELEGRAM_WEBHOOK_SECRET` must not equal the dev default

This is a deliberate guardrail. The local defaults are convenient for development, but they are not safe deployment values.

## Example Configurations

### Minimal local webchat setup

```bash
export DATABASE_URL=postgres://postgres:postgres@localhost:5432/nexus?sslmode=disable
export WEBCHAT_DEV_AUTH=true
export NEXUS_ENV=development
```

### Strict/native ACP HTTP backend

```bash
export DATABASE_URL=postgres://postgres:postgres@localhost:5432/nexus?sslmode=disable
export ACP_IMPLEMENTATION=strict
export ACP_BASE_URL=http://localhost:8090
export ACP_TOKEN=secret
export DEFAULT_ACP_AGENT_NAME=strict-agent
```

### OpenCode stdio backend

```bash
export DATABASE_URL=postgres://postgres:postgres@localhost:5432/nexus?sslmode=disable
export ACP_IMPLEMENTATION=stdio
export ACP_COMMAND=opencode
export ACP_ARGS=acp,--pure,--cwd,/path/to/workdir
export ACP_WORKDIR=/path/to/workdir
export DEFAULT_ACP_AGENT_NAME=build
```

### Production baseline

```bash
export NEXUS_ENV=production
export DATABASE_URL=postgres://app:secret@db:5432/nexus?sslmode=require
export HTTP_ADDR=:8080
export ADMIN_ADDR=127.0.0.1:8081
export ADMIN_BEARER_TOKEN=replace-me
export SLACK_SIGNING_SECRET=replace-me
export WHATSAPP_VERIFY_TOKEN=replace-me
export EMAIL_WEBHOOK_SECRET=replace-me
export TELEGRAM_WEBHOOK_SECRET=replace-me
export OBJECT_STORAGE_BASE_URL=file:///var/lib/nexus/objects
```

## Related Docs

- [Getting Started](./GETTING_STARTED.md)
- [Supported ACP Protocols and Bridges](./ACP_PROTOCOL.md)
- [Operator Guide](./OPERATOR_GUIDE.md)
