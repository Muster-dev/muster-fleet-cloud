# Muster Transport Protocol (MTP) v1.1

Specification for relay servers and clients in the Muster Fleet Cloud system.
The HTTP side of the relay (device login, tokens, agents) is specified in
[cloud-api.md](cloud-api.md).

The relay is a WebSocket server that routes binary frames between **CLI
clients** (`muster-tunnel`) and **agents** (`muster-agent` on remote
machines). It authenticates connections, tracks online agents per org, and
forwards frames within an org. Command payloads are end-to-end encrypted
between CLI and agent, so the relay cannot read them.

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
| 0x20 | STREAM_DATA        | Agent → CLI (routed)  | encrypted                |
| 0x21 | STREAM_END         | Agent → CLI (routed)  | empty                    |
| 0x30 | KEY_EXCHANGE       | CLI → Agent (routed)  | JSON                     |
| 0x31 | KEY_EXCHANGE_ACK   | Agent → CLI (routed)  | JSON                     |
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

### 2. Agent Registration

After AUTH_RESPONSE an agent sends AGENT_HELLO (also within 5 seconds):

```json
{ "agent_name": "prod-1", "version": "0.2.0",
  "public_key": "<base64 X25519, 32 bytes>",
  "hostname": "web-1", "os": "linux", "arch": "amd64" }
```

The relay validates `public_key`, records the metadata on the node, marks
the agent online and replies RELAY_ACK. A second connection with the same
identity replaces the first.

### 3. Routing

The relay routes a frame to `DestID` **within the sender's org only**:

- CLI → agent: `COMMAND`, `KEY_EXCHANGE`, `HEARTBEAT`
- agent → CLI: `COMMAND_ACK`, `COMMAND_RESULT`, `COMMAND_ERROR`,
  `STREAM_DATA`, `STREAM_END`, `KEY_EXCHANGE_ACK`, `HEARTBEAT_ACK`

Any other type addressed to a peer gets `ERROR E_BAD_FRAME`. An unknown or
offline destination (including any agent in another org) gets
`ERROR E_NOT_CONNECTED`, with the original RequestID, to a CLI sender.

Before forwarding, the relay overwrites **SourceID** with the sender's
authenticated name, so peers can't spoof it. The payload is forwarded
byte-for-byte.

### 4. Heartbeats and idle timeout

Clients send `HEARTBEAT` (DestID `"relay"`) every 30 s. The relay answers
`HEARTBEAT_ACK`. The relay closes any connection that sends nothing for 90 s.
Agents reconnect if nothing arrives for 95 s. A CLI may also ping an agent:
HEARTBEAT with DestID set to the agent, which the agent answers.

### 5. Agent list

`AGENT_LIST_REQUEST` (CLI only) → `AGENT_LIST` with a JSON array of the
org's connected agents:

```json
[{"name":"prod-1","org_id":"org_x","version":"0.2.0","hostname":"web-1","os":"linux",
  "arch":"amd64","connected_at":"…","last_seen":"…","public_key":"<base64>"}]
```

---

## End-to-End Encryption

- Key agreement **X25519**, authenticated encryption **NaCl box**
  (XSalsa20-Poly1305).
- Agents have a long-lived keypair (`keys/identity.key`, 0600). Each CLI
  session generates a fresh ephemeral keypair.

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
  "cwd": "/abs/path", "stream": true, "ts": 1764180000 }
// STREAM_DATA
{ "line": "output text", "stream": "stdout" }
// COMMAND_RESULT
{ "exit_code": 0, "status": "done" }
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
  (`LD_*`, `DYLD_*`, `PATH`, `BASH_ENV`, `IFS`, …) are refused.
- `cwd` must be absolute. At most 4 commands run concurrently (`E_AGENT_BUSY`).

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
  "allowed_commands": ["muster deploy", "muster status", "muster rollback", "muster logs"],
  "allow_shell": false,
  "allow_plaintext": false
}
```

Written atomically (temp file + fsync + rename) by `muster-agent join`. The
keypair lives in `keys/` next to the config.

## CLI credential (`~/.muster/cloud.json`, mode 0600)

```json
{ "relay_url": "wss://relay.getmuster.dev", "token": "mst_cli_...",
  "token_id": "tok_...", "org_id": "org_x", "name": "muster-cli on laptop" }
```

`muster-tunnel login` writes it via the device flow. Precedence for the
token: `--token` > `MUSTER_CLOUD_TOKEN` > this file. For the relay:
`--relay` > `MUSTER_CLOUD_RELAY` > file > `wss://relay.getmuster.dev`.

## Token prefixes

| Prefix        | Used by                    |
|---------------|----------------------------|
| `mst_cli_`    | CLI / tunnel               |
| `mst_agent_`  | one-time agent join        |
| `mst_node_`   | agent (after enrollment)   |
| `mst_admin_`  | relay admin API only       |
