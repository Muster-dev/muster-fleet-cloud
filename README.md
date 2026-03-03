# muster-fleet-cloud

Cloud transport addon for [muster](https://github.com/Muster-dev/muster). Run muster commands on remote machines through a WebSocket relay with end-to-end encryption — no direct SSH required.

Works with both `muster fleet` (one project, many machines) and `muster group` (many projects, coordinated deploys). Machines on different LANs, behind NATs, or across cloud providers all work through the relay.

This repo contains the **agent** and **tunnel** (client-side binaries). The relay server is hosted separately.

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
                  | relay server  |
                  | (your hosting)|
                  |               |
                  | :8443/v1/tunnel
                  +---------------+
```

**muster-tunnel** sends commands from your laptop. The relay routes them over WebSocket. **muster-agent** receives commands on the remote machine and executes muster locally. Payloads are end-to-end encrypted (X25519 + XSalsa20-Poly1305) -- the relay cannot read command content.

## Binaries

- **muster-agent** -- Daemon that runs on each remote machine. Maintains a persistent WebSocket connection to the relay, receives commands, executes them locally via muster, and streams output back.
- **muster-tunnel** -- CLI helper invoked by `muster fleet` and `muster group` on your laptop. Connects to the relay, sends commands (`exec`, `push`, `ping`), and prints streamed output. Also lists connected agents.

## Install

```bash
# Install via muster installer (recommended)
curl -fsSL https://getmuster.dev/install.sh | bash

# Or install agent + tunnel directly (latest release)
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

## Quick Start

### 1. Join an agent

On the remote machine:

```bash
muster-agent join \
  --relay wss://relay.example.com:8443 \
  --token mst_agent_<join-token> \
  --org myorg \
  --name prod-1 \
  --project /opt/myapp

muster-agent run
```

### 2. Run a command

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

### Deploy as systemd service

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

## Integration with Muster CLI

The muster bash CLI calls `muster-tunnel` automatically when a fleet machine or group project has cloud transport enabled.

### Fleet (one project → many machines)

```bash
# Add a cloud machine to your fleet
muster fleet add prod-east deploy@prod-east --transport cloud --path /opt/myapp

# Deploy — cloud machines route through the relay, SSH machines use direct SSH
muster fleet deploy
```

Config in `remotes.json`:
```json
{
  "machines": {
    "prod-east": {
      "host": "prod-east",
      "user": "deploy",
      "transport": "cloud",
      "project_dir": "/opt/myapp",
      "mode": "muster"
    }
  }
}
```

### Group (many projects → coordinated deploy)

```bash
# Add a cloud project to a group
muster group add production agent-prod-east --cloud --path /opt/api

# Deploy all projects in the group
muster group deploy production
```

Config in `~/.muster/groups.json`:
```json
{
  "groups": {
    "production": {
      "projects": [
        {"type": "local", "path": "/home/me/frontend"},
        {"type": "remote", "host": "agent-prod-east", "user": "deploy",
         "cloud": true, "project_dir": "/opt/api"}
      ]
    }
  }
}
```

### Global Cloud Settings

Set once in `~/.muster/settings.json`:
```json
{
  "cloud": {
    "relay": "wss://relay.example.com",
    "org_id": "myorg",
    "token": "mst_cli_<your-token>"
  }
}
```

Or via CLI: `muster settings --global cloud.relay '"wss://relay.example.com"'`

### How the Bash CLI Calls the Tunnel

`lib/core/cloud.sh` shells out to `muster-tunnel`:

| Bash function | Tunnel command |
|---|---|
| `_fleet_cloud_exec "$agent" "$cmd" "$cwd"` | `muster-tunnel exec --agent <name> --cmd <cmd> --cwd <dir>` |
| `_fleet_cloud_push "$agent" "$hook" "$env"` | `muster-tunnel push --agent <name> --hook <file> --env <vars>` |
| `_fleet_cloud_check "$agent"` | `muster-tunnel ping --agent <name>` |

Relay URL, token, and org are passed via `--relay`, `--token`, `--org` flags.

## Development

```bash
# Build both binaries
make build

# Build individually
make agent
make tunnel

# Run tests
make test

# Cross-compile for all platforms (linux + darwin, amd64 + arm64)
make dist

# Build release with checksums
make release VERSION=0.3.0

# Upload to GitHub
gh release create v0.3.0 dist/* --title v0.3.0
```

Requires Go 1.24+. Dependencies: `golang.org/x/crypto` (NaCl box, X25519), `golang.org/x/net` (WebSocket).

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

## Shared Packages (`pkg/`)

These packages are exported for use by the relay server and other tools:

| Package | Description |
|---------|-------------|
| `pkg/protocol` | MTP frame encoding/decoding, message types, flags |
| `pkg/tunnel` | WebSocket client, connection management, reconnection logic |
| `pkg/crypto` | X25519 key pairs, NaCl box encryption/decryption |
| `pkg/config` | Agent and relay configuration structures |

## License

MIT
