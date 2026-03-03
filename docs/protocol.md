# Muster Transport Protocol (MTP) v1

Specification for building relay servers compatible with the Muster fleet cloud system.

The relay is a WebSocket server that routes binary frames between **tunnel clients** (CLI) and **agents** (remote machines). The relay authenticates connections, tracks online agents, and forwards frames by destination identity. It never reads encrypted payloads.

---

## Wire Format

Every message is a single WebSocket **binary frame**. Each frame has a fixed 89-byte header followed by a variable-length payload.

```
Offset  Size  Type              Field
------  ----  ----------------  ------------------------------------------
0       1     uint8             Version         (must be 0x01)
1       1     uint8             MsgType         (see Message Types)
2       4     uint32 BE         PayloadLength   (byte count of Payload)
6       16    [16]byte          RequestID       (correlation ID)
22      32    [32]byte          SourceID        (sender identity)
54      32    [32]byte          DestID          (recipient identity)
86      1     uint8             Flags           (bitmask)
87      1     uint8             Reserved        (0x00)
88      1     uint8             Reserved        (0x00)
89      N     []byte            Payload         (JSON or encrypted blob)
```

Total frame size: `89 + PayloadLength`.

### Identity Encoding

SourceID and DestID are UTF-8 strings in the format `"<org_id>/<name>"`, copied into 32 bytes and zero-padded on the right. Parse by reading until the first `0x00` byte. If all 32 bytes are non-zero, use the full 32 bytes.

The special identity `"relay"` (zero-padded to 32 bytes) addresses the relay server itself.

Examples: `"myorg/prod-east-01"`, `"myorg/cli-laptop"`, `"relay"`.

### Flags

| Bit   | Name                | Meaning                                      |
|-------|---------------------|----------------------------------------------|
| 0x01  | `FlagEncrypted`     | Payload is NaCl box encrypted (relay cannot read it) |
| 0x02  | `FlagCompressed`    | Reserved (not implemented)                   |
| 0x04  | `FlagStreamContinued` | More STREAM_DATA frames follow             |
| 0x08  | `FlagStreamEnd`     | Final stream frame                           |

Flags may be ORed together. When `FlagEncrypted` is set, the relay must forward the payload as-is without attempting to parse it.

---

## Message Types

| Hex  | Name                | Direction            | Payload        |
|------|---------------------|----------------------|----------------|
| 0x01 | AUTH_REQUEST        | Client -> Relay      | JSON           |
| 0x02 | AUTH_RESPONSE       | Relay -> Client      | JSON           |
| 0x03 | AGENT_HELLO         | Agent -> Relay       | JSON           |
| 0x04 | RELAY_ACK           | Relay -> Agent       | JSON (optional)|
| 0x05 | HEARTBEAT           | Bidirectional        | Empty          |
| 0x06 | HEARTBEAT_ACK       | Bidirectional        | Empty          |
| 0x10 | COMMAND             | CLI -> Agent (routed)| JSON or encrypted |
| 0x11 | COMMAND_ACK         | Agent -> CLI (routed)| Empty          |
| 0x12 | COMMAND_RESULT      | Agent -> CLI (routed)| JSON or encrypted |
| 0x13 | COMMAND_ERROR       | Agent -> CLI (routed)| JSON           |
| 0x20 | STREAM_DATA         | Agent -> CLI (routed)| JSON or encrypted |
| 0x21 | STREAM_END          | Agent -> CLI (routed)| Empty          |
| 0x30 | KEY_EXCHANGE        | CLI -> Agent (routed)| JSON           |
| 0x31 | KEY_EXCHANGE_ACK    | Agent -> CLI (routed)| JSON           |
| 0xF0 | ERROR               | Relay -> Client      | JSON           |
| 0xF1 | AGENT_LIST          | Relay -> CLI         | JSON           |
| 0xF2 | AGENT_LIST_REQUEST  | CLI -> Relay         | Empty          |

MsgType `0x00` is invalid. Frames with MsgType 0 must be rejected.

---

## Error Codes

Carried in the `"code"` field of ERROR (0xF0) and COMMAND_ERROR (0x13) payloads.

| Code                  | Meaning                                |
|-----------------------|----------------------------------------|
| `E_AUTH`              | Authentication failed                  |
| `E_NOT_CONNECTED`     | Target agent is not connected          |
| `E_COMMAND_REJECTED`  | Command not in agent's allowlist       |
| `E_TIMEOUT`           | Operation timed out                    |
| `E_DECRYPT`           | Payload decryption failed              |
| `E_AGENT_BUSY`        | Agent is already executing a command   |

---

## WebSocket Endpoint

```
Path:    /v1/tunnel
Scheme:  wss:// (TLS required in production)
Frames:  Binary only
```

The relay listens on `:8443` by default. All communication uses binary WebSocket frames.

---

## Connection Flows

### 1. Authentication

Every connection (agent or CLI) must authenticate before sending other messages. The relay validates the token, registers the identity, and responds.

```
Client                              Relay
  |                                   |
  |-- WebSocket connect (wss://relay:8443/v1/tunnel) -->
  |   Header: Authorization: Bearer <token>
  |                                   |
  |-- AUTH_REQUEST (0x01) ----------->|
  |   SourceID: "<org>/<name>"        |
  |   DestID:   "relay"               |
  |   Payload:                        |
  |     {                             |
  |       "token":       "<token>",   |
  |       "client_type": "agent" | "cli",
  |       "org_id":      "<org>",     |
  |       "name":        "<name>"     |
  |     }                             |
  |                                   |
  |<-- AUTH_RESPONSE (0x02) ---------|
  |   Payload:                        |
  |     { "ok": true }                |
  |   -- or --                        |
  |     { "ok": false, "error": "..." }
```

On `"ok": false`, close the connection.

**Relay requirements:**
- Validate the token against your auth backend
- Register the connection identity (SourceID) for routing
- Track `client_type` to distinguish agents from CLI clients
- Reject duplicate identities or handle reconnection (replace old connection)

### 2. Agent Registration

After authentication, agents send AGENT_HELLO to announce capabilities.

```
Agent                               Relay
  |                                   |
  |-- AGENT_HELLO (0x03) ----------->|
  |   SourceID: "<org>/<name>"        |
  |   DestID:   "relay"               |
  |   Payload:                        |
  |     {                             |
  |       "agent_name": "<name>",     |
  |       "org_id":     "<org>",      |
  |       "version":    "0.1.0",      |
  |       "public_key": "<base64>"    |
  |     }                             |
  |                                   |
  |<-- RELAY_ACK (0x04) -------------|
  |   Payload: {} or empty            |
```

**Relay requirements:**
- Store the agent's `public_key` (X25519, base64-encoded) for key exchange responses
- Store agent metadata (version, org_id) for the agent list endpoint
- Mark the agent as "online" in your routing table
- Send RELAY_ACK to confirm registration

### 3. Frame Routing

For messages where DestID is not `"relay"`, the relay looks up the DestID in its connection table and forwards the frame unchanged. The relay does not modify the header or payload.

**Routed message types:** COMMAND, COMMAND_ACK, COMMAND_RESULT, COMMAND_ERROR, STREAM_DATA, STREAM_END, KEY_EXCHANGE, KEY_EXCHANGE_ACK, HEARTBEAT (when DestID is not "relay"), HEARTBEAT_ACK.

If the destination is not connected, respond with:

```
ERROR (0xF0)
  SourceID: "relay"
  DestID:   <original SourceID>
  RequestID: <original RequestID>
  Payload: { "code": "E_NOT_CONNECTED", "message": "agent not connected" }
```

### 4. Heartbeats

The relay must send periodic HEARTBEAT frames to connected agents and expect HEARTBEAT_ACK responses to detect stale connections.

```
Relay                               Agent
  |                                   |
  |-- HEARTBEAT (0x05) ------------->|
  |   SourceID: "relay"               |
  |   DestID:   "<org>/<agent>"       |
  |   Payload:  empty                 |
  |                                   |
  |<-- HEARTBEAT_ACK (0x06) ---------|
  |   SourceID: "<org>/<agent>"       |
  |   DestID:   "relay"              |
  |   RequestID: <echoed>             |
  |   Payload:  empty                 |
```

**Defaults:**
- Send heartbeats every **30 seconds**
- Mark agents disconnected after **90 seconds** of silence
- Remove disconnected agents from the routing table

Clients may also send HEARTBEAT to agents (routed through the relay) as a ping mechanism.

### 5. Agent List

CLI clients can request a list of connected agents.

```
CLI                                 Relay
  |                                   |
  |-- AGENT_LIST_REQUEST (0xF2) ---->|
  |   DestID: "relay"                 |
  |   Payload: empty                  |
  |                                   |
  |<-- AGENT_LIST (0xF1) -----------|
  |   Payload: JSON array of agents   |
```

**Recommended AGENT_LIST payload:**

```json
[
  {
    "name": "prod-east-01",
    "org_id": "myorg",
    "version": "0.1.0",
    "connected_at": "2024-01-15T10:30:00Z",
    "last_heartbeat": "2024-01-15T12:45:30Z",
    "public_key": "<base64>"
  }
]
```

The CLI prints this payload as-is, so the structure is flexible as long as it's valid JSON.

---

## Payload Schemas

### COMMAND (0x10)

```json
{
  "action":   "exec" | "push_hook" | "deploy" | "status" | "rollback" | "logs",
  "command":  "string",
  "services": ["string"],
  "env":      { "KEY": "value" },
  "cwd":      "/path/to/workdir",
  "stream":   true
}
```

- `exec` — run `command` as `bash -c "<command>"`
- `push_hook` — pipe `command` (full script content) into `bash -s` via stdin
- `deploy`, `status`, `rollback`, `logs` — run muster commands on the agent

The relay does not parse this payload. It forwards it as-is. When `FlagEncrypted` is set, the payload is an opaque encrypted blob.

### STREAM_DATA (0x20)

```json
{
  "line":   "output text",
  "stream": "stdout"
}
```

Sent with `FlagStreamContinued` (0x04) on every frame. The relay forwards these without parsing.

### COMMAND_RESULT (0x12)

```json
{
  "exit_code": 0,
  "status":    "done"
}
```

### COMMAND_ERROR (0x13)

```json
{
  "code":    "E_COMMAND_REJECTED",
  "message": "command not allowed"
}
```

### KEY_EXCHANGE (0x30) / KEY_EXCHANGE_ACK (0x31)

```json
{
  "public_key": "<base64 X25519 public key>"
}
```

---

## End-to-End Encryption

Encryption is between CLI and agent. The relay routes encrypted frames without decrypting them.

### Algorithm

- Key agreement: **X25519** (Curve25519 Diffie-Hellman)
- Authenticated encryption: **XSalsa20-Poly1305** (NaCl box)

### Encrypted Payload Format

When `FlagEncrypted` (0x01) is set, the Payload field contains:

```
[0:24]    24-byte random nonce
[24:N]    NaCl box ciphertext (plaintext + 16-byte Poly1305 MAC)
```

Overhead: 40 bytes (24 nonce + 16 MAC) above plaintext.

### Key Exchange Flow

1. CLI sends KEY_EXCHANGE (0x30) with its public key
2. Agent responds with KEY_EXCHANGE_ACK (0x31) with its public key
3. Both derive the shared secret via X25519
4. Subsequent COMMAND frames use `FlagEncrypted` with NaCl box

The agent also sends its public key in AGENT_HELLO, so the relay can cache it for distribution without requiring a live key exchange round-trip.

### Key Format

- On disk: 32 raw bytes
- In protocol payloads: standard base64 (non-URL-safe, with `=` padding)
- X25519 clamping applied to private keys: `priv[0] &= 248`, `priv[31] &= 127`, `priv[31] |= 64`

---

## Relay Server Requirements

A compatible relay server must implement:

1. **WebSocket server** on `/v1/tunnel` accepting binary frames
2. **Authentication** — validate AUTH_REQUEST tokens, respond with AUTH_RESPONSE
3. **Agent registry** — track connected agents from AGENT_HELLO, store metadata and public keys
4. **Frame routing** — forward frames by DestID lookup, return ERROR if destination not connected
5. **Heartbeat** — send periodic HEARTBEAT to agents, disconnect silent ones after timeout
6. **Agent list** — respond to AGENT_LIST_REQUEST with connected agent metadata
7. **Health endpoint** — HTTP GET `/healthz` returning 200 OK (used by `muster setup` to verify relay reachability)

### What the relay does NOT do

- Decrypt or inspect encrypted payloads (E2E between CLI and agent)
- Parse or validate COMMAND payloads (opaque, forwarded as-is)
- Execute commands (that's the agent's job)
- Manage agent configuration (agents self-configure)

### TLS

Production relays should terminate TLS. The WebSocket scheme is `wss://`. Reverse proxy configs (nginx, Caddy) must set:

```
proxy_read_timeout  86400s;   # 24h — long-lived agent connections
proxy_send_timeout  86400s;
```

And upgrade headers for WebSocket:

```
Upgrade: websocket
Connection: upgrade
```

---

## Reconnection

Agents reconnect with exponential backoff:

```
delay = min(BaseDelay * 2^attempt, MaxDelay) + random(0, MaxJitter)
```

| Parameter   | Default  |
|-------------|----------|
| BaseDelay   | 1s       |
| MaxDelay    | 60s      |
| MaxJitter   | 500ms    |

If a connection was stable for >60 seconds before disconnecting, the attempt counter resets to 0. On reconnect, the full auth flow repeats (AUTH_REQUEST, AUTH_RESPONSE, AGENT_HELLO, RELAY_ACK).

The relay should handle agent reconnection gracefully — replace the old connection entry when the same identity re-authenticates.

---

## Configuration Reference

### Relay Server Config

```json
{
  "listen":              ":8443",
  "tls": {
    "cert": "/path/to/cert.pem",
    "key":  "/path/to/key.pem"
  },
  "database": {
    "path": "muster-cloud.db"
  },
  "heartbeat_interval":  "30s",
  "heartbeat_timeout":   "90s",
  "log_level":           "info",
  "log_format":          "json"
}
```

### Agent Config (`~/.muster-agent/config.json`)

```json
{
  "relay": {
    "url":   "wss://relay.example.com:8443",
    "token": "mst_agent_..."
  },
  "identity": {
    "org_id": "myorg",
    "name":   "prod-server-01"
  },
  "project": {
    "dir":  "/opt/myapp",
    "mode": "muster"
  },
  "muster_path":       "/usr/local/bin/muster",
  "allowed_commands":  ["muster deploy", "muster status", "muster rollback", "muster logs"],
  "log_file":          "/var/lib/muster/agent.log",
  "reconnect_base_delay": "1s",
  "reconnect_max_delay":  "60s"
}
```

### Token Conventions

| Prefix          | Usage       |
|-----------------|-------------|
| `mst_agent_...` | Agent token |
| `mst_cli_...`   | CLI token   |

Token validation and issuance are relay-specific. The protocol only requires that AUTH_REQUEST carries the token and AUTH_RESPONSE indicates success or failure.
