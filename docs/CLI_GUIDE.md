# Nexus CLI Guide

The Nexus CLI is a dev-only wrapper around the existing webchat HTTP API. It is intended for local testing of the full Nexus path:

1. CLI command
2. webchat API
3. DB and outbox queue
4. worker
5. ACP or OpenCode bridge
6. webchat history

It is not a standalone ACP client and does not add a separate `cli` channel.

## Start The Gateway

Enable the dev-only webchat session endpoint:

```bash
WEBCHAT_DEV_AUTH=true go run ./cmd/gateway
```

To test with OpenCode over stdio:

```bash
DATABASE_URL='postgres://midas:midas@localhost:5432/nexus?sslmode=disable' \
WEBCHAT_DEV_AUTH=true \
ACP_IMPLEMENTATION=stdio \
ACP_COMMAND=opencode \
ACP_WORKDIR="$PWD" \
DEFAULT_ACP_AGENT_NAME=build \
go run ./cmd/gateway
```

`POST /webchat/dev/session` returns `404` unless all of these are true:

- `WEBCHAT_DEV_AUTH=true`
- `NEXUS_ENV=development`
- the request host is loopback, such as `localhost` or `127.0.0.1`

## Log In

Create a dev webchat session without email OTP:

```bash
go run ./cmd/nexuscli dev-login \
  --base-url http://localhost:8080 \
  --email dev@example.com
```

Use a loopback base URL. The dev session endpoint intentionally rejects non-local hosts.

The CLI stores the gateway URL, session cookie, and CSRF token in:

```text
$XDG_CONFIG_HOME/nexus/cli.json
```

Override the state file for isolated tests:

```bash
NEXUSCLI_STATE=/tmp/nexuscli-test.json go run ./cmd/nexuscli dev-login \
  --base-url http://localhost:8080 \
  --email dev@example.com
```

## Send A Message

```bash
go run ./cmd/nexuscli send "Reply with exactly: nexus cli opencode ok"
```

The command sends a multipart `POST /webchat/messages` request with the stored session cookie and CSRF token. The response should include an accepted queue item:

```json
{
  "data": {
    "status": "accepted",
    "queue_id": "queue_..."
  }
}
```

## Read History

```bash
go run ./cmd/nexuscli history --limit 20
```

This calls `GET /webchat/history` using the stored session. After the worker finishes, assistant messages appear in the returned `data.items` list.

## Respond To An Await

```bash
go run ./cmd/nexuscli respond \
  --await-id await_123 \
  --reply "approve"
```

This calls `POST /webchat/awaits/respond` with the stored session and CSRF token.

## Cleanup

Remove local CLI state:

```bash
rm -f "${XDG_CONFIG_HOME:-$HOME/.config}/nexus/cli.json"
```

For isolated test state:

```bash
rm -f /tmp/nexuscli-test.json
```
