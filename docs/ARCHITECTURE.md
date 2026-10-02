| `POST /instance/started` | header `X-TunnelTab-Instance` | 204 (the previous version confirms the start after "Update now") |
# Architecture

How TunnelTab is put together, and where to make changes.

> **Living document.** Sections marked *(planned)* describe the design from
> [PLAN.md](../PLAN.md) and are replaced with the real details as each phase
> is built. Current state: **v0.1.0 released**; the sections below describe it.

## Overview

TunnelTab is one Go executable. When started it:

1. finds its data folder (`data/` next to the executable, or `--data`);
2. starts a web server on `127.0.0.1` and opens the dashboard in the default
   browser, using a one-time token that is exchanged for a session cookie;
3. waits for the user to unlock the vault with the master password;
4. connects to servers over SSH on demand, providing port forwards (for web
   UIs) and PTY sessions (for the in-browser terminal).

```
┌──────────────── Browser tab ────────────────┐
│  web/static: dashboard (vanilla JS/CSS)     │
│  xterm.js terminal                          │
└──────┬───────────────────┬──────────────────┘
       │ REST + events      │ terminal WebSocket
       │ (cookie, Host/Origin checked, 127.0.0.1 only)
┌──────▼───────────────────▼──────────────────┐
│ tunneltab executable                        │
│                                             │
│  internal/server ── API, auth, events, WS   │
│        │                                    │
│  internal/vault ─── encrypted data + secrets│
│  internal/model ─── types + validation      │
│  internal/sshx ──── SSH connections,        │
│        │            forwards, PTYs          │
│  internal/config ── paths, settings, logs   │
│  internal/platform  browser, terminal, OS   │
└────────┼────────────────────────────────────┘
         │ SSH (golang.org/x/crypto/ssh)
         ▼
     Your VPSs  ◀── forwards: 127.0.0.1:<local> → <remoteHost>:<remotePort>
```

## Packages

| Path | Responsibility | Status |
|---|---|---|
| `cmd/tunneltab` | Entry point: flags, startup, single instance, browser, shutdown | Done |
| `internal/config` | Portable paths, `settings.json`, rotated logs | Done |
| `internal/vault` | Master-password KDF, encrypted file format, atomic save + backup, auto-lock (`View`/`Update` count as activity; `Peek`, for background work such as health checks and reconnects, does not) | Done |
| `internal/model` | Project / Server / Service types, IDs, validation, CRUD operations, secret-free public view | Done |
| `internal/atomicfile` | Crash-safe file writes (temp file → fsync → rename) | Done |
| `internal/sshx` | Connection pool, auth, host-key checks, forwards, terminals (PTY), keep-alive (and ping from it), reconnect, per-tunnel traffic counters (`traffic.go`) | Done |
| `internal/sshx/sshtest` | In-process SSH server used by tests | Done |
| `internal/server` | HTTP server, launch links + sessions, Host/Origin checks, API, events, terminal WebSocket | Done |
| `internal/health` | Opt-in server health: the one fixed command TunnelTab runs on a server by itself (`Command`) and its strict parser (`Parse`, fuzzed) | Done |
| `internal/discover` | "Find services": the fixed, read-only command TunnelTab runs on a server when the user clicks Find services (`Command`: `ss`/`netstat` + `docker ps`), its strict parser (`Parse`, fuzzed) and the table of well-known apps (`apps.go`) | Done |
| `internal/webcheck` | Service checks: does a port answer like a web page? One fixed `HEAD` request, https first (certificate not verified) then http, over connections the caller dials through SSH (`Manager.Through`); path validated (fuzzed) | Done |
| `internal/update` | Updates, only when the user clicks: "Check for updates" (GitHub releases/latest API, version comparison) and "Update now" (download, signature + checksum verification, unpack, install with `.old` backups, rollback, cleanup). Release signature format in `signature.go` | Done |
| `internal/platform` | Open browser, error dialog, instance file and data folder lock | Done |
| `web` | Embeds `web/static/` into the binary (`web.Files`) | Done |
| `web/static` | The dashboard (vanilla JS modules + CSS) | Done |
| `internal/devtools/fakessh` | Local SSH server + demo web app for trying the dashboard (not shipped) | Done |
| `tests/e2e` | Browser walkthrough (Playwright) against the real program | Done |
| `scripts/mkzip` | Build helper: zips the portable folder | Done |
| `scripts/signsums` | Release tool: creates the signing key, signs/verifies `SHA256SUMS.txt` | Done |

Each package has a `doc.go` describing its job in more detail.

## Data folder

```
data/
├── vault.enc        all projects, servers, services and secrets (encrypted)
├── vault.enc.bak    previous version, kept by every save
├── settings.json    non-secret preferences
├── instance.json    port + secret of the running copy (deleted on exit)
├── instance.lock    locked while a copy runs (one copy per data folder)
└── logs/            rotated logs; never contain secrets
```

## Data model (`internal/model`)

`model.Data` is the whole vault payload: `{version, projects[], servers[], services[], knownHosts[]}`.

| Type | Fields | Notes |
|---|---|---|
| Project | `id, name, description, order` | Deleting a project deletes its servers and their services |
| Server | `id, projectId, name, host, port, username, auth, order` | `host`: DNS name or IP; `username`: letters, digits, `. _ @ -` |
| Auth | `type` + fields for that type | See below; unused fields are cleared on save |
| Service | `id, serverId, label, remoteHost, remotePort, localPort, protocol, path, autoStart, order` | `remoteHost` defaults to `127.0.0.1`; `localPort` 0 = auto; fixed local ports must be unique |
| KnownHost | `host, key, addedAt` | `host` is `name` for port 22, `[name]:port` otherwise; `key` in authorized_keys format; `SetHostKey` replaces a host's keys |

| `auth.type` | Uses | Secret fields |
|---|---|---|
| `agent` | running ssh-agent | none |
| `keyFile` | `keyPath` (relative = inside portable folder) | `passphrase` (optional) |
| `keyVault` | key stored in the vault | `privateKey`, `passphrase` (optional) |
| `password` | password | `password` |

**Rules the model enforces**

- IDs are UUIDv4 generated by `model.NewID()`; IDs sent by the browser are only
  used to look items up.
- Every operation (`AddProject`, `UpdateServer`, `DeleteService`, …) validates
  before changing anything, so a failed call leaves the data untouched.
- `UpdateServer` keeps stored secrets when the login method is unchanged and
  the field is left blank (the dashboard never has to send secrets back);
  changing the method discards the old secrets. `ClearPassphrase` removes an
  optional passphrase.
- `Data.Validate()` checks everything at once (IDs, references, limits,
  unique local ports); the vault runs it before every save.
- Secret fields are tagged `secret:"true"`. `Data.Public()` returns
  `PublicData`, where each secret is replaced by a `has…` flag. A test fills
  every tagged field with a marker and fails if any reaches `Public()`, and
  fails if `Auth` gains an unclassified field.

## Vault (`internal/vault`)

**API:** `Create` (new vault, returned unlocked) · `Open` (existing, locked) ·
`Unlock` · `Lock` · `View(fn)` (read a copy) · `Update(fn)` (change a copy;
validated, saved, then swapped in) · `ChangePassword` · `Touch` /
`SetAutoLock` / `LockIfIdle` / `RunAutoLock` · `OnLock(fn)` callbacks.

**File format** (`data/vault.enc`, JSON):

```json
{
  "format": "tunneltab-vault",
  "version": 1,
  "kdf": { "name": "argon2id", "time": 4, "memoryKiB": 262144, "threads": 4, "salt": "<16 bytes, base64>" },
  "cipher": "aes-256-gcm",
  "nonce": "<12 bytes, base64>",
  "ciphertext": "<base64>"
}
```

- key = Argon2id(master password, salt, params) → 32 bytes; never stored.
- plaintext = JSON of `model.Data`.
- The header (everything except `nonce` and `ciphertext`) is GCM additional
  data: editing any parameter or the salt makes decryption fail.
- On read, KDF parameters must be within bounds (time 1–20, memory
  8 MiB–1 GiB, threads 1–16) so a tampered file can't exhaust memory.
- Every save writes a fresh random nonce, copies the current file to
  `vault.enc.bak`, then atomically replaces `vault.enc`.
- `ChangePassword` generates a new salt, re-encrypts, and then replaces
  `vault.enc.bak` too, so the old password no longer opens any file.

**Changing the format:** bump `formatVersion` (header) or
`model.CurrentVersion` (payload), keep reading the old version, and add a
migration in `vault.decodeData`.

## SSH engine (`internal/sshx`)

```
StartForward(service) ──▶ listen 127.0.0.1:<port> ──▶ acquire(server) ──▶ dial (first user only)
                                                          │                  │
                                    shared *serverConn ◀──┘      Targets(serverID) → vault
                                          │                     authMethods → ssh.ClientConfig
                                   supervise(): keep-alive,      hostKeyCallback(confirmed keys)
                                   reconnect with back-off
browser ──▶ 127.0.0.1:<port> ──▶ forward.handle ──▶ client.Dial(remoteHost:remotePort) (direct-tcpip)
```

- **`Manager`** owns everything. `StartForward`, `StopForward`, `StopServer`
  (call on server edit/delete), `Forwards`, `Servers`, `Resume` (call after
  unlock), `Close`. State changes are reported through `Config.OnEvent`.
- **One connection per server**, reference-counted by its forwards (and later
  terminals); closed when the last user stops.
- **Credentials are not kept.** Every (re)connect calls `Config.Targets`
  (backed by the vault) and drops the result once connected. While the vault
  is locked, `Targets` returns `ErrPaused` and the connection waits in state
  `paused` until `Resume`.
- **Host keys:** `hostKeyCallback` accepts only confirmed keys.
  `*UnknownHostKeyError` carries the presented key and fingerprint: the
  dashboard asks the user, stores *that* key (`SetHostKey`) and retries.
  `*HostKeyChangedError` blocks. Host-key algorithms are pinned to the
  confirmed key's type. SSH verifies the host key before authentication, so
  no credentials reach an unconfirmed server.
- **Login methods** (`auth.go`): password (+ keyboard-interactive), vault key,
  key file (relative paths resolve against `Config.BaseDir`, the portable
  folder), ssh-agent (`agent_unix.go`: `SSH_AUTH_SOCK`; `agent_windows.go`:
  the OpenSSH agent pipe).
- **Forwards** (`forward.go`) listen on 127.0.0.1 only. The port is bound
  before connecting, so a busy port fails fast (`ErrPortInUse`). Auto ports
  (`localPort` 0) try a stable port derived from the service ID
  (20000–29999), then any free port.
- **Resilience:** keep-alive every 30 s; 3 missed → reconnect. Reconnect
  back-off 1 s → 30 s. Network errors retry forever; auth failures and
  host-key problems stop the server's forwards with state `failed`.

| State | Meaning |
|---|---|
| `connecting` | first connection attempt |
| `connected` / `active` | server up / forward usable |
| `reconnecting` | connection lost, retrying |
| `paused` | waiting for the vault to be unlocked |
| `failed` | gave up (error explains why) |
| `stopped` | stopped by the user |

## Settings and logs (`internal/config`)

- `settings.json`: `port` (1024–65535, default 47811), `autoLockMinutes`
  (0 = never, max 1440, default 15), `closeTunnelsOnLock` (default false).
  A missing file means defaults; an invalid file is reported and defaults are used.
- Logs: `data/logs/tunneltab.log`, rotated at 1 MiB, keeping 3 old files.
  **Log policy:** IDs and error types (`sshx.ErrorKind`) only — never
  hostnames, addresses, fingerprints, tokens or secrets, because logs are
  not encrypted. `TestLogsContainNoSecretsOrAddresses` enforces it.
- Permissions: `RestrictToOwner` (`perm_windows.go` / `perm_unix.go`) limits
  the data folder to the current user at startup (a warning is logged where
  the file system has no permissions).
- Data folder: `--data`, else `data/` next to the executable (under `go run`,
  `./data`). `EnsureDataDir` creates it (owner-only) and reports
  `ErrNotWritable` clearly.

## Local server (`internal/server`)

### Signing in

```
tunneltab ──opens──▶ http://127.0.0.1:47811/?launch=<one-time token, 2 min>
dashboard JS: POST /api/session {launch} ──▶ {session}   (stored in localStorage)
every API call: Authorization: Bearer <session>
```

- **No cookies.** Cookies for 127.0.0.1 are sent to *every* port, including
  tunneled web apps, and browsers treat all ports as one "site". The session
  token lives in the dashboard's own storage (separate per port) and is sent
  as a header, so cross-site request forgery is impossible by design.
- Sessions last until the program exits (max 20; oldest dropped).
- Launch and session tokens are 256-bit random, stored only as SHA-256 hashes,
  and never logged.

### Checks on every request (`guard`)

1. `Host` must be `127.0.0.1:<port>` or `localhost:<port>` (DNS rebinding).
2. `Origin`, when present, must be that same address; POST/PUT/DELETE must
   have one (exception: `/api/instance/launch` and `/api/instance/started`,
   which use the instance secret).
3. `Sec-Fetch-Site` other than `same-origin`/`none` is refused.
4. `OPTIONS` is refused; no CORS headers are ever sent.
5. Security headers: strict CSP (no inline scripts), `nosniff`, `DENY`
   framing, `no-referrer`, COOP/CORP `same-origin`; API responses `no-store`.
6. Request bodies are limited to 1 MiB; unknown JSON fields are rejected.

### API reference

All paths are under `/api`; all except the first three need a session.
Errors are `{"error": "<code>", "message": "…", "field": "…"}`.

| Method & path | Body | Result |
|---|---|---|
| `POST /session` | `{launch}` | `{session}` |
| `POST /instance/launch` | header `X-TunnelTab-Instance` | `{url}` (used by a second launch) |
| `GET /state` | | `{vault: none\|locked\|unlocked, version, unlockWaitMs, minPasswordLen}` |
| `GET /events` | | event stream (see below) |
| `GET /settings` · `PUT /settings` | `Settings` | settings (+ `restartRequired` if the port changed) |
| `POST /quit` | | stops tunnels and exits |
| `POST /touch` | | 204; the user is working in the dashboard (it sends this on clicks, keys and scrolling, at most every 30 s), so auto-lock waits — moving between pages makes no other request |
| `GET /traffic` | | `{services: [{serviceId, todayIn, todayOut, lastHour: [60 × bytes per minute, oldest first]}]}` — counted on this PC (`sshx` traffic meter), in memory only |
| `POST /vault/create` | `{password}` | 400 `weak_password`, 409 `vault_exists` |
| `POST /vault/unlock` | `{password}` | 401 `wrong_password`, 429 `too_many_attempts` (+ `retryAfterMs`) |
| `POST /vault/lock` | | |
| `POST /vault/password` | `{old, new}` | |
| `GET /data` | | `{data: PublicData, forwards: [ForwardStatus], servers: [ServerStatus], terminals: [{id, serverId, openedAt, attached, client}], activity: [activity entry], health: {serverId: reading}, checks: {serviceId: check}}`; `ServerStatus` has `since` (first connect, kept across reconnects), `reconnects`, `reason` (`sshx.ErrorKind`), `held` (kept connected by the Health box's Connect) and `pingMs` (last keep-alive round trip; the first keep-alive goes out right after connecting) |
| `POST /projects` · `PUT`/`DELETE /projects/{id}` | `{name, description}` | project; delete cascades |
| `POST /servers` · `PUT`/`DELETE /servers/{id}` | `model.Server` | `PublicServer` (never secrets); blank secrets are kept on update |
| `POST /servers/{id}/move` | `{projectId}` | |
| `PUT /servers/{id}/notes` | `{notes}` | 204; free text (≤ 10 000 characters, line breaks allowed), stored in the vault; `PublicServer` includes `notes` |
| `PUT /servers/{id}/health` | `{enabled}` | 204; switches the opt-in health check on (checked at once if connected) or off (reading dropped at once); stored in the vault, `PublicServer.healthEnabled` |
| `POST /servers/{id}/connect` | | 204; the Health box's Connect: connects (409 `unknown_host_key`/`host_key_changed` as for a tunnel) and holds the connection, reading health, until Disconnect or no dashboard is open |
| `DELETE /servers/{id}/connect` | | 204; Disconnect: drops the hold (the connection closes unless a tunnel or terminal uses it) and the reading unless health is switched on |
| `POST /servers/{id}/clear-passphrase` | | |
| `POST /servers/{id}/test` | | connects once (drives host-key confirmation) |
| `POST /servers/{id}/discover` | | "Find services", only on the user's click: connects if needed (drives host-key confirmation), runs `discover.Command`, answers `{candidates: [{name, host, port, protocol, path, kind (web/maybe/other), app, process, container, image, listen (local/all/other), added, check (a service check result, or absent for ports known not to be web pages; at most 24 checked)}], docker (ok/none/denied/stopped/error), truncated}`; 422 `discover_failed` if neither `ss` nor `netstat` answered. Nothing is saved |
| `POST /servers/{id}/services` | `{services: [model.Service]}` | adds 1–200 services to the server in one vault update (all or none; a validation error names `services.<index>.<field>`); 201 with the new services |
| `POST /services` · `PUT`/`DELETE /services/{id}` | `model.Service` | service |
| `POST /services/{id}/start` | | `{forward, url}` |
| `POST /services/{id}/stop` | | |
| `POST /services/{id}/check` | | `{check: {state (responding/not_web/no_answer), protocol?, status?, at}}`; checks now, through `Manager.Through` (may connect: host-key errors as for Start). Start also checks once the tunnel is up; editing or deleting a service drops its result |
| `POST /hostkeys/confirm` | `{token, replace}` | stores the pending key |
| `POST /hostkeys/forget` | `{host}` | removes a confirmed key (asks again next time) |
| `PUT /projects/order` | `{ids}` | new order of all projects |
| `PUT /projects/{id}/servers/order` | `{ids}` | new order of the project's servers; servers from other projects in the list are moved in |
| `PUT /servers/{id}/services/order` | `{ids}` | new order of the server's services |
| `POST /updates/check` | | `{current, latest, newer, devBuild, testOf, canInstall, url, publishedAt}` (`testOf`: the release a test build was made after) or `{noRelease}` — contacts GitHub, only on request |
| `POST /updates/install` | | "Update now": downloads, verifies and installs the latest release, answers `{status: "restarting", version}`, then restarts TunnelTab; 409 `update_unavailable` / `update_busy`, 502 `update_failed` (nothing changed) |
| `POST /terminals` | `{serverId, cols, rows}` | opens a shell; `{terminalId, ticket, serverName}` (see Terminals) |
| `POST /terminals/{id}/attach` | | `{ticket, …}` to re-attach to a running session; 423 while locked, 404 once ended |
| `DELETE /terminals/{id}` | | ends a session |
| `GET /terminals/connect?ticket=…` | WebSocket | terminal stream; authenticated by the one-time ticket, not the session header |

Status codes: 400 invalid input, 401 not signed in / wrong password, 403
failed security check, 404 not found, 409 conflict (incl. host-key
confirmation needed, port in use), 413 too large, 423 vault locked, 429 wait,
502 SSH problem (`auth_failed`, `ssh_error`).

**Host-key confirmation:** `start` or `test` returns 409 with
`error: unknown_host_key` (or `host_key_changed`), `address`, `keyType`,
`fingerprint` and a `token`. The key stays on the server; after the user
confirms, the dashboard sends `{token}` (plus `replace: true` for a changed
key) to `/hostkeys/confirm` and retries. Tokens expire after 10 minutes and
are cleared when the vault locks. A token also remembers the keys that were
confirmed when it was issued: if they changed since (another tab confirmed
or replaced a key), confirming it fails with 409
`host_key_question_stale` and changes nothing, so an old "new server"
question can never replace a key without the "key changed" warning; the
dashboard then closes the question and connects again, which asks what
fits now. A token for a key that is already the confirmed one succeeds
without saving the vault (a save would replace its one backup).

### Events (`GET /api/events`)

Server-Sent Events format, read with `fetch` (so the session header can be
sent). Each `data:` line is JSON:

| `type` | Fields | Meaning |
|---|---|---|
| `tunnel` | `kind` (server/forward), `id`, `serverId`, `state`, `error`, `localPort`, and for servers `since`, `reconnects`, `reason` | SSH engine state change |
| `activity` | `at`, `kind` (server/forward/terminal/discover), `id`, `serverId`, `state`, `error`, `reconnects`, `reason` | a line for "Recent activity" (see below) |
| `health` | `serverId`, `reading` (`{health?, error?, at}`, or null when switched off or disconnected) | a server health reading |
| `check` | `serviceId`, `check` (as above, or null when the service changed) | a service check result |
| `discover` | `serverId`, `step` (connecting, scanning, checking), `done`, `total` (checking only) | how far a Find services scan has got, for the dialog that started it |
| `update` | `step` (checking, verifying, downloading, unpacking, installing, restarting, failed), `done`, `total` (bytes, while downloading), `version` | how far "Update now" has got (see Updates) |
| `vault` | `state` (locked/unlocked) | lock state changed |
| `data` | | stored data changed: re-fetch `/api/data` |
| `resync` | | events were dropped: re-fetch everything |

A `: ping` comment is sent every 20 s. Slow clients get `resync` instead of
blocking the app.

**Activity log** (`internal/server/activity.go`): the last 200 connection,
tunnel and terminal events worth showing (connected, reconnecting, failed,
stopped; tunnel started/stopped/failed; terminal opened/ended; services
searched — each search has its own ID, so every one is listed), with a
repeated state recorded once. IDs and states only; the dashboard looks the
names up. Kept in memory only, never written to disk or the log file.

**Server health** (`internal/server/health.go`, `internal/health`): opt-in
per server (`model.Server.HealthEnabled`, off by default). Every 30 s, if a
dashboard event stream is open and the vault is unlocked, each enabled
server is checked: `Manager.RunIfConnected` runs `health.Command` over the
server's **existing** connection (`ErrNotConnected` otherwise — it never
dials), 10 s timeout, 64 KiB output limit; `health.Parse` turns the output
into load, cores, memory, uptime and up to 5 disks. Readings are kept in
memory, sent as `health` events and in `GET /api/data`; a server that
disconnects or is switched off loses its reading at once. A server that
connects is checked right away.

The Health box's **Connect** (`POST /servers/{id}/connect`) calls
`Manager.Hold`: it connects (host-key errors as for a tunnel) and keeps
the connection open with nothing else using it, and the server is checked
like an enabled one. **Disconnect** (`DELETE /servers/{id}/connect`) calls
`Manager.Unhold`. Holds are in memory only, shown as `held` in
`ServerStatus`, and dropped by `StopServer` (edit/delete), `StopAll` (lock
with "close tunnels on lock"), a server that can't be reconnected, and the
first 30 s round with no dashboard open.

**Find services** (`internal/server/discover.go`, `internal/discover`):
only when the user clicks **Find services** and confirms.
`Manager.Run` connects if needed (and lets the connection go afterwards,
so it closes unless a tunnel or terminal uses it), runs the constant
`discover.Command` (`ss -tlnp`, `netstat -tln`, `docker ps` with a
three-field JSON format; `;` only, so it works in fish too), 20 s timeout,
256 KiB output limit. `discover.Parse` prefers `ss` (falls back to
`netstat`), joins listening ports with published container ports (one
candidate per port), names them by a built-in table of well-known images
(then ports), else by container or program name (cleaned: printable, ≤ 60
characters), and picks the address to tunnel to (`127.0.0.1` for loopback
or all-address listeners). At most 200 candidates; every candidate makes a
valid service (fuzzed). The server marks ports that already have a
service; the result isn't stored. The user ticks rows and `POST
/servers/{id}/services` adds them in one vault update. Each search adds an
activity line (kind `discover`).
The scan and the checks share one connection (`Manager.Through` around
`Manager.Run`): after parsing, up to 24 candidates that may be web pages
(not `other`) are checked, 8 at a time, with `webcheck.Check`; each
candidate carries its `check`. The dashboard ticks those that answered,
sets their protocol to the one they answered on, and moves unknown ports
that didn't answer to *Not web pages*.

**Service checks** (`internal/server/check.go`, `internal/webcheck`): does
the app behind a service answer? `webcheck.Check` opens a channel through
the server's connection to the service's address and sends one `HEAD`
request, over TLS first, then plain: *responding* (with protocol and
status), *not_web* (connected, no HTTP answer) or *no_answer* (couldn't
connect). Run after Start (once the tunnel is up), on **Check**
(`POST /services/{id}/check`), by the dashboard on **Open** when the last
result wasn't OK, and in Find services. Results are in memory, sent as
`check` events and in `GET /api/data`, and dropped when the service is
edited or deleted.


## Terminals

```
dashboard: "+ New terminal" ──▶ TermView in the server's Terminals tab (js/termview.js)
            "Pop out ↗"      ──▶ terminal.html#<serverId>/<terminalId> (same session, new tab)
termview.js: POST /api/terminals {serverId, cols, rows, client?}
             ──▶ Manager.OpenShell (host-key/login errors come back as API errors)
             ◀── {terminalId, ticket, serverName}   → address becomes #<serverId>/<terminalId>
             WebSocket /api/terminals/connect?ticket=…  (one-time, 30 s; Origin checked)
             ⇄ binary frames = terminal bytes; text frames = JSON control
re-attach:   POST /api/terminals/{id}/attach ──▶ {ticket} ──▶ WebSocket again
```

- **Engine** (`internal/sshx/shell.go`): `OpenShell` requests an
  `xterm-256color` PTY and a shell on the server's shared connection (so it
  reuses a tunnel's connection, and keeps the connection open while in use).
  `Resize`, `Close`, `Done`, `ExitStatus` → `(code, nil)` for a normal exit,
  `(-1, ErrShellClosed)` when TunnelTab closed it, `(-1, err)` when the
  connection dropped. Max 32 terminals.
- **Sessions** (`internal/server/terminal.go`): a `termSession` owns the
  shell and lives independently of any page. `runTerminal` reads the shell's
  output, keeps the last 512 KiB for replay, and forwards it to the attached
  page. A page **attaches** over a WebSocket (a new page takes over from an
  old one) and receives `{"type":"attached"}` followed by the replay.
- **Locking detaches, it doesn't close.** `hideTerminals` sends
  `{"type":"locked"}` and disconnects every page, so nothing can be seen or
  typed; the shells — and programs running in them, like a `docker pull` —
  keep going. Attaching is refused while locked. After unlock the page
  re-attaches by itself (it watches the event stream) and the output is
  replayed. The page also blanks its screen while locked.
- **Closing the tab ends the session.** The page keeps its event stream
  open with `?terminal=<id>` for as long as the tab exists (locked, hidden
  or not — browsers don't throttle open connections in background tabs,
  unlike timers), which counts as a *watcher*. A session with no attached
  page and no watcher is closed 10 s later (`terminalDetachGrace`, checked
  every 5 s), whether or not the vault is locked — long enough for a reload
  to re-attach. `DELETE /api/terminals/{id}` ends one explicitly.
- **Terminals in the dashboard belong to its tab.** The dashboard's event
  stream carries a random per-tab ID (`?client=<id>`, kept in
  sessionStorage so a reload keeps it), and it passes the same ID when
  opening a terminal (`client`). A session whose owner's stream is open
  (or closed less than 10 s ago) is kept, with or without an attached page
  — so in-page terminals survive looking at other servers, locking and
  reloading, and end when the dashboard tab closes. `GET /api/data` lists
  sessions with their `client`, so a reloaded dashboard finds its own.
  **Pop out** attaches `terminal.html` to the same session (taking it over);
  the dashboard's view shows *open in another tab* and **Bring back here**
  re-attaches.
- **Other messages:** browser → app `{"type":"resize","cols":…,"rows":…}`;
  app → browser `{"type":"reconnecting"}` / `{"type":"reconnected"}` (see
  below) and `{"type":"exit","code":…,"message":…}` when the session ends.
- **A dropped connection keeps the session.** When the SSH connection
  drops, the shell ends but `sshx.Shell` keeps its reference to the
  connection (and its place in the Manager, so Disconnect, Quit and
  `CloseShells` still find it). The connection therefore stays and
  reconnects with the usual back-off, shown as `reconnecting` everywhere,
  never `failed` — the same as when a tunnel holds it. The session tells
  the page `reconnecting`, adds a grey line to the output, drops typing, and
  every second (`terminalReopenEvery`) calls `Shell.Reopen`, which opens a
  new shell on the connection once it is back (then lets go of the old
  reference) and the page is told `reconnected`; same terminal ID, old
  output kept. Closing the terminal (`DELETE`, the tab closing, Disconnect)
  lets go of the connection at once; if reconnecting gives up (login or
  host key), the session ends with the reason. A session that ends without
  an exit code counts as a dropped connection only if the connection
  closes too within 2 s (`lostConnectionWait`): otherwise it's an ordinary
  end, so a server that hangs up a shell doesn't loop.
- **View** (`js/termview.js`, `TermView`): one session in an element —
  xterm.js (vendored in `web/static/vendor/xterm/`) with the fit addon,
  the WebSocket, a message bar (New session / Reconnect / Bring back here);
  used by the terminal page (`terminal.html`, `js/terminal.js`) and the
  dashboard's Terminals tab (several per server, Split = two side by side);
  copy/paste like Windows Terminal (Ctrl+C copies a selection, otherwise
  interrupts; Ctrl+V pastes; right-click copies a selection; the key
  handler calls preventDefault so Ctrl+Shift+C doesn't open Firefox's
  Inspector); re-attaches after a reload,
  lock or blip; *New session* (or Enter) after the shell ends. On
  `reconnecting` it shows *Reconnecting…* and resets modes the old program
  may have left on (full screen, mouse reporting…) without clearing the
  screen or moving the cursor.
- **CSP exception:** xterm.js creates `<style>` elements (it has no nonce
  support), so the two pages that show terminals — `/terminal.html` and the
  dashboard (`/`, `/index.html`) — get `style-src 'self' 'unsafe-inline'`.
  Inline *styles* only: scripts stay `'self'`-only everywhere, the dashboard
  never inserts HTML (`h()` builds text nodes), and terminal output is drawn
  as text.

## Dashboard (`web/static`)

Plain ES modules, no framework and no build step; the files are embedded in
the executable as-is.

| File | Job |
|---|---|
| `index.html` | Page shell; loads `app.css` and `js/app.js` |
| `js/api.js` | Sign-in (launch link → session in localStorage), `api()` fetch wrapper (`ApiError` with `code`), event stream reader with reconnect |
| `js/dom.js` | `h(tag, attrs, …children)` element builder — text is always inserted as text nodes, never `innerHTML` |
| `js/dialogs.js` | Native `<dialog>` modals (`openDialog`, `confirmDialog`), form `field`/`checkbox`, `tabs` (Settings), toasts |
| `js/forms.js` | Project/server/service/settings dialogs; `withHostKeys(fn)` runs a connecting call and handles fingerprint confirmation |
| `js/app.js` | Screens (signed out, setup, unlock, dashboard), state, rendering, live events, actions |
| `js/termview.js` | `TermView`: one terminal session (xterm.js + WebSocket), used by the dashboard and the terminal page |
| `terminal.html`, `js/terminal.js`, `terminal.css` | The terminal page (see Terminals) |
| `vendor/xterm/` | xterm.js 6 + fit addon (MIT), bundled; see its README to update |
| `app.css` | All styling (dark GitHub palette from local.browser) |

**Flow:** `start()` signs in → `GET /api/state` → setup, unlock or
dashboard. The dashboard is a sidebar (projects and servers) plus one
page chosen by the address: `#/` the Overview (totals, needs attention,
running now, recent activity, all servers), `#/server/<id>[/services|/terminals|/activity]`
a server page with tabs. `hashchange` re-renders; focus moves to the new
page's heading. Below 860 px the sidebar becomes a slide-in panel (☰ Menu). The dashboard renders from `GET /api/data`; events update tunnel
states in place (`tunnel`), trigger a re-fetch (`data`, `resync`) or switch
to the unlock screen (`vault`). Screens the user may be typing into are
never redrawn by a background refresh.

**Reordering** (`app.js`): projects and servers in the sidebar and apps on the
Services tab each have a ⠿ `grip` button that is the drag
source (HTML5 drag and drop; types `application/x-tunneltab-project`,
`…-server`, and `…-service-<serverId>` so services only drop within their
server) and also moves the item with ↑/↓. `dropTarget` shows a line
before/after the hovered row; the new complete order is sent to the order
endpoints, and focus returns to the moved item's grip after re-rendering.

**Rules** (the CSP enforces the first):
- No inline `<script>`, no `on…=` attributes, no `style=` attributes
  (the terminal page may use inline styles only because xterm.js needs them).
- No `innerHTML`; build DOM with `h()`.
- Pages opened for services get no `window.opener` access.
- Messages that must be seen while a dialog is open go *inside* the dialog
  (toasts sit behind the modal backdrop).
- Live events redraw the whole page, so `renderDashboard` keeps the keyboard
  focus: `focusKey`/`restoreFocus` (`dom.js`) find the same control again
  (or the one that replaced it, e.g. Start → Stop). Disable a focused button
  with `setBusy`, not `disabled = true`, so its focus survives too.

## Startup and shutdown (`cmd/tunneltab`)

1. Resolve and create the data folder; open the log; load settings.
2. Lock `data/instance.lock` (`platform.LockDataDir`: `flock` on Linux,
   `LockFileEx` on Windows), so only one process uses the data folder and
   two copies can't overwrite each other's vault saves. The OS releases the
   lock when the process exits, even after a crash. If another process
   holds it, ask that copy for a new launch link via `data/instance.json`
   (`/api/instance/launch` with its secret), open it and exit. That copy
   may still be starting or quitting, so retry both for up to 20 s, then
   show an error. A file system that can't lock (some network shares) only
   gets a log warning and the instance-file check.
3. Listen on 127.0.0.1:`port` (settings, `--port`); if busy, any free port.
4. Write `instance.json` (port + random secret), create the server, open the
   browser at the launch link (`--no-browser` prints it instead).
5. Run until Quit, Ctrl+C or SIGTERM; then stop all tunnels, end event
   streams, shut the HTTP server down and delete `instance.json`. The lock
   is released when the process exits. If this takes more than 15 s, a
   watchdog ends the program anyway.

6. **After "Update now"** (`restart.go`): the new files are already in place
   (the old ones renamed to `*.old`). Shut down as in step 5, delete
   `instance.json` and release the data folder lock (the new version needs
   it), even if the watchdog fired (it then closes the dashboard server
   first, so a hang can't leave the new files in place unchecked); then
   start the program again with the same arguments and wait up to 30 s
   for it to write `instance.json` with its own PID *and* answer
   `/api/instance/started` (so it has opened the vault and serves the
   dashboard; the file alone is written before that). If it doesn't, kill
   it, wait for it to exit, put
   the `.old` files back (`update.Rollback`, retried for up to 10 s while
   Windows still holds them) and start the previous version instead.
   The new version deletes the `.old` files and `.update/` only after that
   confirmation (or after 60 s without one, e.g. when started by hand), so
   a rollback always finds them; it retries, since Windows keeps the old
   program locked until it exits.

### Updates (`internal/update`)

`Check` reads the release's tag and assets; `canInstall` needs a release
newer than the running version with `tunneltab-<v>.zip`,
`SHA256SUMS.txt` and `SHA256SUMS.txt.sig`, all at
`https://github.com/Aerobit/TunnelTab/releases/download/` (or the test
server given with `--update-url`). A test build (`git describe`
version such as `0.6.1-3-g652f29e` or `0.6.1-dirty`, as CI builds are
named) counts as the release it was made after (`testOf`), so only a newer
release is offered; other non-release versions (`dev`) are `devBuild` and
never offered one. `Download` fetches the checksums and
signature, verifies the signature against `ReleasePublicKey`, requires the
zip for exactly that version, downloads it into `.update/` next to the
program, compares its SHA-256, and extracts only the known package files
(`PackageFiles`) from the zip's `tunneltab/` folder. The running program
receives this OS's binary even if it was renamed. `Install` renames each
file it replaces to `<name>.old` (possible even for a running `.exe`) and
moves the new one in; any failure rolls back.

**Progress:** `Download` reports each step to a callback (`update.Progress`:
verifying → downloading with `done`/`total` bytes, at most ~100 reports →
unpacking); the server adds `checking` first, `installing` before
`Install`, then `restarting` or `failed`, and sends them all as `update`
events. Settings draws them with a `<progress>` bar. After the answer the
page shows *Updating TunnelTab* and polls `GET /api/state` with its old
session token: the restarted program refuses it (401), which is how the
page sees the restart. When the new tab has signed in (its session
replaces the old one in `localStorage`), the page asks `/api/state` with
that session and says whether the new version runs or the previous one
came back (rollback).


Flags: `--data <dir>`, `--port <n>`, `--no-browser`, `--version`,
`--update-url <url>` (tests: a fake release API; downloads may then come
from that server).

## Packaging (`scripts/`, `packaging/`)

| File | Role |
|---|---|
| `scripts/build.sh`, `scripts/build.ps1` | Build both executables and the portable folder, zip and checksum (identical output) |
| `scripts/mkzip` | Zips the folder (keeps the Linux binary executable) |
| `scripts/notices` | Writes `THIRD_PARTY_NOTICES.txt` from the modules compiled in |
| `scripts/signsums` | Signs `SHA256SUMS.txt` in the release workflow; `genkey` / `verify` for the release key (see DEVELOPMENT.md → Release signing key) |
| `packaging/README.txt` | Copied into the folder (version stamped in) |
| `packaging/icon.png` | Windows executable icon (rendered from `web/static/icon.svg`) |
| `web/static/icon.svg`, `logo.svg` | Browser-tab icon (with its dark square) and the transparent logo shown inside the pages |

`internal/config.RunningFromTempFolder` stops the Windows program when it's
started from inside the ZIP (Explorer runs a temporary copy there, and the
vault would be lost with it).

## Where to change what

| I want to… | Look in |
|---|---|
| Change the build or packaging | `scripts/build.sh` **and** `scripts/build.ps1` (keep them identical), `packaging/` |
| Change the exe icon or version details | `packaging/icon.png`; the `go-winres` call in both build scripts |
| Add a Go dependency | also check `go run ./scripts/notices` finds its license |
| Change CI or releases | `.github/workflows/` |
| Add a command-line flag | `cmd/tunneltab/main.go` |
| Change how updates work | `internal/update` (GitHub API, version comparison, download + verification, install), `cmd/tunneltab/restart.go` (restart, rollback), `web/static/js/forms.js` (Settings), `tests/e2e/update.js`. Keep CLAUDE.md invariant 9. |
| Add an API endpoint | `internal/server/api.go` (`routes` + handler) and a test in `server_test.go`; document it in the API table above |
| Add an event type | `internal/server/events.go` and the events table above |
| Change the dashboard look | `web/static/app.css` |
| Add a dialog or form | `web/static/js/forms.js` (use `openDialog` + `field`) |
| Change what the dashboard shows | `web/static/js/app.js` (`renderProject`, `renderServer`, `renderService`) |
| Add a field to servers/services | `internal/model`, then the API in `internal/server`, then `web/static` |
| Change encryption parameters | `internal/vault/format.go` — `DefaultParams` for new vaults; bounds for reading |
| Change the vault file format | `internal/vault/format.go` — bump `formatVersion`, keep reading old files |
| Add a validation rule | `internal/model/validate.go` (+ a case in `model_test.go`) |
| Add a secret field | `internal/model/model.go` with `secret:"true"`, then `public.go` (the test will remind you) |
| Add a setting | `internal/config/settings.go` (`Settings`, defaults, `Validate`) |
| Add a login method | `internal/model` (`AuthType`, validation, `normalizeAuth`, `Public`), then `internal/sshx/auth.go` |
| Change keep-alive / reconnect timing | `sshx.NewManager` defaults (`internal/sshx/manager.go`) |
| Change the terminal (theme, keys, font) | `web/static/js/terminal.js` |
| Update xterm.js | replace the files in `web/static/vendor/xterm/` (see its README), run `tests/e2e` |
| Test something against SSH | `internal/sshx/sshtest` (add features there, never use a real server) |
| Support another terminal program | `internal/platform` |

This table grows as the code is written.
