# Muster Transport Protocol (MTP) v1.2

Specification for relay servers and clients in the Muster Fleet Cloud system.
The HTTP side of the relay (device login, tokens, agents) is specified in
[cloud-api.md](cloud-api.md).

The relay is a WebSocket server that routes binary frames between **CLI
clients** (`muster-tunnel`) and **agents** (`muster-agent` on remote
machines). It authenticates connections, tracks online agents per org, and
forwards frames within an org. Command payloads are end-to-end encrypted
between CLI and agent, so the relay cannot read them.

> **v1.2 changes from v1.1** (muster-fleet-cloud v0.3.1, muster-cloud relay
> with SESSION_INFO support): remote cancel (`COMMAND_CANCEL`), relay → agent
> session notifications (`SESSION_INFO`, `SESSION_CLOSED`), agent-controlled
> caller environment, mode discovery in `AGENT_HELLO`/`AGENT_LIST`, and CLI
> key pinning on agents. All additions are optional fields or new message
> types, which v1.1 peers ignore; see [Compatibility](#compatibility-v12--v11).
>
> **v1.1 changes from v1:** org is derived from the token, header identities
> are bare names, agents enroll with a join token and receive a node token,
> the relay stamps SourceID and only routes within an org, and payloads are
> always encrypted with the sender key embedded. The frame format is unchanged.

---

## Wire Format

Every message is a single WebSocket **binary** message: a fixed 89-byte
header followed by the payload.

```
Offset  Size  Type        Field
------  ----  ----------  ------------------------------------------
0       1     uint8       Version         (must be 0x01)
1       1     uint8       MsgType         (see Message Types; 0 is invalid)
2       4     uint32 BE   PayloadLength
6       16    [16]byte    RequestID       (random; correlates request/response)
22      32    [32]byte    SourceID        (sender name, zero-padded)
54      32    [32]byte    DestID          (recipient name or "relay", zero-padded)
86      1     uint8       Flags
87      2     -           Reserved (0)
89      N     []byte      Payload         (JSON, or encrypted blob)
```

**Size limit:** the relay rejects messages larger than 1 MiB (configurable
with `--max-message-bytes`) by closing the connection.

### Identities

SourceID/DestID hold a **bare name**, 1–32 bytes of `[A-Za-z0-9._-]`
starting with an alphanumeric, zero-padded. The org is never in the header.
The relay knows each connection's org from its credential.

- Agent names are chosen at enrollment and are unique per org.
- CLI names are **assigned by the relay** per connection (`cli-xxxxxxxx`) and
  returned in AUTH_RESPONSE.
- `"relay"` addresses the relay itself.
- For compatibility, a DestID of the form `"<org>/<name>"` is accepted. The
  part before the last `/` is ignored.

### Flags

| Bit  | Name                  | Meaning                               |
|------|-----------------------|---------------------------------------|
| 0x01 | `FlagEncrypted`       | Payload is an encrypted blob (below)  |
| 0x02 | `FlagCompressed`      | Reserved                              |
| 0x04 | `FlagStreamContinued` | More STREAM_DATA frames follow        |
| 0x08 | `FlagStreamEnd`       | Final stream frame                    |

---

## Message Types

| Hex  | Name               | Direction             | Payload                  |
|------|--------------------|-----------------------|--------------------------|
| 0x01 | AUTH_REQUEST       | Client → Relay        | JSON                     |
| 0x02 | AUTH_RESPONSE      | Relay → Client        | JSON                     |
| 0x03 | AGENT_HELLO        | Agent → Relay         | JSON                     |
| 0x04 | RELAY_ACK          | Relay → Agent         | empty                    |
| 0x05 | HEARTBEAT          | Client → Relay, CLI → Agent | empty              |
| 0x06 | HEARTBEAT_ACK      | Relay → Client, Agent → CLI | empty              |
| 0x10 | COMMAND            | CLI → Agent (routed)  | encrypted                |
| 0x11 | COMMAND_ACK        | Agent → CLI (routed)  | empty                    |
| 0x12 | COMMAND_RESULT     | Agent → CLI (routed)  | encrypted                |
| 0x13 | COMMAND_ERROR      | Agent → CLI (routed)  | encrypted, or JSON before decryption |
| 0x14 | COMMAND_CANCEL     | CLI → Agent (routed)  | empty (v1.2)             |
| 0x20 | STREAM_DATA        | Agent → CLI (routed)  | encrypted                |
| 0x21 | STREAM_END         | Agent → CLI (routed)  | empty                    |
| 0x30 | KEY_EXCHANGE       | CLI → Agent (routed)  | JSON                     |
| 0x31 | KEY_EXCHANGE_ACK   | Agent → CLI (routed)  | JSON                     |
| 0x40 | SESSION_INFO       | Relay → Agent         | JSON (v1.2)              |
| 0x41 | SESSION_CLOSED     | Relay → Agent         | JSON (v1.2)              |
| 0xF0 | ERROR              | Relay → Client        | JSON                     |
| 0xF1 | AGENT_LIST         | Relay → CLI           | JSON array               |
| 0xF2 | AGENT_LIST_REQUEST | CLI → Relay           | empty                    |

### Error codes (`code` in ERROR / COMMAND_ERROR / failed AUTH_RESPONSE)

| Code                 | Meaning                                         |
|----------------------|-------------------------------------------------|
| `E_AUTH`             | Authentication failed                           |
| `E_NOT_CONNECTED`    | Target agent is not connected (in your org)     |
| `E_BAD_FRAME`        | Message type not allowed for this sender/target |
| `E_COMMAND_REJECTED` | Agent refused the command (allowlist, validation, replay) |
| `E_DECRYPT`          | Payload could not be decrypted / encryption required |
| `E_TIMEOUT`          | Operation timed out                             |
| `E_AGENT_BUSY`       | Agent is at its concurrent-command limit        |

---

## Endpoint

```
wss://relay.getmuster.dev/v1/tunnel      (binary frames only)
```

The relay listens on `127.0.0.1:8443` by default behind a TLS-terminating
proxy. Clients refuse plain `ws://` to non-loopback hosts
(`MUSTER_CLOUD_INSECURE=1` overrides this, for testing only).

---

## Connection Flows

### 1. Authentication

The first frame on every connection must be AUTH_REQUEST, sent within **5
seconds** of the upgrade, or the relay closes the socket. Failed attempts
count against a per-IP limit. After too many, upgrades from that IP get
HTTP 429 for a while.

```
AUTH_REQUEST  DestID "relay"
{
  "token":       "mst_cli_... | mst_agent_... | mst_node_...",
  "client_type": "cli" | "agent",
  "org_id":      "org_x",        // optional; must match the token's org
  "name":        "prod-1",       // agents: requested name when enrolling
  "version":     "0.2.0"
}

AUTH_RESPONSE (success)
{ "ok": true, "org_id": "org_x", "name": "prod-1", "session_id": "sess_...",
  "node_token": "mst_node_..." }   // only on enrollment
AUTH_RESPONSE (failure; the relay then closes)
{ "ok": false, "code": "E_AUTH", "message": "invalid or expired token" }
```

| client_type | accepted tokens          | resulting name                         |
|-------------|--------------------------|----------------------------------------|
| `cli`       | `mst_cli_`               | relay-assigned `cli-xxxxxxxx`          |
| `agent`     | `mst_agent_` (join)      | requested name, or the join token's machine name |
| `agent`     | `mst_node_`              | the name bound to the node credential  |

Admin tokens are not accepted on the WebSocket.

#### Enrollment (join token → node token)

A join token is **one-time**. On the first successful AUTH_REQUEST with a
`mst_agent_` token, the relay does all of the following atomically:
validates the token (unused, unexpired, org matches), creates the node
record and a `mst_node_` credential, marks the join token used, and returns
the credential in `node_token`. The agent must persist it (mode 0600) before
doing anything else and use it for every later connection.

A join token presented again is rejected. Before v1.1, agents stored the
join token and reused it, so every restart or reconnect failed with "token
already used".

#### Pairing (code + QR → join token), agent ≥ 0.4.0, relay ≥ 0.3.3

`muster-agent pair` gets its join token over HTTPS instead of from the user,
then enrolls exactly as above. The endpoints are in `docs/cloud-api.md` →
Machine pairing.

1. `POST /api/v1/pair/start` with the machine's name (optional), hostname,
   os/arch, agent version and whether a project dir is set. The reply has a
   `pair_code` (`XXXX-XXXX`, same alphabet as device-login codes), an opaque
   `device_code`, `verification_uri_complete`, `expires_in` (600) and
   `interval` (3 s).
2. The agent prints the code, the URL and (on a TTY, unless `--no-qr`) a QR
   code of the URL: Unicode half blocks, black on white with explicit SGR
   colours and a 4-module quiet zone, so it scans on dark and light themes.
3. It polls `POST /api/v1/pair/token` every `interval`. `slow_down` and HTTP
   429 add 5 s, network errors and 5xx are retried, and `access_denied`,
   `expired_token` or passing `expires_in` end the attempt (exit 1, nothing
   written).
4. On approval the reply carries a fresh one-time `join_token` bound to the
   approving org and the (possibly renamed) machine name, valid 15 minutes.
   The agent connects with it, receives its `mst_node_` credential, and
   writes the config (0600). The config keeps the relay URL given with
   `--relay`. The `relay_url` in the reply is informational only.

A relay without these endpoints answers 404. The agent then says so and
points to `muster-agent join`.

### 2. Agent Registration

After AUTH_RESPONSE an agent sends AGENT_HELLO (also within 5 seconds):

```json
{ "agent_name": "prod-1", "version": "0.3.1",
  "public_key": "<base64 X25519, 32 bytes>",
  "hostname": "web-1", "os": "linux", "arch": "amd64",
  "mode": "muster", "project_dir": true,
  "allowed_commands": ["muster deploy", "muster status"],
  "allow_shell": false,
  "features": ["cancel", "caller-env", "cli-key-pinning"] }
```

The last five fields are new in v1.2 (older agents omit them):

| Field              | Meaning |
|--------------------|---------|
| `mode`             | Project mode, `muster` or `push` (push also accepts `push_hook`) |
| `project_dir`      | A project directory is configured **and exists**. The path is never sent |
| `allowed_commands` | The exec allowlist (word-prefix entries, see [Agent command policy](#agent-command-policy)) |
| `allow_shell`      | `exec` runs through `bash -c` with no allowlist |
| `features`         | `cancel` (honours COMMAND_CANCEL / SESSION_CLOSED), `caller-env`, `cli-key-pinning` (policy is not `off`) |

The relay validates `public_key`, records the metadata on the node (lists
are capped at 64 entries of 200 bytes), marks the agent online and replies
RELAY_ACK. A second connection with the same identity replaces the first.

### 3. Routing

The relay routes a frame to `DestID` **within the sender's org only**:

- CLI → agent: `COMMAND`, `COMMAND_CANCEL`, `KEY_EXCHANGE`, `HEARTBEAT`
- agent → CLI: `COMMAND_ACK`, `COMMAND_RESULT`, `COMMAND_ERROR`,
  `STREAM_DATA`, `STREAM_END`, `KEY_EXCHANGE_ACK`, `HEARTBEAT_ACK`

Any other type addressed to a peer gets `ERROR E_BAD_FRAME`. An unknown or
offline destination (including any agent in another org) gets
`ERROR E_NOT_CONNECTED`, with the original RequestID, to a CLI sender.

Before forwarding, the relay overwrites **SourceID** with the sender's
authenticated name, so peers can't spoof it. The payload is forwarded
byte-for-byte.

#### Session notifications (v1.2)

The relay itself (SourceID `"relay"`) tells agents about the CLI connections
that talk to them. No client can send these types to a peer (`E_BAD_FRAME`),
and no client can have SourceID `"relay"`, so an agent accepts them only with
SourceID `"relay"`.

```jsonc
// SESSION_INFO: sent before the first frame a CLI connection routes to an
// agent connection (again if the agent reconnects).
{ "cli": "cli-3f9a1c2e", "token_id": "tok_ab12...", "token_name": "muster-cli on laptop" }
// SESSION_CLOSED: sent to every agent the CLI talked to when it disconnects.
{ "cli": "cli-3f9a1c2e" }
```

`token_id` and `token_name` describe the CLI token the connection
authenticated with. They come from the relay's store, never from the CLI.
Frames on one WebSocket are ordered, so SESSION_INFO always arrives before
that CLI's KEY_EXCHANGE or COMMAND.

### 4. Heartbeats and idle timeout

Clients send `HEARTBEAT` (DestID `"relay"`) every 30 s. The relay answers
`HEARTBEAT_ACK`. The relay closes any connection that sends nothing for 90 s.
Agents reconnect if nothing arrives for 95 s. A CLI may also ping an agent:
HEARTBEAT with DestID set to the agent, which the agent answers.

### 5. Agent list

`AGENT_LIST_REQUEST` (CLI only) → `AGENT_LIST` with a JSON array of the
org's connected agents:

```json
[{"name":"prod-1","org_id":"org_x","version":"0.3.1","hostname":"web-1","os":"linux",
  "arch":"amd64","connected_at":"…","last_seen":"…","public_key":"<base64>",
  "mode":"muster","project_dir":true,"allowed_commands":["muster deploy"],
  "features":["cancel","caller-env","cli-key-pinning"]}]
```

`mode`, `project_dir`, `allowed_commands`, `allow_shell` and `features`
appear only for v1.2 agents (`project_dir` is absent, not `false`, for an
agent that did not report it).

---

## End-to-End Encryption

- Key agreement **X25519**, authenticated encryption **NaCl box**
  (XSalsa20-Poly1305).
- Agents have a long-lived keypair (`keys/identity.key`, 0600).
- v1.2 CLIs also have a long-lived keypair (`~/.muster/cloud_keys/identity.key`,
  0600, override with `MUSTER_CLOUD_KEY_DIR`), created on first use, which
  agents pin per CLI token (see [CLI key pinning](#cli-key-pinning-v12)).
  v1.1 CLIs generate a fresh keypair per session.

### Encrypted payload format (`FlagEncrypted`)

```
[0:32]    sender's X25519 public key
[32:56]   24-byte random nonce
[56:N]    NaCl box ciphertext (plaintext + 16-byte Poly1305 tag)
```

The overhead is 72 bytes. Carrying the sender key lets the agent reply
without session state. The box MAC authenticates it.

### Flow

1. CLI → `KEY_EXCHANGE {"public_key": "<cli ephemeral>"}`, agent →
   `KEY_EXCHANGE_ACK {"public_key": "<agent>"}`.
2. The CLI **pins** the agent key (trust on first use, like SSH
   `known_hosts`) in `~/.muster/cloud_known_agents.json`, keyed by
   relay+org+agent. If the key changes, the CLI refuses unless the user
   passes `--accept-new-key`. That is what stops a malicious relay from
   swapping keys after first contact.
3. CLI → `COMMAND` encrypted to the agent key. The plaintext is a
   `CommandRequest` with `ts` (Unix seconds).
4. The agent decrypts. It rejects a reused nonce (replay cache), a `ts` more
   than 5 min off its clock, and any unencrypted COMMAND (unless
   `allow_plaintext` is set). It answers `COMMAND_ACK`, then streams
   `STREAM_DATA` and finishes with `COMMAND_RESULT`, all encrypted to the CLI
   key from the command. The CLI rejects replies not sealed by the pinned
   agent key.

### Payloads (plaintext, before encryption)

```jsonc
// COMMAND
{ "action": "exec" | "push_hook" | "deploy" | "status" | "rollback" | "logs",
  "command": "muster status", "services": ["api"], "env": {"K":"v"},
  "cwd": "/abs/path", "stream": true, "ts": 1764180000,
  "persistent_key": true }          // v1.2: sealed with the CLI's long-lived key
// STREAM_DATA
{ "line": "output text", "stream": "stdout" }
// COMMAND_RESULT
{ "exit_code": 0, "status": "done" }   // or {"exit_code":130,"status":"cancelled"}
// COMMAND_ERROR
{ "code": "E_COMMAND_REJECTED", "message": "..." }
```

### Agent command policy

- `exec`: the command line is split into argv **without a shell**. Unquoted
  shell metacharacters (`; & | < > $ \` ( )` and newlines) are rejected, and
  the leading words must match an `allowed_commands` entry ("muster deploy"
  allows `muster deploy api`). The binary is executed directly, and `muster`
  maps to `muster_path`. `allow_shell: true` switches to `bash -c` with no
  allowlist. Only enable it if every CLI token holder of the org may run
  anything on the machine.
- `push_hook` (script piped to `bash -s`): only when the agent's project
  `mode` is `push`.
- `deploy/status/rollback/logs`: `muster <action> --json [services]`. Service
  names are validated.
- `env`: names must be `[A-Za-z_][A-Za-z0-9_]*`. Loader and shell variables
  (`LD_*`, `DYLD_*`, `PATH`, `BASH_ENV`, `IFS`, …) are refused. The caller
  variables below are overwritten by the agent, whatever `env` says.
- `cwd` must be absolute. At most 4 commands run concurrently (`E_AGENT_BUSY`).

**Default allowlist** (`muster-agent join` writes it into new configs;
existing configs keep their list, edit `allowed_commands` to change it):

| Entry                 | Notes |
|-----------------------|-------|
| `muster deploy`       | any arguments (`muster deploy api --force`) |
| `muster status`       | |
| `muster rollback`     | |
| `muster logs`         | streams until stopped. muster has no `--tail` mode yet, so a follow can't be prevented; v1.2 CLIs end it with COMMAND_CANCEL |
| `muster history`      | read-only |
| `muster --version`    | read-only |
| `muster hooks verify` | read-only part of `muster hooks` (`approve`/`lock`/`unlock` stay blocked) |

Matching is word by word on the split argv, as a prefix: `muster hooks
verify` does not allow `muster hooks approve`, and `muster --version` does
not allow `muster --versionx`. `muster hooks status` and `muster restart` do
not exist in the muster CLI, so they are not listed.

### Caller environment (v1.2)

The agent sets these on every command. They override the agent's own
environment and the request's `env`, so a CLI cannot set them:

| Variable                 | Value |
|--------------------------|-------|
| `MUSTER_VIA_AGENT`       | `1` |
| `MUSTER_CLOUD_CALLER`    | CLI token name from SESSION_INFO (empty if unknown) |
| `MUSTER_CLOUD_CALLER_ID` | CLI token ID from SESSION_INFO (empty if unknown) |
| `MUSTER_DEPLOY_SOURCE`   | `cloud:<token name>` (`cloud` if unknown) |

"Unknown" means the relay sent no SESSION_INFO (a v1.1 relay). The values are
asserted by the relay: the CLI can't spoof them, but a compromised relay
could. Treat them as an audit label, not as authentication.

### Remote cancel (v1.2)

Each command runs in its own process group with its own context.

1. On SIGINT/SIGTERM, `muster-tunnel exec`/`push` sends `COMMAND_CANCEL` with
   the COMMAND's RequestID, keeps printing output for up to 5 s, then exits
   **130**. A second signal exits at once.
2. The agent looks the command up by (SourceID, RequestID), so one CLI can't
   cancel another's command. It sends SIGTERM to the whole process group,
   then SIGKILL after 3 s if anything in the group is left.
3. It replies `COMMAND_RESULT {"exit_code":130,"status":"cancelled"}`.

When a CLI connection drops without cancelling (killed, network loss), the
relay sends `SESSION_CLOSED` and the agent cancels every running command of
that CLI the same way. Commands keep running across *agent*↔relay
reconnects. A SESSION_CLOSED that happens while the agent itself is offline
is lost, and that command runs to completion.

Background processes that a successful command leaves behind (e.g. services
a `bare`/`dev` deploy starts) are not touched. Only a cancel kills the group.

### CLI key pinning (v1.2)

**Threat.** Commands are end-to-end encrypted, but the relay chooses which
key a session uses. Before v1.2 a malicious or compromised relay could act
as a CLI towards an agent at any time: generate a keypair, seal a COMMAND to
the agent's (public) key and route it. The agent had no way to tell a real
CLI from the relay. The CLI already pins agent keys, so the relay can't read
or forge traffic of sessions the user starts. Commands the relay *originates*
were the gap.

**Mitigation.** v1.2 CLIs seal commands with a long-lived identity key and
set `persistent_key: true`. The agent pins keys per CLI token ID (from
SESSION_INFO) in `trusted_clis.json` next to its config (0600).
`cli_key_policy` in the agent config:

| Policy | Behaviour |
|--------|-----------|
| `tofu` (default) | A token's first persistent key is pinned. After that, every command under that token must use that key: other keys, and per-session keys, are rejected (`E_COMMAND_REJECTED`, "CLI key ... changed"). Tokens with no pin yet may still use per-session keys (v1.1 CLIs) |
| `strict` | Only token+key pairs already pinned are accepted (`muster-agent trust add <token-id> <key>`). Commands with no SESSION_INFO (old relay) or plaintext are rejected |
| `off`    | No checks (v1.1 behaviour) |

`muster-tunnel whoami` prints the CLI's token ID and public key.
`muster-agent trust list | add <token-id> <key> | reset <token-id>|--all`
manages pins. The running agent re-reads the file on every command, so a
reset takes effect immediately. Re-installing a CLI (new key) under the same
token needs `trust reset` on each agent, or a new token (`muster-tunnel
login` issues one).

**What remains open.**
- *First contact (tofu).* The relay can still introduce a token the agent
  has never seen (it also issues token IDs) and get its key pinned, or use a
  token before its real owner does. `strict` closes this, at the cost of
  adding each CLI key to each agent by hand.
- *Unpinned tokens.* Under `tofu`, a token that has only ever been used by
  v1.1 CLIs has no pin, so the relay can impersonate it.
- *Relay-asserted identity.* The token *name* and ID in the caller
  environment come from the relay.
- *Cancel/close are not authenticated end to end.* The relay can forge
  COMMAND_CANCEL or SESSION_CLOSED. It can already drop traffic, so this only
  adds denial of service.
- An org-signed CLI certificate (a dashboard-held org key that signs CLI
  keys at login, verified by agents) would close the first-contact gap
  without manual steps. It is not implemented.

---

## Reconnection

```
delay = min(BaseDelay * 2^attempt, MaxDelay) + random(0, MaxJitter)
BaseDelay 1s · MaxDelay 60s · MaxJitter 500ms
```

The attempt counter resets after a connection that lasted more than 60 s.
Every reconnect repeats AUTH_REQUEST (with the node token) → AUTH_RESPONSE →
AGENT_HELLO → RELAY_ACK. A rejected credential (`E_AUTH`) is permanent: the
agent stops instead of retrying, and `muster-agent run` exits 1.

---

## Agent config (`~/.muster-agent/config.json`, or `--config`; mode 0600)

```json
{
  "relay":    { "url": "wss://relay.getmuster.dev", "token": "mst_node_..." },
  "identity": { "org_id": "org_x", "name": "prod-1" },
  "project":  { "dir": "/opt/myapp", "mode": "muster" },
  "muster_path": "/usr/local/bin/muster",
  "allowed_commands": ["muster deploy", "muster status", "muster rollback", "muster logs",
                       "muster history", "muster --version", "muster hooks verify"],
  "allow_shell": false,
  "allow_plaintext": false,
  "cli_key_policy": "tofu"
}
```

`cli_key_policy` is new in v1.2 (`tofu` when absent; `join --cli-key-policy`
sets it). Pinned CLI keys live in `trusted_clis.json` next to the config.

Written atomically (temp file + fsync + rename) by `muster-agent pair` or
`join`. The keypair lives in `keys/` next to the config.

### Agent state (`agent-state.json` next to the config, mode 0644)

```json
{ "connected": true, "name": "prod-1", "org_id": "org_x", "relay": "wss://relay.getmuster.dev",
  "pid": 4242, "connected_since": "2026-11-26T22:03:59Z", "updated_at": "2026-11-26T22:05:29Z" }
```

`muster-agent run` writes this file after each registration, on every
HEARTBEAT_ACK (every 30 s) and on disconnect. It holds no secrets.
`muster-agent status --connected` exits 0 only if `connected` is true and
`updated_at` is under 90 s old, 3 otherwise, and 1 if there is no config.
Other processes can tell whether the agent is online this way without
opening a second connection (that would replace the daemon's).

## CLI credential (`~/.muster/cloud.json`, mode 0600)

```json
{ "relay_url": "wss://relay.getmuster.dev", "token": "mst_cli_...",
  "token_id": "tok_...", "org_id": "org_x", "name": "muster-cli on laptop" }
```

`muster-tunnel login` writes it via the device flow. Precedence for the
token: `--token` > `MUSTER_CLOUD_TOKEN` > this file. For the relay:
`--relay` > `MUSTER_CLOUD_RELAY` > file > `wss://relay.getmuster.dev`.

## Compatibility (v1.2 ↔ v1.1)

Old peers ignore new message types and JSON fields. Unhandled frames are
logged on v1.1 agents ("unhandled message type").

| Combination | Result |
|---|---|
| v1.2 tunnel → v1.1 relay | Works. COMMAND_CANCEL gets `E_BAD_FRAME` from the relay; the tunnel still exits 130, but the command keeps running on the agent |
| v1.2 tunnel → v1.2 relay → v1.1 agent | Works. The agent ignores COMMAND_CANCEL/SESSION_*: no remote cancel, no caller env, no pinning. Check `features` in `agents` before relying on cancel |
| v1.1 tunnel → v1.2 relay → v1.2 agent | Works. Ctrl+C just closes the connection → SESSION_CLOSED → the agent stops the command. Per-session keys are accepted while the token has no pin |
| v1.2 agent ← v1.1 relay | Works. No SESSION_INFO, so caller env says `cloud` with no caller, `tofu` can't pin (no token ID), and `strict` rejects everything |
| Same token from a v1.2 tunnel, then a v1.1 tunnel (`tofu`) | **Break:** once pinned, the per-session key of the v1.1 tunnel is rejected. Upgrade the tunnel or run `muster-agent trust reset <token-id>` |

Default allowlist changes apply only to configs written by `join` after the
upgrade.

## Token prefixes

| Prefix        | Used by                    |
|---------------|----------------------------|
| `mst_cli_`    | CLI / tunnel               |
| `mst_agent_`  | one-time agent join        |
| `mst_node_`   | agent (after enrollment)   |
| `mst_admin_`  | relay admin API only       |
