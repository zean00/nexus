# Operator Guide

## Scope

This guide covers the current operational HTTP surface for the gateway:

- probe endpoints
- runtime and metrics inspection
- ACP inspection and compatibility views
- Telegram trust and failure-management views

The handlers are wired in [internal/app/app.go](/home/sahal/workspace/nexus/internal/app/app.go).

## Probes And Runtime

Gateway and admin surfaces both expose:

- `GET /healthz`
- `GET /readyz`
- `GET /metrics`

Health and readiness include:

- ACP catalog state
- default ACP agent readiness
- worker/reconciler runtime state
- recent probe transitions

`GET /admin/runtime` returns the same runtime perspective in JSON, including:

- current `health`
- current `readiness`
- persisted recovery counters
- compact ACP summary

Important runtime fields:

- `data.acp.default_agent_ready`
- `data.acp.default_agent_reason_count`
- `data.acp.default_agent_warning_count`
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
- approved/denied decision counts
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
5. inspect `/admin/telegram/trust/summary` and `/admin/telegram/failures` for Telegram trust/operator issues
6. use `/metrics` for scrape-based confirmation and alert correlation
