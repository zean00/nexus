# Operator Guide

## Scope

This guide covers the current operational HTTP surface for the gateway:

- probe endpoints
- runtime and metrics inspection
- ACP inspection and compatibility views
- Telegram trust and failure-management views

The handlers are wired in `internal/app/app.go`.

## Production Hardening

Set `NEXUS_ENV=production` for production deployments. In production, startup requires:

- `ADMIN_BEARER_TOKEN`
- `SLACK_SIGNING_SECRET` not equal to `dev-secret`
- `WHATSAPP_VERIFY_TOKEN` not equal to `dev-whatsapp-verify`
- `EMAIL_WEBHOOK_SECRET` not equal to `dev-email-secret`
- `TELEGRAM_WEBHOOK_SECRET` not equal to `dev-telegram-secret`

Admin data and mutation endpoints require `Authorization: Bearer <ADMIN_BEARER_TOKEN>`. `GET /healthz` and `GET /readyz` remain open for probes. The Trust Admin page and static assets remain open, then prompt for the bearer token in the browser and send it with API requests.

HTTP server timeout defaults are:

- `HTTP_READ_HEADER_TIMEOUT_SECONDS=5`
- `HTTP_READ_TIMEOUT_SECONDS=30`
- `HTTP_WRITE_TIMEOUT_SECONDS=120` for the admin server; the gateway server leaves `WriteTimeout` unset for long-lived webchat event streams
- `HTTP_IDLE_TIMEOUT_SECONDS=120`

`WEBCHAT_DEV_AUTH=true` enables `POST /webchat/dev/session` for local CLI testing without the email OTP flow. This endpoint returns `404` unless `NEXUS_ENV=development` and the request host is loopback, such as `localhost` or `127.0.0.1`.

## Probes And Runtime

Gateway and admin surfaces both expose:

- `GET /healthz`
- `GET /readyz`
- `GET /metrics`

Health and readiness include:

- ACP catalog state
- default ACP agent readiness
- worker and reconciler runtime state
- recent probe transitions

`GET /admin/runtime` returns the same runtime perspective in JSON, including:

- current `health`
- current `readiness`
- persisted recovery counters
- compact ACP summary
- stdio ACP subprocess status when the active ACP bridge exposes it

Important runtime fields:

- `data.acp.default_agent_ready`
- `data.acp.default_agent_reason_count`
- `data.acp.default_agent_warning_count`
- `data.acp_runtime.running`
- `data.acp_runtime.initialized`
- `data.acp_runtime.callback_counts.*`
- `data.persisted.*`

## ACP Endpoints

ACP inspection endpoints:

- `GET /admin/acp/agents`
- `GET /admin/acp/compatible`
- `GET /admin/acp/validate`
- `GET /admin/acp/summary`
- `GET /admin/acp/bridge-blocks`

Useful query params:

- `refresh=true` on ACP list and validate endpoints
- `agent_name=...` on `/admin/acp/validate`
- normal paging filters on `/admin/acp/bridge-blocks`

Key ACP concepts surfaced operationally:

- catalog health and cache status
- bridge-compatible vs strict-compatible agents
- default-agent validation
- bridge-block totals and recent bridge-block events
- stdio ACP subprocess runtime state when available

### Compatibility Modes

The gateway currently distinguishes two ACP validation modes:

- `strict_acp`
- `opencode_bridge`

`strict_acp` means an agent manifest is considered fully compatible only if it is:

- healthy
- not marked as `protocol: "opencode"`
- `supports_await_resume = true`
- `supports_streaming = true`
- `supports_artifacts = true`

`opencode_bridge` means the backend is usable through the OpenCode bridge, but not treated as native full ACP compatibility for structured await and resume semantics.

Current practical status in this repo:

- OpenCode is validated and supported as `opencode_bridge`
- no concrete external agent is currently documented here as fully verified `strict_acp`

### Stdio ACP Runtime Notes

When `ACP_IMPLEMENTATION=stdio`, the probe, runtime, and metrics surfaces also expose subprocess state:

- process running and initialized state
- last subprocess error
- startup time
- callback counters for:
  - permission requests
  - filesystem reads and writes
  - terminal create, output, wait, kill, and release

Observed current behavior with real `opencode acp` in this environment:

- file-read tasks succeed
- file-write tasks succeed
- terminal tasks succeed
- the ACP callback counters can still remain zero

Interpretation:

- zero callback counters do not necessarily mean the stdio ACP path is broken
- they can mean the server completed the task without issuing client callback requests to Nexus
- non-zero counters are still useful when debugging future ACP server behavior changes

## Telegram Trust Endpoints

Primary Telegram trust endpoints:

- `GET /admin/telegram/users`
- `GET /admin/telegram/users/detail`
- `GET /admin/telegram/users/summary`
- `POST /admin/telegram/users/upsert`
- `POST /admin/telegram/users/delete`
- `GET /admin/telegram/requests`
- `POST /admin/telegram/requests/resolve`
- `GET /admin/telegram/denials`
- `GET /admin/telegram/failures`
- `GET /admin/telegram/trust/summary`
- `GET /admin/telegram/trust/decisions`

`/admin/telegram/failures` supports:

- `failure_type=all`
- `failure_type=not_found`
- `failure_type=not_pending`
- `failure_type=internal`

`/admin/telegram/trust/summary` includes:

- pending request counts
- approved and denied decision counts
- generic failure counts
- specific failure counts:
  - `failure_not_found`
  - `failure_not_pending`
  - `failure_internal`
- independent `has_more` and `next_cursors` for:
  - `recent_failures`
  - `failure_not_found`
  - `failure_not_pending`
  - `failure_internal`

## Metrics

Important ACP metrics:

- `nexus_acp_catalog_cache_valid`
- `nexus_acp_default_agent_compatible`
- `nexus_acp_default_agent_ready`
- `nexus_acp_default_agent_reason_count`
- `nexus_acp_default_agent_warning_count`
- `nexus_acp_compatible_agents`
- `nexus_acp_incompatible_agents`
- `nexus_acp_bridge_compatible_agents`
- `nexus_acp_degraded_agents`
- `nexus_acp_warning_count`
- `nexus_acp_bridge_block_count`
- `nexus_acp_runtime_running`
- `nexus_acp_runtime_initialized`
- `nexus_acp_runtime_error`
- `nexus_acp_runtime_permission_requests`
- `nexus_acp_runtime_fs_read_text_file`
- `nexus_acp_runtime_fs_write_text_file`
- `nexus_acp_runtime_terminal_create`
- `nexus_acp_runtime_terminal_output`
- `nexus_acp_runtime_terminal_wait`
- `nexus_acp_runtime_terminal_kill`
- `nexus_acp_runtime_terminal_release`

Important persisted operator-action metrics:

- `nexus_persisted_operator_run_cancels_total`
- `nexus_persisted_operator_surface_switches_total`
- `nexus_persisted_operator_surface_closures_total`
- `nexus_persisted_operator_telegram_approvals_total`
- `nexus_persisted_operator_telegram_denials_total`
- `nexus_persisted_operator_telegram_resolve_not_found_total`
- `nexus_persisted_operator_telegram_resolve_not_pending_total`
- `nexus_persisted_operator_telegram_resolve_internal_total`

Important persisted recovery metrics:

- `nexus_persisted_outbox_requeues_total`
- `nexus_persisted_queue_repairs_recovered_total`
- `nexus_persisted_queue_repairs_requeued_total`
- `nexus_persisted_run_refreshes_total`
- `nexus_persisted_await_expiries_total`
- `nexus_persisted_delivery_retries_total`
- `nexus_persisted_bridge_await_blocks_total`

## Recommended Triage Flow

When traffic looks unhealthy:

1. check `/readyz`
2. inspect `/admin/runtime`
3. inspect `/admin/acp/summary`
4. inspect `/admin/acp/bridge-blocks` if OpenCode bridge degradation is suspected
5. inspect `/admin/telegram/trust/summary` and `/admin/telegram/failures` for Telegram trust and operator issues
6. use `/metrics` for scrape-based confirmation and alert correlation
