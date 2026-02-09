# nats-bridge

Sidecar container that bridges NATS JetStream messages to OpenClaw webhook endpoints for the Knights of the Round Table multi-agent platform.

## What It Does

1. Connects to NATS JetStream
2. Subscribes to fleet-scoped task topics (e.g., `fleet-tim.tasks.security.>`)
3. Forwards incoming task messages to the local OpenClaw webhook as chat completion requests
4. Publishes OpenClaw responses back to NATS on result topics
5. Sends periodic heartbeats
6. Exposes `/healthz` and `/readyz` on port 8080 for Kubernetes probes

## Configuration

| Variable | Default | Description |
|---|---|---|
| `NATS_URL` | `nats://nats.roundtable.svc:4222` | NATS server URL |
| `FLEET_ID` | `fleet-default` | Fleet identifier |
| `AGENT_ID` | `unknown` | Agent identifier |
| `SUBSCRIBE_TOPICS` | `<fleet-id>.tasks.>` | Comma-separated NATS subjects |
| `OPENCLAW_WEBHOOK_URL` | `http://localhost:18789/v1/chat/completions` | OpenClaw endpoint |
| `OPENCLAW_TOKEN` | (none) | Bearer token for OpenClaw |
| `HEARTBEAT_INTERVAL` | `60` | Heartbeat interval in seconds |
| `RESULT_PREFIX` | `<fleet-id>.results` | Topic prefix for results |

## Message Flow

```
NATS JetStream → nats-bridge → OpenClaw webhook
                                    ↓
NATS JetStream ← nats-bridge ← OpenClaw response
```

## Build

```bash
go build -o nats-bridge .
```

## Docker

```bash
docker build -t nats-bridge .
docker run -e NATS_URL=nats://localhost:4222 -e FLEET_ID=fleet-tim -e AGENT_ID=galahad nats-bridge
```

## License

MIT
