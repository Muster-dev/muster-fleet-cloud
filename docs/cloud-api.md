# Fleet Cloud API contract (v1)

> Source of truth shared by the relay (`muster-cloud`), the clients
> (`muster-fleet-cloud`: `muster-agent`, `muster-tunnel`) and the dashboard.
> This file is kept identical in `muster-cloud/docs/cloud-api.md` and
> `muster-fleet-cloud/docs/cloud-api.md`. The section
> **Implementation notes** at the end lists every detail the original
> contract left open and the (small) additions made while implementing it.

Hosts (production): relay https://relay.getmuster.dev (WebSocket: wss://relay.getmuster.dev/v1/tunnel), dashboard https://cloud.getmuster.dev.
Org model: one org per Clerk user for now, org_id = "org_<clerkUserId>" (see dashboard/src/lib/org.ts).

## Token types (prefix → purpose)
- mst_admin_  relay administration; used ONLY server-side by the dashboard. Never given to users.
- mst_cli_    a user's CLI/tunnel credential, scoped to one org. Issued by device login or dashboard.
- mst_agent_  one-time agent join token, scoped to org (+ optional machine name), expires (default 24h).
- mst_node_   long-lived per-agent credential issued by the relay when an agent first joins with mst_agent_. Revocable.
All tokens: 32 bytes crypto/rand, stored only as sha256 hash, compared in constant time. Raw value shown once.

## Device login (RFC 8628 style) — public endpoints are rate limited per IP
POST /api/v1/device/code        body {"client_name":"muster-cli on ricky-mbp"}
  200 {"device_code":"<opaque 32B>","user_code":"ABCD-EFGH","verification_uri":"https://cloud.getmuster.dev/device",
       "verification_uri_complete":"https://cloud.getmuster.dev/device?code=ABCD-EFGH","expires_in":600,"interval":5}
  user_code: 8 chars from an unambiguous alphabet (no 0/O/1/I), formatted XXXX-XXXX.
POST /api/v1/device/token       body {"device_code":"..."}
  400 {"error":"authorization_pending"} | 400 {"error":"slow_down"} | 400 {"error":"expired_token"} | 400 {"error":"access_denied"}
  200 {"token":"mst_cli_...","token_id":"...","org_id":"org_x","name":"muster-cli on ricky-mbp","relay_url":"wss://relay.getmuster.dev"}
  A device_code can be redeemed exactly once.
GET  /api/v1/device/{user_code}            (admin)  200 {"user_code","client_name","status":"pending|approved|denied|expired","created_at","expires_at"} | 404
POST /api/v1/device/{user_code}/approve    (admin)  body {"org_id":"org_x","user_id":"user_x"}  → 200 {"status":"approved"}
POST /api/v1/device/{user_code}/deny       (admin)  → 200 {"status":"denied"}

## Tokens (admin)
POST   /api/v1/tokens        body {"type":"cli|agent-join","org_id":"org_x","name":"prod-1","expires_in":86400?}  → 201 {"token":"mst_...","id":"...","type","org_id","name","created_at","expires_at"}
GET    /api/v1/tokens?org_id=org_x   → 200 [{"id","type","org_id","name","created_at","expires_at","last_used_at","used"}]  (never hashes or raw tokens)
DELETE /api/v1/tokens/{id}?org_id=org_x → 204  (must belong to org)

## Agents / nodes (admin)
GET    /api/v1/agents?org_id=org_x   → 200 [{"name","org_id","connected":bool,"last_seen","version","hostname","os","arch","joined_at"}]
DELETE /api/v1/agents/{name}?org_id=org_x → 204  (revokes the mst_node_ credential and drops the connection)

## Agent enrollment
1. `muster-agent join --relay wss://relay.getmuster.dev --token mst_agent_... [--name prod-1] [--project /opt/app]`
2. Agent connects, auths with the join token. Relay validates (unused, unexpired, org match), creates node record + mst_node_ token, returns it in the auth_ok payload ("node_token"), marks join token used.
3. Agent persists node token (0600 config file) and uses it for every later connection/reconnect. Join token is never needed again.

## Health
GET /healthz → 200 {"status":"ok","version":"x.y.z","agents":N}   (no auth, no org data)

---

## Implementation notes (v1 as built)

Nothing above changed shape. These are the details the contract left open,
plus a few strictly additive extensions.

### General
- Admin auth is **only** `Authorization: Bearer mst_admin_...`. Missing or
  invalid → `401 {"error":"admin token required"}`. Too many failed admin or
  WebSocket auths from one IP → `429` until the bucket refills (default 10/min).
- Admin tokens are **global** (not org-scoped). They can only be created with
  the relay CLI (`muster-cloud token create --type admin`), never through the API.
- All error bodies are `{"error":"<message>"}`. Request bodies are JSON, max
  16 KiB, and unknown fields are rejected (`400`).
- `org_id` must match `^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`. A missing or invalid
  `org_id` query parameter → `400`.
- Timestamps are RFC 3339 UTC strings. Optional ones (`expires_at`,
  `last_used_at`, `last_seen`) are `null` when unset.

### Device login
- Rate limit (per client IP, default 30/min across both public endpoints):
  `429 {"error":"rate limited"}` with `Retry-After`.
- `POST /device/code`: `client_name` is optional (defaults to `"muster-cli"`),
  1–100 printable chars. `verification_uri` is `<--dashboard-url>/device`.
- `POST /device/token`: an unknown, already-redeemed or purged `device_code`
  returns `400 {"error":"expired_token"}`. `slow_down` is returned when a
  client polls sooner than ~80% of `interval` after its previous poll; the
  session's interval then grows by 5 s (RFC 8628 §3.5). A denied session
  returns `access_denied` once and is then forgotten.
- `relay_url` is the relay's `--public-url`. If that is unset (local
  development), it is derived from the request (`ws://<Host>`).
- The issued CLI token is named after `client_name` and records the
  approving `user_id` (`created_by`, internal).
- Admin `GET /device/{user_code}`: the code is case-insensitive and the dash
  is optional. After a successful redemption the session is gone (`404`).
- Approve/deny on a session that is not `pending` (already approved, denied
  or expired) → `409 {"error":"code is no longer pending"}`. Unknown code →
  `404`. Approve requires a valid `org_id`. `user_id` is optional, ≤128 chars.
- Device sessions are kept in memory (10 min TTL, max 10 000 pending). A relay
  restart cancels pending logins; the CLI just runs `login` again.

### Tokens
- `POST /tokens`: `type` is `cli` or `agent-join` (anything else → `400`). `name`
  defaults to the type and is 1–100 printable chars. `expires_in` is 1..31536000
  seconds. Defaults: `agent-join` 24 h, `cli` never expires (`expires_at: null`).
- **Addition:** optional `"machine_name":"prod-1"` (agent-join only) binds the
  join token to one agent name. An agent joining with a different `--name` is
  rejected.
- The `201` response also includes `last_used_at: null` and `used: false`, so
  it has the same shape as a list entry plus `token`.
- `GET /tokens` lists only `cli` and `agent-join` tokens of the org, oldest
  first. Node credentials are managed through `/agents` and admin tokens are
  never listed. `used` is true for a consumed join token.
- `DELETE /tokens/{id}`: a token of another org, a node credential, an admin
  token or an unknown ID all return `404`. Deleting a CLI token also **closes
  live tunnel sessions** authenticated with it.

### Agents
- `GET /agents` lists every **enrolled** agent of the org (persisted), sorted
  by name, with `connected` showing whether it is online right now. `last_seen`
  is the last frame time while connected, else the time it last disconnected.
  `version/hostname/os/arch` come from the agent's AGENT_HELLO (empty strings
  until it first connects).
- Agent names are 1–32 chars of `[A-Za-z0-9._-]`, start with an alphanumeric,
  and are unique per org. Joining with a name already enrolled in the org is
  rejected; revoke the old agent first.
- `DELETE /agents/{name}` deletes the node credential first, then closes the
  connection, so the agent cannot reconnect. `muster-agent run` then exits
  non-zero with a "credential may have been revoked" message.

### Health
- `agents` is the total number of connected agents across all orgs (no
  per-org data).

### WebSocket protocol changes (`/v1/tunnel`, see `docs/protocol.md` in muster-fleet-cloud)
- The relay derives the org **from the token**. `org_id` in AUTH_REQUEST is
  optional; if sent it must match (else auth fails).
- `AUTH_RESPONSE` ok payload: `{"ok":true,"org_id","name","session_id","node_token"?}`.
  Failure: `{"ok":false,"code":"E_AUTH","message":"..."}`.
- Header identities (SourceID/DestID, 32 bytes) are now **bare names**.
  `org_<clerkUserId>/name` doesn't fit in 32 bytes. The relay routes only
  within the sender's authenticated org and stamps SourceID with the sender's
  authenticated name. A legacy `org/name` DestID is accepted, but the org part
  is ignored.
- CLI connection names are assigned by the relay (`cli-xxxxxxxx`), so parallel
  `muster-tunnel` processes never kick each other off.
- The token is sent only in AUTH_REQUEST, not in an HTTP header, so it can't
  end up in proxy logs.
