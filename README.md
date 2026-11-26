# muster-fleet-cloud

Cloud transport addon for [muster](https://github.com/Muster-dev/muster). Run muster commands on remote machines through a WebSocket relay with end-to-end encryption — no direct SSH required.

Works with both `muster fleet` (one project, many machines) and `muster group` (many projects, coordinated deploys). Machines on different LANs, behind NATs, or across cloud providers all work through the relay.

This repo contains the **agent** and **tunnel** (client-side binaries). The relay server is [muster-cloud](https://github.com/Muster-dev/muster-cloud), hosted at `relay.getmuster.dev`.

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
- **muster-tunnel** -- CLI helper invoked by `muster fleet` and `muster group` on your laptop. Logs in with the device flow (`login`), connects to the relay, sends end-to-end encrypted commands (`exec`, `push`, `ping`), and prints streamed output. Also lists connected agents.

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

You need a relay: the hosted one at `wss://relay.getmuster.dev` (the default), or your own [muster-cloud](https://github.com/Muster-dev/muster-cloud).

### 1. Connect a machine (on the server)

The easy way: run one command and approve the code. You don't copy any tokens.

```bash
muster cloud connect                 # muster CLI: installs muster-agent if needed, then pairs
# or directly:
muster-agent pair --name prod-1 --project /opt/myapp
```

`pair` prints a code, the approval URL and a QR code of that URL. Scan it with your phone or open the URL, sign in to Fleet Cloud, check the code and machine details, and approve (you can rename the machine there). The agent then enrolls on its own and saves its credential. Options: `--mode muster|push`, `--relay URL`, `--no-qr`, `--config PATH`. The QR code is hidden when stdout isn't a terminal. A denied or expired code exits 1 and changes nothing.

Then keep it running:

```bash
sudo muster-agent install-service    # systemd unit (Linux) / LaunchDaemon (macOS), runs as you
muster-agent install-service --user  # or per-user (systemctl --user / LaunchAgent), no sudo
muster-agent install-service --print # just show the unit/plist and where it would go
muster-agent status --connected      # exit 0 once the running agent is online (3 if not)
```

`install-service` is safe to re-run: it rewrites the unit only when it changed and leaves an unchanged, running service alone. On Linux, `--user` services stop at logout unless you run `sudo loginctl enable-linger $USER` (it tells you).

#### Advanced: join tokens

For automation (cloud-init, Ansible) create a one-time join token in the dashboard or with the relay admin API (`POST /api/v1/tokens`), then:

```bash
muster-agent join \
  --relay wss://relay.getmuster.dev \
  --token mst_agent_<one-time-join-token> \
  --name prod-1 \
  --project /opt/myapp

muster-agent run
```

Both `pair` and `join` end the same way: the one-time join token is swapped for a long-lived **node credential** (`mst_node_...`). It's saved to `~/.muster-agent/config.json` (mode 0600, written atomically) next to the agent's X25519 keypair. `run` and every reconnect use the node credential, and the join token is never needed again. With `join`, the org comes from the token, so `--org` is optional (it is checked if given).

### 2. Log in from your laptop (device flow)

```bash
muster-tunnel login                 # prints a URL + code; approve it in the dashboard
muster-tunnel whoami                # org: org_...
```

The CLI token (`mst_cli_...`) is saved to `~/.muster/cloud.json` (0600). Instead, you can pass `--token`, or set `MUSTER_CLOUD_TOKEN` and `MUSTER_CLOUD_RELAY`.

### 3. Run commands

```bash
muster-tunnel agents                                   # JSON list of connected agents
muster-tunnel ping --agent prod-1                      # ok
muster-tunnel exec --agent prod-1 --cmd "muster deploy api"
muster-tunnel exec --agent prod-1 --cmd "muster deploy api" --env RELEASE=v42 --env NOTIFY=1
```

Output streams back in real time, and the exit code is the remote command's. Ctrl+C (or SIGTERM) asks the agent to stop the command, killing its whole process group, and exits 130. On first contact the CLI pins the agent's public key in `~/.muster/cloud_known_agents.json`. If the key ever changes, the CLI refuses to continue unless you pass `--accept-new-key`.

## Security model

- **End-to-end encryption.** Commands, output and results are sealed with NaCl box (X25519 + XSalsa20-Poly1305) between the CLI's key (`~/.muster/cloud_keys`) and the agent's key. The relay only sees routing headers. Agents reject unencrypted commands, replayed messages and stale timestamps.
- **Org isolation.** The relay derives the org from the credential and routes only inside it.
- **Credentials.** Join tokens are one-time and expire (24 h default). Node credentials can be revoked from the dashboard (`DELETE /api/v1/agents/{name}`). Revocation drops the connection, and the agent exits instead of retrying. All tokens are stored hashed on the relay.
- **Agent command policy.** `exec` runs **without a shell**. The command must match `allowed_commands` word for word as a prefix (`"muster deploy"` allows `muster deploy api`), and shell metacharacters (`; | & $ \` > <`…) are rejected. `push` (piped hook scripts) only works when the agent is in `push` mode. Set `"allow_shell": true` only if every holder of a CLI token for the org may run arbitrary commands on that machine.
- **CLI key pinning.** Agents pin each CLI token's key on first use (`cli_key_policy: tofu`) and reject a different key for that token afterwards, so the relay can't pose as a CLI that already talked to the agent. `strict` accepts only keys added with `muster-agent trust add`. See [docs/protocol.md](docs/protocol.md#cli-key-pinning-v12) for the remaining first-contact gap.
- **Caller context.** Every command gets `MUSTER_VIA_AGENT=1`, `MUSTER_CLOUD_CALLER=<CLI token name>` and `MUSTER_DEPLOY_SOURCE=cloud:<token name>`, set by the agent from what the relay reports. `--env` can't override them.
- **Transport.** The token goes in the first frame, never in a URL or HTTP header. Clients refuse `ws://` to non-local hosts.

## Configuration

### Agent config (`~/.muster-agent/config.json`, or `--config PATH` / `$MUSTER_AGENT_CONFIG`)

Written by `muster-agent pair` or `join`. See `deploy/agent.json.example`.

| Field | Description |
|-------|-------------|
| `relay.url` | Relay URL (`wss://relay.getmuster.dev`) |
| `relay.token` | Node credential (`mst_node_...`), written by `pair` / `join` |
| `identity.org_id` / `identity.name` | Assigned by the relay at enrollment |
| `project.dir` | Default working directory for commands |
| `project.mode` | `muster` (run allowlisted muster commands) or `push` (also accept hook scripts) |
| `muster_path` | Path to the muster binary (`muster` in commands maps to this) |
| `allowed_commands` | Command prefixes the agent will run (argv match, no shell) |
| `allow_shell` | Run `exec` through `bash -c` with no allowlist (default `false`) |
| `allow_plaintext` | Accept unencrypted commands from legacy CLIs (default `false`) |
| `cli_key_policy` | `tofu` (default), `strict` or `off`. See CLI key pinning |
| `reconnect_base_delay` / `reconnect_max_delay` | Backoff bounds |

A running agent also writes `agent-state.json` next to the config (connected or not, since when, last heartbeat; no secrets). `muster-agent status --connected` reads it.

### Deploy as a systemd service

`muster-agent install-service` (see Quick Start) is the simple path: it runs the agent as the user who paired it. For a hardened setup with a dedicated `muster` system user:

```bash
sudo ./deploy/install-agent.sh --binary ./muster-agent \
  --token mst_agent_<join-token> --name prod-1 --project /opt/myapp
```

This creates a `muster` system user and enrolls the agent as that user, into `/var/lib/muster/agent/config.json`. It then installs `deploy/muster-agent.service` and starts it. Logs: `journalctl -u muster-agent -f`.

### Relay reverse proxy

`deploy/caddy/Caddyfile` and `deploy/nginx/relay.conf` are ready-made configs for `relay.getmuster.dev`. They handle TLS, WebSocket upgrade, passing the real client IP to the relay (with Cloudflare in front), and exposing only the relay's routes.

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
    "relay": "wss://relay.getmuster.dev",
    "org_id": "myorg",
    "token": "mst_cli_<your-token>"
  }
}
```

Or via CLI: `muster settings --global cloud.relay '"wss://relay.getmuster.dev"'`

### How the Bash CLI Calls the Tunnel

`lib/core/cloud.sh` shells out to `muster-tunnel`:

| Bash function | Tunnel command |
|---|---|
| `_fleet_cloud_exec "$agent" "$cmd" "$cwd"` | `muster-tunnel exec --agent <name> --cmd <cmd> --cwd <dir>` |
| `_fleet_cloud_push "$agent" "$hook" "$env"` | `muster-tunnel push --agent <name> --hook <file> --env K=V [--env K2=V2 ...]` |
| `_fleet_cloud_check "$agent"` | `muster-tunnel ping --agent <name>` |

Relay URL, token, and org are passed via `--relay`, `--token`, `--org` flags. All three are optional now: they default to `MUSTER_CLOUD_*` env vars and `~/.muster/cloud.json`, and `--org` is only a check.

> **Note:** agents run `exec` commands without a shell. Compound commands such as `export X=1; muster status --json` are rejected unless the agent sets `allow_shell: true`. Send plain `muster ...` commands with `--cwd` and use `push` mode for scripts.

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

Muster Transport Protocol (MTP) is a binary frame protocol over WebSocket (89-byte header + payload). The full spec, including authentication, enrollment, routing rules and the encryption format, is in [docs/protocol.md](docs/protocol.md). The relay's HTTP API (device login, tokens, agents) is in [docs/cloud-api.md](docs/cloud-api.md).

## Shared Packages (`pkg/`)

These packages are exported for use by the relay server and other tools:

| Package | Description |
|---------|-------------|
| `pkg/protocol` | MTP frames, message types, auth/hello/command payload types, name validation |
| `pkg/tunnel` | WebSocket transport (size limits, deadlines), relay client, reconnect loop, CLI `Session` (key exchange, encrypted exec) |
| `pkg/crypto` | X25519 key pairs, NaCl box, `Seal`/`Open` with embedded sender key |
| `pkg/config` | Agent config, CLI credential file, pinned agent keys, atomic 0600 writes |

## License

MIT
