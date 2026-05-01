# Features

This document describes the implemented feature set in Nexus as it exists today.

The important framing is that Nexus is intentionally asymmetric. All channels share the same canonical core, but they do not all expose the same UX fidelity.

## Messaging Channels

Implemented channel adapters:

- Slack
- Telegram
- WhatsApp
- Email
- Webchat

Core channel capabilities:

- inbound verification where the provider supports it
- canonical message normalization
- responder binding for await/resume flows
- outbound message delivery
- external outbound push for reminders, broadcasts, and notifications
- outbound await prompts
- per-channel artifact handling where supported

## Session and Queue Management

Nexus keeps work serialized per session.

Implemented behavior:

- deterministic session resolution from channel surface data
- one active run per session
- queueing of additional messages while a run is active
- queue advancement after terminal run states
- virtual session management for selected surfaces

Current session UX details:

- Telegram has explicit session commands such as `/new`, `/switch`, `/sessions`, and `/close`
- webchat exposes chat lifecycle through HTTP/UI instead of slash-style commands

This queue-first model is one of the main reasons the system stays recoverable under retries, crashes, and long-running agent work.

## ACP Routing and Validation

Implemented behavior:

- agent discovery through the configured ACP bridge
- cached agent manifest catalog
- compatibility validation before run start
- distinction between native/strict ACP and OpenCode bridge behavior
- routed execution with a default ACP agent

Compatibility checks include:

- agent health
- session reload support
- await/resume support
- streaming support
- artifact support

That validation step matters because the gateway depends on more than just “can this backend answer a prompt.”

## Await / Resume Workflows

Nexus supports human-in-the-loop pauses.

Implemented behavior:

- store pending awaits with trust policy metadata
- render waits into channel-native UX
- accept channel-specific responses
- enqueue durable resume work
- continue the original session queue after terminal completion

Channel examples:

- Slack: interactive buttons
- Telegram: inline keyboard buttons
- WhatsApp: buttons for small choice sets, text fallback otherwise
- Email: reply by email with await marker retained in subject
- Webchat: direct API/UI response

The result is one logical await model with different channel-specific interaction styles.

## Artifact Handling

Implemented artifact features:

- inbound artifact hydration from supported channels
- storage metadata in Postgres
- blob persistence through object storage
- outbound artifact delivery per channel where possible
- authenticated webchat artifact access
- inline webchat rendering for image/audio/video

Current state by area:

- inbound artifacts: webchat, Slack, Telegram, WhatsApp, WhatsApp Web, email
- outbound artifact delivery: Slack, Telegram, WhatsApp, WhatsApp Web, email, webchat
- webchat rendering: inline image/audio/video, file rows for other artifact types
Artifact support is broad, but it is not uniform. The exact behavior depends on both the channel and the ACP bridge in use.

## Location Handling

Nexus supports inbound coordinate normalization for channels that expose native
location payloads.

Implemented location features:

- Telegram `location` and `venue` payloads become canonical location message parts
- official WhatsApp Cloud `location` payloads become canonical location message parts
- WhatsApp Web/WAHA `location` payloads become canonical location message parts
- canonical location parts use `application/vnd.nexus.location+json`
- when no user-entered text is present, Nexus adds a text fallback with coordinates and a maps link
- ACP prompt builders render location parts as readable text instead of raw JSON

Current limits:

- Slack, email, and webchat do not currently parse native coordinate payloads
- generic rendered assistant output does not automatically become a native outbound location send
- Telegram and official WhatsApp can still send native outbound locations through raw channel payloads

## Webchat

Webchat is the most complete first-party UX in the repo.

Implemented features:

- email OTP and magic-link auth
- dev-only local session bootstrap
- CSRF protection
- chat history and SSE updates
- artifact upload and rendering
- identity profile and phone management
- configurable session, user, or linked-channel history scope
- link-code and unlink flows
- step-up verification flow
- configurable interaction visibility modes:
  - `full`
  - `simple`
  - `minimal`
  - `off`

If you are evaluating Nexus end to end for the first time, webchat is the best starting surface.

## Admin and Runtime Surfaces

Admin/runtime capabilities include:

- health and readiness probes
- Prometheus-style metrics output
- session, run, await, delivery, and audit inspection endpoints
- agent catalog and ACP validation endpoints
- trust and identity administration
- runtime visibility into worker, reconciler, and stdio ACP state

See [OPERATOR_GUIDE.md](./OPERATOR_GUIDE.md) for operational usage.

## Reliability and Recovery

Implemented reliability features:

- inbound idempotency receipts
- outbox processing
- reconciler recovery loops
- stale queue/run/delivery handling
- retry and circuit-breaker policy for external HTTP integrations
- retention policies for payloads, artifacts, audit rows, and sessions

## Identity and Trust

Implemented trust features:

- canonical user model
- linked identities per channel
- phone capture for cross-channel pairing hints
- step-up challenge windows
- trust policy persistence
- approval channel restrictions
- required linked identity / recent step-up enforcement
- official WhatsApp 24-hour customer service window tracking, template fallback, and STOP-style opt-out handling

## Developer Tooling

Included developer tooling:

- `cmd/migrator`
- `cmd/gateway`
- `cmd/worker`
- `cmd/nexuscli`
- integration tests for stdio OpenCode
- checked-in frontend bundles for webchat and trustadmin

## What Is Not a Feature

Nexus is not:

- a standalone ACP client like `acpx`
- a generic workflow engine
- a multi-tenant SaaS control plane
- a fully symmetric channel product where all channels have identical UX fidelity

The system is intentionally asymmetric across channels, and the renderer layer encodes those tradeoffs.

## Related Docs

- [Architecture](./ARCHITECTURE.md)
- [Configuration](./CONFIGURATION.md)
- [Channel Behavior and Compatibility Matrix](./CHANNEL_MATRIX.md)
