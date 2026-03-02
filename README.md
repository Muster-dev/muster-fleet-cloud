# muster-fleet-cloud

Remote deploy transport for [muster](https://github.com/Muster-dev/muster). Run muster commands on cloud machines through a WebSocket relay with end-to-end encryption.

## Architecture

```
  Your laptop                         Cloud / VPS
  +--------------+                    +---------------+
  | muster CLI   |                    | muster-agent  |
  | (bash)       |                    | (Go daemon)   |
  |   |          |                    |   |           |
  |   v          |                    |   v           |
  | muster-tunnel|   MTP/WebSocket    | muster        |
  | (Go helper)  | ---- wss:// -----> | (bash, local) |
  +--------------+        |           +---------------+
                          |
                  +-------+-------+
                  | muster-cloud  |
                  | (relay server)|
                  |               |
                  | :8443/v1/tunnel
                  +---------------+
```

**muster-tunnel** sends commands from your laptop. **muster-cloud** relays them over WebSocket. **muster-agent** receives commands on the remote machine and executes muster locally. Payloads are end-to-end encrypted (X25519 + XSalsa20-Poly1305) -- the relay cannot read command content.

## Quick Start

### 1. Start the relay

```bash
# Build and run (development)
make cloud
./muster-cloud --listen :8443
```

### 2. Create tokens

```bash
# Create an admin token for managing the relay
./muster-cloud token create --type admin --org myorg

# Create a join token for an agent
./muster-cloud token create --type agent-join --org myorg --name prod-1

# Create a CLI token for tunnel access
./muster-cloud token create --type cli --org myorg --name laptop
```

Save the printed tokens immediately -- they cannot be retrieved later.

### 3. Join an agent

On the remote machine:

```bash
curl -fsSL https://raw.githubusercontent.com/Muster-dev/muster-fleet-cloud/main/install.sh | bash -s -- --agent

muster-agent join \
  --relay wss://relay.example.com:8443 \
  --token mst_agent_<join-token> \
  --org myorg \
  --name prod-1 \
  --project /opt/myapp

muster-agent run
```

### 4. Run a command

From your laptop:

```bash
muster-tunnel exec \
  --relay wss://relay.example.com:8443 \
  --token mst_cli_<cli-token> \
  --org myorg \
  --agent prod-1 \
  --cmd "muster deploy api"
```

Output streams back in real time.

## Binaries

- **muster-cloud** -- Relay server. Accepts WebSocket connections, authenticates tokens, routes MTP frames between CLI clients and agents. Manages token lifecycle (create/list/revoke).
- **muster-agent** -- Daemon that runs on each remote machine. Maintains a persistent WebSocket connection to the relay, receives commands, executes them locally via muster, and streams output back.
- **muster-tunnel** -- CLI helper invoked by `muster fleet` on your laptop. Connects to the relay, sends commands (`exec`, `push`, `ping`), and prints streamed output. Also lists connected agents.

## Install

```bash
# Install agent + tunnel (latest release)
curl -fsSL https://raw.githubusercontent.com/Muster-dev/muster-fleet-cloud/main/install.sh | bash

# Agent only
curl -fsSL ... | bash -s -- --agent

# Tunnel only
curl -fsSL ... | bash -s -- --tunnel

# Specific version, custom prefix
curl -fsSL ... | bash -s -- --all --version 0.2.0 --prefix /usr/local/bin
```

Binaries go to `~/.muster/bin` by default. The installer adds it to your PATH.

Supported platforms: linux/amd64, linux/arm64, darwin/amd64, darwin/arm64.

## Configuration

### Agent config (`~/.muster-agent/config.json`)

Created automatically by `muster-agent join`. See `deploy/agent.json.example` for the full schema.

```json
{
  "relay": {
    "url": "wss://relay.example.com:8443",
    "token": "mst_agent_..."
  },
  "identity": {
    "org_id": "myorg",
    "name": "prod-1"
  },
  "project": {
    "dir": "/opt/myapp",
    "mode": "muster"
  },
  "muster_path": "/usr/local/bin/muster",
  "allowed_commands": [
    "muster deploy",
    "muster status",
    "muster rollback",
    "muster logs"
  ],
  "reconnect_base_delay": "1s",
  "reconnect_max_delay": "60s"
}
```

| Field | Description |
|-------|-------------|
| `relay.url` | Relay WebSocket URL |
| `relay.token` | Agent authentication token |
| `identity.org_id` | Organization scope |
| `identity.name` | Unique agent name within the org |
| `project.dir` | Working directory for muster commands |
| `project.mode` | `muster` (run muster locally) or `push` (receive hook scripts) |
| `allowed_commands` | Whitelist of commands the agent will execute |
| `reconnect_base_delay` | Initial reconnect delay (exponential backoff) |
| `reconnect_max_delay` | Maximum reconnect delay |

### Relay flags

```
muster-cloud [options]

  --listen <addr>        Listen address (default: :8443, env: LISTEN)
  --tls-cert <path>      TLS certificate (env: TLS_CERT)
  --tls-key <path>       TLS private key (env: TLS_KEY)
  --token-store <path>   Token store file (default: tokens.json, env: TOKEN_STORE)
```

### Token management

Three token types, identified by prefix:

| Type | Prefix | Purpose |
|------|--------|---------|
| `admin` | `mst_admin_` | Relay administration |
| `agent-join` | `mst_agent_` | One-time agent registration |
| `cli` | `mst_cli_` | CLI/tunnel access |

Tokens are SHA-256 hashed before storage. The raw token is shown once at creation.

```bash
muster-cloud token create --type agent-join --org myorg --name prod-1
muster-cloud token list --org myorg
muster-cloud token revoke <token-id>
```

## Deploy to Production

### systemd service

Copy `deploy/muster-agent.service` to `/etc/systemd/system/`:

```bash
sudo useradd --system --home-dir /var/lib/muster --create-home muster
sudo mkdir -p /etc/muster
sudo cp deploy/agent.json.example /etc/muster/agent.json
# Edit /etc/muster/agent.json with your relay URL, token, etc.

sudo cp deploy/muster-agent.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now muster-agent
sudo journalctl -u muster-agent -f
```

The service runs as a dedicated `muster` user with systemd security hardening (ProtectSystem, NoNewPrivileges, PrivateTmp).

### TLS

**Native TLS** (relay terminates TLS directly):

```bash
muster-cloud --listen :8443 --tls-cert /etc/letsencrypt/live/relay.example.com/fullchain.pem \
                             --tls-key /etc/letsencrypt/live/relay.example.com/privkey.pem
```

**Reverse proxy** (nginx/Caddy terminates TLS, relay listens on localhost):

```bash
# relay
muster-cloud --listen 127.0.0.1:8080

# nginx
location /v1/tunnel {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_read_timeout 86400s;
}
```

The relay exposes `/healthz` for load balancer health checks.

## Development

```bash
# Build all three binaries
make build

# Build individually
make agent
make cloud
make tunnel

# Run tests
make test

# Cross-compile for all platforms (linux + darwin, amd64 + arm64)
make dist

# Build release with checksums
make release VERSION=0.2.0

# Upload to GitHub
gh release create v0.2.0 dist/* --title v0.2.0
```

Requires Go 1.24+. Dependencies: `golang.org/x/crypto` (NaCl box, X25519), `golang.org/x/net` (WebSocket).

CI runs `go vet` and `make test` on push and PR to main.

## Protocol

Muster Transport Protocol (MTP) is a binary frame protocol carried over WebSocket.

### Frame layout (89-byte header + variable payload)

```
Offset  Size  Field
0       1     Version (0x01)
1       1     Message type
2       4     Payload length (big-endian uint32)
6       16    Request ID (UUID)
22      32    Source identity (zero-padded)
54      32    Destination identity (zero-padded)
86      1     Flags
87      2     Reserved
89      N     Payload (JSON or encrypted blob)
```

### Message types

| Code | Name | Direction |
|------|------|-----------|
| 0x01 | AUTH_REQUEST | Client -> Relay |
| 0x02 | AUTH_RESPONSE | Relay -> Client |
| 0x03 | AGENT_HELLO | Agent -> Relay |
| 0x04 | RELAY_ACK | Relay -> Agent |
| 0x05 | HEARTBEAT | Bidirectional |
| 0x10 | COMMAND | CLI -> Agent (via relay) |
| 0x11 | COMMAND_ACK | Agent -> CLI |
| 0x12 | COMMAND_RESULT | Agent -> CLI |
| 0x20 | STREAM_DATA | Agent -> CLI |
| 0x30 | KEY_EXCHANGE | Bidirectional |
| 0xF0 | ERROR | Relay -> Client |

### Flags

| Bit | Name | Meaning |
|-----|------|---------|
| 0x01 | ENCRYPTED | Payload is E2E encrypted |
| 0x02 | COMPRESSED | Payload is compressed |
| 0x04 | STREAM_CONTINUED | More stream frames follow |
| 0x08 | STREAM_END | Final stream frame |

### End-to-end encryption

Agents generate an X25519 key pair at join time. Encrypted payloads use NaCl box (XSalsa20-Poly1305): a 24-byte random nonce is prepended to the ciphertext. The relay routes frames by destination identity but cannot decrypt the payload.

## License

MIT
