# TunnelTab — Project Plan

> Status: **v0.8.9 released** (2026-10-09: terminals drawn with xterm.js WebGL so the last column no longer runs under the scrollbar at Windows display scaling; v0.8.8 only moved the scrollbar; v0.8.7 = §23). v0.1.0 (2026-09-30) completed the original plan (§1–§9); later releases are in §11–§14, §18 (tray icon) and §19–§23. Ideas not yet scheduled are in §10, §15, §16 (remote desktop) and §17 (file browser and transfers).
> Successor to `local.browser` (Chrome extension + Node native host). Starts fresh; no data import.

## 1. Goal

A **portable, self-contained** app for reaching your own VPSs securely, with no install and no third-party accounts:

- One click → **SSH terminal** to a server.
- One click → **web UI quick launch** (tunnel starts if needed, page opens in a browser tab).
- Everything lives in **one folder** you can copy to any Windows or Linux PC or USB stick.
- Credentials protected by a **master password**; stealing the folder alone reveals nothing.

## 2. Decisions (locked)

| Decision | Choice |
|---|---|
| Name | **TunnelTab** (`tunneltab`) |
| Form factor | Single executable + dashboard in your normal browser (`http://127.0.0.1:<port>`) |
| Language | Go (single static binary, built-in SSH client, cross-compiles, no runtime to install) |
| Platforms | Windows x64 (primary), Linux x64 |
| Credentials | Encrypted vault unlocked by a master password; SSH keys / ssh-agent preferred, passwords supported |
| Terminal | Built into the page (xterm.js); optional "open in system terminal" for key/agent logins |
| Frontend | Vanilla JS + CSS, no framework, no build step, all assets bundled locally |
| Migration | None — start fresh |
| Repo | New repo `Aerobit/TunnelTab`; this folder is uploaded as-is |

## 3. Architecture

```
 Browser tab (dashboard UI, xterm.js)
        │  plain HTTP + WebSocket over loopback, bound to 127.0.0.1 only
        │  session cookie + Host/Origin checks
        ▼
 tunneltab executable
   ├── server   local web server: auth, REST API, event stream, terminal WebSockets
   ├── vault    master-password vault (Argon2id → AES-256-GCM), auto-lock
   ├── sshx     SSH engine: one connection per server, host-key checks,
   │            port forwards, PTY sessions, keep-alive, auto-reconnect
   └── config   portable paths, settings, logging
        │
        ▼  SSH (golang.org/x/crypto/ssh — no external ssh/sshpass needed)
   Your VPSs
```

### Portable folder (what users run)

```
tunneltab/
├── tunneltab.exe            Windows
├── tunneltab-linux-amd64    Linux x64
├── README.txt
└── data/                    created on first run, shared by all builds
    ├── vault.enc            projects, servers, services, secrets — all encrypted
    ├── settings.json        non-secret prefs (port, auto-lock time)
    └── logs/                rotated; never contains secrets
```

- Data lives **next to the executable** (true portable mode). `--data <dir>` overrides.
- If the folder isn't writable, the app says so clearly instead of silently writing elsewhere.

### Source repo layout (what goes on GitHub)

```
tunneltab/
├── README.md               overview, screenshots, quick start
├── PLAN.md                 this file (kept up to date / archived when done)
├── LICENSE                 MIT
├── CHANGELOG.md
├── CLAUDE.md               rules for AI-assisted edits (security invariants, conventions)
├── AGENTS.md               same rules for other AI tools (points at CLAUDE.md)
├── .gitignore              data/, dist/, secrets, OS junk
├── .gitattributes          line endings (LF; CRLF for .ps1)
├── .editorconfig
├── .vscode/extensions.json recommended VS Code extensions
├── go.mod / go.sum
├── cmd/tunneltab/main.go   entry point: flags, single-instance, open browser
├── internal/
│   ├── config/             portable paths, settings, logging
│   ├── vault/              KDF, encryption, file format, auto-lock
│   ├── model/              projects, servers, services (data types + validation)
│   ├── sshx/               connections, auth methods, host-key checks, forwards, PTY
│   │   └── sshtest/        in-process SSH server for tests
│   ├── server/             HTTP server, auth/session, API handlers, events, terminal WS
│   ├── platform/           open browser, system terminal launch, per-OS bits
│   └── atomicfile/         crash-safe file writes (used by vault and settings)
├── web/                    web.go embeds static/ into the binary
│   └── static/             dashboard (HTML/CSS/JS, vendored xterm.js)
├── docs/
│   ├── USER_GUIDE.md
│   ├── ARCHITECTURE.md
│   ├── SECURITY.md
│   └── DEVELOPMENT.md
├── scripts/
│   ├── build.sh            builds Windows + Linux x64 → dist/tunneltab-<version>.zip
│   ├── build.ps1           same, from Windows
│   └── mkzip/              small Go helper that zips the portable folder (no zip tool needed)
├── packaging/
│   └── README.txt          copied into the portable folder (version stamped in)
└── .github/workflows/
    ├── ci.yml              vet, lint, tests on every push/PR (Windows + Linux)
    └── release.yml         on tag v*: build + attach portable zip to a GitHub Release
```

### Dependencies (kept minimal)

- `golang.org/x/crypto` — ssh, ssh/agent, ssh/knownhosts, argon2
- A small WebSocket library (e.g. `github.com/coder/websocket`)
- `github.com/Microsoft/go-winio` — Windows OpenSSH agent named pipe
- xterm.js + fit addon — vendored into `web/vendor/`, no CDN

## 4. Data model

- **Project** — `id, name, description, order`
- **Server** — `id, projectId, name, host, port, username, auth`
  - `auth.type`: `agent` | `keyFile` | `keyVault` | `password`
  - `keyFile`: path (relative paths resolve inside the portable folder)
  - `keyVault`: private key stored inside the vault; optional passphrase
  - `password`: stored inside the vault
- **Service** (replaces "port mapping") — `id, serverId, label, remoteHost (default 127.0.0.1), remotePort, localPort (or auto), protocol http|https, path (e.g. /admin), autoStart`

All IDs are generated server-side. All fields validated in `internal/model` before use.

## 5. Security design

| Threat | Mitigation |
|---|---|
| Someone copies the folder / USB stick | Vault encrypted with key from master password (Argon2id, ~64 MiB, tuned to ~0.5–1 s). No key stored on disk. |
| Wrong password / tampering | AES-256-GCM authentication fails → clear "wrong password or corrupted vault" error. Atomic writes + one backup copy. |
| Secrets leaking at runtime | No secrets in command lines, env vars, logs, API responses or error messages. Secrets only returned to the SSH engine, never to the browser. |
| Unattended PC | Auto-lock after inactivity (default 15 min, configurable). Lock wipes the in-memory key; running tunnels keep running unless "close all on lock" is enabled. |
| Server impersonation (MITM) | Confirmed host keys stored inside the encrypted vault (a plain `known_hosts` file would reveal which servers you use). First connection shows the fingerprint and asks you to confirm; no credentials are sent before that. Changed key → hard block with explanation and a deliberate "replace key" action. |
| Malicious website attacking the local server | Bind 127.0.0.1 only; one-time launch link → session token kept by the dashboard and sent as a header (not a cookie: cookies for 127.0.0.1 go to every port, including tunneled apps); reject bad `Host` (DNS rebinding), cross-origin `Origin` and cross-site `Sec-Fetch-Site`; strict CSP; no CORS. |
| Other devices on the network using tunnels | Forwards bind to 127.0.0.1 only. |
| Command injection | No shell is ever invoked for SSH. Only the optional "system terminal" launch runs an external program, with arguments passed as an array and validated. |
| Brute-force on the unlock screen | Delay/backoff after failed attempts (the KDF cost already makes offline guessing slow). |

`docs/SECURITY.md` will state plainly what is **not** protected (e.g. malware already running as your user, a compromised VPS).

## 6. Features

1. **First run** — create master password (strength meter, confirmation, warning that it can't be recovered).
2. **Unlock screen**; manual **Lock** button; auto-lock.
3. **Projects / servers / services** — add, edit, delete, drag between projects (same look as local.browser).
4. **Connection management** — one SSH connection per server, shared by its tunnels and terminals; keep-alive; auto-reconnect with backoff; live status dot per server/service.
5. **Web UI quick launch** — "Open" starts the tunnel if needed, then opens `protocol://127.0.0.1:<localPort><path>`. Local port "auto" gives each service a stable port (derived from its ID) so cookies/bookmarks survive restarts; a fixed port that's busy is reported clearly rather than silently changed.
6. **Built-in terminal** — tabs, resize, copy/paste, reconnect; multiple terminals per server.
7. ~~System terminal~~ — deferred to after v1 (see Out of scope).
8. **Settings** — auto-lock time, close-tunnels-on-lock, change master password, back up vault, dashboard port.
9. **Quit** — stops all tunnels cleanly and exits. Re-running the exe while running just opens a new dashboard tab (single instance).

## 7. Build phases

Each phase ends with tests passing and docs updated (`ARCHITECTURE.md` grows with the code).

| # | Phase | Done when |
|---|---|---|
| 0 ✅ | **Scaffold** — repo layout, go.mod, build scripts, CI, docs skeletons, CLAUDE.md/AGENTS.md | `build.sh` produces empty binaries for all targets |
| 1 ✅ | **Config + vault + model** | Create/unlock/change-password/lock round-trip; tamper & wrong-password tests |
| 2 ✅ | **SSH engine** — auth methods, host-key checks, connection pool, forwards, reconnect | Integration tests against an in-process test SSH server: forward traffic, bad host key blocked, reconnect works |
| 3 ✅ | **Local server + API** — session auth, Host/Origin checks, REST, event stream | Security tests: requests without a session / wrong Host / wrong Origin rejected |
| 4 ✅ | **Dashboard UI** — port local.browser design; unlock, projects, servers, services, fingerprint prompt, settings | Automated browser walkthrough (tests/e2e) passes; manual check in your browser |
| 5 ✅ | **Terminal** — xterm.js ↔ PTY over WebSocket (system-terminal launcher deferred: it would bypass TunnelTab's host-key checks and can't use vault keys) | Interactive shell against test server; resize works |
| 5b ✅ | **README screenshots** — generated by a script (tests/e2e) with made-up demo data, saved in docs/images/, shown in README.md; re-run whenever the UI changes | README shows setup, dashboard, fingerprint prompt, terminal, settings |
| 6 ✅ | **Hardening** — security review, fuzz validation inputs, log redaction check, race detector | `go test -race ./...` clean; review findings fixed |
| 7 ✅ | **Packaging + docs** — portable zip, README.txt, USER_GUIDE, release workflow | Zip runs on a clean Windows PC by double-click; Linux via `./tunneltab-linux-amd64` |

Testing environment: everything through phase 6 is built and tested in the dev container (Go installed without admin rights). Final phase-7 check is done by you on Windows.

## 8. Documentation deliverables

| File | Audience | Contents |
|---|---|---|
| `README.md` | Everyone | What it is, screenshots, download/quick start, feature list, links |
| `docs/USER_GUIDE.md` | Users | First run, adding servers, keys vs passwords vs agent, fingerprints, terminal, quick launch, backups, moving between PCs, troubleshooting, FAQ |
| `docs/ARCHITECTURE.md` | Developers | Component diagram, request/data flow, vault file format, API reference, event types, "where to change what" guide |
| `docs/SECURITY.md` | Everyone | Threat model, crypto choices + parameters, what's out of scope, how to report issues |
| `docs/DEVELOPMENT.md` | Developers | Install Go, build, test, run in dev mode, release process, code conventions |
| `CLAUDE.md` / `AGENTS.md` | AI assistants | Security invariants that must never be broken, conventions, how to test |
| `CHANGELOG.md` | Everyone | Keep-a-Changelog format, starting at 0.1.0 |

## 9. Known trade-offs

- Unsigned `.exe` → Windows SmartScreen warning ("More info → Run anyway"). Code signing is optional/paid.
- No system tray icon in v1 (adds per-OS complexity); the app runs until **Quit**. Added in v0.8.0 (§18).
- Dashboard is a browser tab, not a native window — by design, so no extra runtime is bundled.

## 10. Out of scope for v1 (possible later)

~~System tray icon~~ (§18) · macOS build · Linux ARM build · "Open in system terminal" (would need TunnelTab's host-key checks and vault keys handed to an external ssh) · jump hosts / ProxyJump · SOCKS proxy mode · SFTP file browser (idea written up in §17) · import from local.browser or `~/.ssh/config` · code signing.

## 11. v0.2.0 — "Update now" (released 2026-10-01)

Today **Check for updates** only reports a newer version and links to the
release page; you download, extract and replace the files yourself. v0.2.0
adds an **Update now** button that does this for you, without giving up the
"no network unless you click" rule.

### What the user sees

1. Settings → **Check for updates** → "TunnelTab 0.2.0 is available" + release notes link + **Update now**.
2. **Update now** asks first: "TunnelTab will restart. This closes 3 terminals and 2 tunnels. Update now?"
3. Progress: downloading → verifying → installing → restarting.
4. The new version opens a fresh dashboard tab; you unlock with your master password as usual. Your `data` folder is untouched.
5. If anything fails, nothing is replaced and the message says why (e.g. "the download is not signed by TunnelTab — not installed").

### Decisions

| Topic | Decision |
|---|---|
| When | Only when the user clicks **Update now**. Still no automatic checks or downloads. |
| Source | Only `github.com/Aerobit/TunnelTab` release assets (same host check as today's links). |
| Trust | Releases are **signed**. The release workflow signs `SHA256SUMS.txt` with an Ed25519 private key stored as a GitHub Actions secret and publishes `SHA256SUMS.txt.sig`. The app has the public key built in and installs only a zip whose SHA-256 matches a correctly signed `SHA256SUMS.txt`. Go standard library only (`crypto/ed25519`, `crypto/sha256`) — no new dependencies. |
| Versions | Only installs a version newer than the running one (no downgrades); dev builds can't self-update. |
| Replacing a running exe | Download and verify into `<exe folder>/.update/`, rename the running `tunneltab.exe` → `tunneltab.exe.old`, move the new exe into place (Linux: keep mode 0755), start it, then quit. The new process waits for the old one to release the port/single-instance lock. |
| Rollback | On the next start, if the new version started fine it deletes `*.old` and `.update/`. If it fails to start, the old exe is put back. |
| What is replaced | Only the executable for the current OS (and the bundled `README.txt`/notices). Never the `data` folder. |
| Refuse to update when | Running from a temp/read-only folder, the exe folder isn't writable, or the vault is mid-save. |
| Size limits | Download capped (e.g. 100 MB) and time-limited; zip extraction guards against path traversal and zip bombs. |

### One-time setup (you)

1. Generate the signing key pair (I'll give exact commands; the private key never goes into the repo).
2. Add the private key as a GitHub secret (click-by-click steps provided).
3. The public key is committed in `internal/update`.

**Note:** v0.1.0 has no updater, so the step from 0.1.0 → 0.2.0 is done by hand
one last time. From 0.2.0 onward, **Update now** works.

### Build phases

| # | Phase | Done when |
|---|---|---|
| U1 ✅ | **Release signing** — signing tool (`scripts/` or `internal/devtools`), release workflow signs `SHA256SUMS.txt`, key setup documented in `docs/RELEASE_CHECKLIST.md` / `DEVELOPMENT.md` | A test tag produces `SHA256SUMS.txt.sig` that verifies with the committed public key |
| U2 ✅ | **Download + verify** (`internal/update`) — fetch assets, size/time limits, signature + hash check, safe unzip | Tests with a fake release server: good release accepted; bad signature, wrong hash, wrong host, oversized, older version, path-traversal zip all rejected |
| U3 ✅ | **Install + restart** — rename/swap, relaunch, wait for port, cleanup of `.old`, rollback on failed start | Tests on Linux in the container; you test on Windows with a real 0.2.0 → 0.2.1 update |
| U4 ✅ | **UI** — Update now button, confirm dialog listing open terminals/tunnels, progress and errors | `tests/e2e` walkthrough with the fake release server |
| U5 ✅ | **Docs + security** — update CLAUDE.md invariant ("never downloads or runs anything" → "only on click, only signed releases"), SECURITY.md, SECURITY_REVIEW.md, USER_GUIDE (Updates + FAQ), ARCHITECTURE (API `POST /updates/install`), CHANGELOG | Docs match behaviour; security review of the new code done |

## 12. v0.3.0 / v0.4.0 — Dashboard redesign (released 2026-10-01)

The dashboard is one long list today. The redesign uses a **sidebar** with
an **Overview** home and a **page per server** with tabs, and moves
terminals **into the page**. Mockups (example data): the "Blend" page of
the *TunnelTab Dashboard Concepts* canvas,
https://claude.ai/artifact/J8Tm3ZRyoxxjK41uDUoRoz.

### What the user sees

- **Sidebar** (always visible): Overview, then projects and their servers
  with status dots and short status ("2 running", "reconnecting"); + Project;
  Settings, Lock, Quit at the bottom. On a narrow window it folds into a
  menu button.
- **Overview** (the home screen): totals (servers online, tunnels running,
  terminals open, traffic today), a **Needs attention** banner (a server
  reconnecting, a busy port…), **Running now** (every running tunnel and
  terminal, with Stop / Open / Show), **Recent activity**, and a table of
  **all servers**.
- **Server page**, with tabs:
  - **Overview** — health (when switched on), connection (connected since,
    ping, reconnects today, login method, fingerprint), apps, notes.
  - **Services** — start/stop, open, edit, reorder.
  - **Terminals** — terminals **inside the page** (see below).
  - **Activity** — this server's recent events.
  - **Notes** — free text about the server.
- **Built-in terminals:** **+ New terminal** opens a terminal in the
  server's Terminals tab, with its own tab strip (several terminals per
  server), **Split** (two side by side) and **Pop out ↗** (the same session
  in its own browser tab, as today). The address remembers where you are
  (`#/server/<id>/terminals`), so a reload comes back to the same place.
- **Server health** (CPU load, memory, disk, server uptime): **off by
  default**; switched on or off per server at any time in **Settings →
  Server health** (and from the server's Overview tab). *(Changed in
  0.6.0, see §14: the Overview tab has Connect/Disconnect instead of the
  switch, and the setting moved to Settings → Servers.)*

### Decisions

| Topic | Decision |
|---|---|
| Navigation | Hash routes (`#/overview`, `#/server/<id>/<tab>`); no framework, still vanilla JS with `h()`. Back/forward work. |
| Reordering | Kept: servers (and moving between projects) in the sidebar; services in the Services tab; projects in the sidebar. Keyboard reordering stays. |
| Terminals | Same terminal sessions and WebSocket protocol as today. In-page terminals stay alive while you look at another server or the Overview; × ends one; closing the dashboard tab ends them (as closing a terminal tab does today). **Pop out** hands the session to a terminal tab (the existing "opened in another tab" hand-over). Locking detaches and blanks them, as today. Copy/paste as in 0.2.1. |
| Activity log | Kept **in memory only** (last ~200 events), sent over the event stream, cleared on Quit. Never written to disk. Contains server names, so it is only shown while unlocked (like the rest of the data). |
| Connection details | "Connected since", reconnect count and ping are measured by the SSH engine (ping = keep-alive round trip). |
| Traffic | Counted on the PC as data passes through each tunnel: today's total and per-minute buckets for the last hour, in memory only. |
| Notes | A new `notes` field on servers, stored **inside the encrypted vault**. Older versions keep but don't show it. |
| Server health | **Opt-in per server**, stored in the vault (`healthEnabled`). Runs only while that server is already connected for something else (it never opens a connection by itself), every 30 s while the dashboard is open. One SSH exec of a fixed, read-only command (`cat /proc/loadavg /proc/meminfo /proc/uptime; nproc; df -P -k`) — no user input in the command; output size-limited and parsed strictly; Linux servers only (others show "not available"). Switching it off stops it immediately. Documented in SECURITY.md. *0.6.0 adds a second way: **Connect** on the Health box (see §14).* |
| Invariants | No change to "no local shell" or "network only to your servers": health runs a command **on your server** over the existing SSH connection. CLAUDE.md invariant 7 gets a line naming the health command as the only remote command TunnelTab runs by itself. |

### Build phases

| # | Phase | Ships in | Done when |
|---|---|---|---|
| D1 ✅ | **New layout** — sidebar, hash routes, Overview home (totals, needs attention, running now, all servers), server page with Overview / Apps / Activity tabs, in-memory activity log, connected-since + reconnect count; reordering moved into the new places; narrow-window menu | 0.3.0 | Browser test covers navigation, reload, reordering, lock/unlock; README screenshots regenerated |
| D2 ✅ | **Built-in terminals** — Terminals tab with terminal tabs, + New terminal, Split, Pop out (session hand-over), lock blanking, close-dashboard-ends-sessions | 0.3.0 | Browser test: open two terminals in-page, split, pop one out, lock/unlock, reload |
| D3 ✅ | **Ping, traffic, notes** — keep-alive RTT, per-tunnel byte counters (today + last hour chart), server notes in the vault | 0.4.0 | Unit tests (counters, vault round-trip with notes, older vault without notes); browser test |
| D4 ✅ | **Server health (opt-in)** — Settings → Server health tab with a switch per server (plus one on the server's Overview tab), the fixed read-only command, strict parser, 30 s polling only while connected and the dashboard is open | 0.4.0 | Parser tests with real `/proc` samples and hostile output (huge, malformed); test server answers the command; browser test: off by default, on, off again stops polling; SECURITY.md + SECURITY_REVIEW.md updated |

## 13. v0.5.0 — Service discovery (released 2026-10-02)

Built as proposed below (S1, S2, S4). Decisions taken: the command runs
`ss -tlnp`, `netstat -tln` and `docker ps` (three fields, JSON); ports on
loopback or all addresses tunnel to `127.0.0.1`, ports bound to one other
address are offered with that address (marked *only on …*), ports open on
all addresses are marked *open on all addresses*; recognised web apps are
ticked already. **S3 (web check)** was built in 0.7.0 and extended to
added services ("service checks": after Start, on Check, on Open when the
last check failed); see ARCHITECTURE.md → Service checks. Ports that answer
are ticked and get the protocol they answered on.

Adding services by hand means knowing each app's port. **Find services**
would look at a server, list the web apps and ports it finds, and let you
tick the ones to add — nothing is added without your choice.

### What the user sees

1. On a server's **Services** tab (and in the empty state of a new server):
   **Find services…**. It asks first: "TunnelTab will run a read-only
   command on *homelab* to list its open ports and Docker containers."
2. A list of what was found, for example:

   | Add | Name (editable) | Port | Found as |
   |---|---|---|---|
   | ☑ | n8n | 5678 | Docker container `n8n`, published on 127.0.0.1:5678 |
   | ☑ | Grafana | 3000 | Docker container `grafana` |
   | ☐ | Portainer | 9443 (https) | Docker container `portainer` |
   | ☐ | — | 5432 | listening port (PostgreSQL — not a web page) |

   Ports already added as services are shown as *already added* and can't
   be ticked again. Known apps get their usual name and protocol (n8n,
   Grafana, Portainer, Home Assistant, Uptime Kuma, Proxmox, …); unknown
   ports are named after the container or process, or left for you to name.
3. **Add selected** creates the services in one go (same validation as the
   service dialog). Nothing starts automatically.

### Decisions (as proposed; what was built is summarised above)

| Topic | Proposal |
|---|---|
| When it runs | Only when you click **Find services** and confirm — never automatically, never in the background. Unlike server health it may connect to the server (you asked for it). |
| What runs | One fixed, read-only command — the second (and only other) command TunnelTab would run on a server, next to the opt-in health check. Roughly: `ss -Htln` (listening TCP ports, no root needed) and, if available, `docker ps --format '{{json .}}'` (container names, images, published ports; works when the user may use Docker). No user input in the command; size- and time-limited output, strict parser, fuzzed — like `internal/health`. |
| Recognising apps | A built-in table of well-known images and ports → name, protocol and path (e.g. `n8nio/n8n` → n8n, http, 5678). Matched by image first, port second. |
| Checking it's a web page (optional) | For candidates, a single `HEAD /` through a short-lived SSH tunnel to tell web UIs from databases and pick http/https. Only to the server itself, only during the scan you started. Could be a later step. |
| Listening addresses | Ports on `127.0.0.1` and `0.0.0.0`/`::` are offered (both reachable through the tunnel); ports bound only to other interfaces are shown as such. |
| Privacy | Nothing leaves the PC; the scan result is shown and then forgotten (only what you add is saved, in the vault). The scan is recorded in Recent activity ("Searched for services"). |
| Non-Linux / no `ss` | Falls back to `netstat -tln`; if neither works, says so and offers the manual dialog. |

### Build phases (sketch)

| # | Phase | Done when |
|---|---|---|
| S1 | **Discovery command + parser** (`internal/discover`): `ss`/`netstat` and `docker ps` output → candidates; known-apps table | Parser tests with real outputs (Ubuntu, Debian, Alpine, no Docker, Docker without permission), hostile/huge output, fuzzing |
| S2 | **API + UI**: `POST /api/servers/{id}/discover` (on click, connects if needed), results dialog with tick boxes, editable names, *already added* marking, **Add selected** | Browser test against the dev SSH server answering the command; nothing added unless ticked |
| S3 | **Web check (optional)**: HEAD probe through the tunnel to label web UIs and pick http/https | Tests with HTTP, HTTPS and non-HTTP backends |
| S4 | **Docs + security**: CLAUDE.md invariant 7 names this command too (on click only), SECURITY.md, SECURITY_REVIEW.md, USER_GUIDE | Review done; docs match behaviour |

## 14. v0.6.0 — Health box Connect, one Servers tab (released 2026-10-02)

Two changes the user asked for after using 0.5.0:

### What the user sees

- **Health box: Connect instead of Turn on/off.** On a server's Overview
  tab, **Connect** (labelled **Show health** when the server is already
  connected for a service or terminal) connects and shows live health until
  **Disconnect** or until the dashboard is closed, whether or not the server
  is ticked in Settings. Ticked servers still show health whenever a service
  or terminal keeps them connected, and never connect just for it.
- **Settings → Servers:** the old *Servers* (fingerprints) and *Server
  health* tabs are one tab: a row per server with its project, its
  **Health** tick box and its confirmed fingerprint with **Forget**.
  Fingerprints for addresses no server uses any more are listed underneath.

### Decisions

| Topic | Decision |
|---|---|
| Keeping the connection | `sshx.Manager.Hold` / `Unhold`: a hold is one more user of the server's shared connection, in memory only, shown as `held` in the server status. `POST`/`DELETE /api/servers/{id}/connect`. Host keys are checked as for a tunnel (unknown or changed keys need confirmation). |
| Forgotten Connect | A hold is dropped on Disconnect, on server edit/delete, on lock with "close tunnels on lock", when the server can't be reconnected, and on the first 30 s health round with no dashboard open. |
| Invariants | CLAUDE.md invariant 7 reworded: health runs for ticked servers (existing connection only) or held ones (connected by the user's click); `Manager.Hold` is never called in the background. Same fixed read-only command, limits and parser. |

Tests: `TestHold` (sshx), `TestServerConnect` (server), and the browser
test (Show health → Disconnect, tick and untick in Settings → Servers).

## 15. Future — Smaller ideas (backlog)

### Progress bar while updating (released in v0.5.0)

Built as described. The *Updating* screen tells the restart by the old
session being refused, then asks the new tab's session which version runs
(no new endpoint).

Today **Update now** shows "Downloading and checking the update…" until
the restart, with no sign of how far along it is. Show real progress:

1. In Settings → Updates, a progress bar with the current step and, while
   downloading, how much is done: *Downloading 4.2 of 9.8 MB (43%)* →
   *Checking the signature* → *Installing* → *Restarting*.
2. On the *Updating TunnelTab* screen, keep showing the step (*Starting
   TunnelTab 0.5.0…*) until the new version is up, then say so — or, after
   about 30 s, explain that the previous version came back (rollback).

**How:** the install request already runs in the program; it would send
`update` events (`step`, `done`, `total` bytes) over the event stream while
it works (the download size is known from the release file). The dashboard
draws them with a native `<progress>` element. No extra network access;
same checks and limits as today.

**Done when:** a browser test (`tests/e2e/update.js`, with a slowed-down
fake release server) sees the steps in order and the bar move; docs updated.

### Recommended next (from the 2026-10-01 review)

Not scheduled; in the suggested order:

1. **Tests for `cmd/tunneltab`**, above all the update rollback (old files
   put back when the new version doesn't start).
2. **CI hardening:** pin GitHub Actions to commit SHAs; keep the signing key
   in a protected GitHub Environment that only the release workflow uses.
3. **Split `web/static/js/app.js`** into smaller modules (overview, server
   page, health, sidebar), like `discover.js` and `termview.js`.
4. **Accessibility pass:** keyboard focus order, labels and live regions
   for status changes, checked with a screen reader.
5. **Bring `docs/SECURITY_REVIEW.md` up to date** as a full review of the
   current version, not only per-feature rows.

## 16. Future — Remote desktop (VNC) (idea, not scheduled)

See a server's (or a home PC's) graphical desktop from the dashboard, over
the same SSH connection — no VNC port exposed to the internet.

### What the user sees

- A server gets a **Desktop** tab (when a desktop is set up for it): the
  remote screen in the page, with keyboard and mouse, **Fit to window** /
  **Actual size**, **Ctrl+Alt+Del**, clipboard send/receive, and **Pop out ↗**
  like terminals.
- Setting it up: in the server's settings, *Remote desktop: VNC on port
  5900* (or the display number), plus the VNC password if the server asks
  for one — stored in the encrypted vault like the other secrets.

### How (proposal)

- **Transport:** TunnelTab opens an SSH tunnel to the VNC server's port on
  the server itself (usually `127.0.0.1:5900`, so VNC never listens on the
  network), and bridges it to the page over a WebSocket — the same
  authenticated, one-time-ticket pattern as terminals.
- **Viewer:** an in-browser VNC client, most likely **noVNC** (MPL-2.0),
  vendored and embedded like xterm.js — no plugin, nothing loaded from the
  internet. This is a **new dependency**, so it needs your OK first
  (CLAUDE.md), plus its license in THIRD_PARTY_NOTICES.
- **Alternative / extra:** *Open in your VNC viewer* — start the tunnel and
  show `127.0.0.1:<port>` to paste into TightVNC/RealVNC. TunnelTab still
  wouldn't launch other programs itself (CLAUDE.md invariant 7).
- **Not covered:** Windows RDP needs a different protocol (and usually a
  gateway such as Apache Guacamole); a possible later step.

### Things to decide before building

| Topic | Question |
|---|---|
| Scope | VNC only first? Which servers will you use it with (Linux desktop, Raspberry Pi, Windows with a VNC server)? |
| Security | The VNC password is weak by design; the protection is SSH. Show a warning if the VNC port is reachable from outside? Lock blanks and disconnects the desktop, like terminals. |
| Viewer | noVNC embedded (recommended) vs. external viewer only (simpler, no new dependency). |
| CSP | noVNC draws on a canvas and uses a WebSocket to the same origin; check it needs no inline scripts (styles are already allowed on the dashboard). |

### Build phases (sketch)

| # | Phase | Done when |
|---|---|---|
| V1 | **Desktop setting + tunnel bridge**: per-server VNC port/password in the vault; WebSocket ⇄ SSH channel bridge with tickets | Tests with a fake VNC server (RFB handshake) through the test SSH server |
| V2 | **Viewer**: vendored noVNC in a Desktop tab, fit/scale, Ctrl+Alt+Del, clipboard, Pop out, blank on lock | Browser test against a fake RFB server showing a test pattern |
| V3 | **Docs + security review**, license notice | Review done; user guide explains setting up a VNC server safely (bound to localhost) |

## 17. Future — Files: browse, upload and download (idea, not scheduled)

Move files between this PC and a server without a separate program
(WinSCP, FileZilla, `scp`): browse the server's folders in the dashboard
and upload or download with a click or by dragging. It uses SFTP over the
server's existing SSH connection, so nothing new is opened to the
internet and nothing needs installing on the server (every OpenSSH server
has SFTP).

### What the user sees

- A **Files** tab on each server's page, next to Terminals. It opens in the
  login user's home folder; a path bar shows where you are (click a part to
  go up, or type a path).
- A list of the folder: name, size, modified date, permissions; folders
  first; sort by any column; **Show hidden files** switch.
- **Download**: a file goes to the browser's normal download folder; a
  selection or a folder downloads as one `.zip`, made on the fly.
- **Upload**: **Upload files…** (the browser's file picker) or drag files
  and folders from Windows Explorer / the desktop onto the list.
- **New folder**, **Rename**, **Delete** (asks first; folders say how many
  items are inside).
- A **Transfers** panel with a progress bar per file, speed, **Cancel**, and
  *Replace / Keep both / Skip* when a file already exists.
- Later, optionally, a **two-pane view** (this PC on the left, the server on
  the right) for copying back and forth — see "Local files" below.

### How (proposal)

- **SFTP client:** an SFTP session on the server's shared connection
  (`sshx.Manager`, like terminals: opening Files connects if needed, closing
  the tab lets the connection go). Either add `github.com/pkg/sftp` (the
  standard Go SFTP library; a new dependency needs your OK) or write the
  small subset needed (list, stat, open, read, write, mkdir, rename,
  remove) on top of `x/crypto/ssh`.
- **API:** `GET /api/servers/{id}/files?path=` (list), `POST …/files/mkdir`,
  `…/rename`, `…/delete`. Downloads and uploads stream through the program,
  never held in memory or written to disk on the PC (apart from the
  browser's own download). A download uses a one-time, short-lived ticket
  in the URL (like terminals), so the browser saves it natively with its
  own progress; uploads are streamed `PUT`s with the session token.
- **Events:** transfer progress over the event stream (like Update now's
  progress bar); each upload, download, rename and delete is listed in the
  server's Activity (names shown only while unlocked, like the rest).

### Things to decide before building

| Topic | Question / recommendation |
|---|---|
| Local files | **Recommended:** no browsing of this PC by TunnelTab at all — the browser's file picker, drag-and-drop and Downloads folder do it, and the dashboard API can't read local files. A two-pane view would let the program list and read local folders, a much bigger risk if the dashboard were ever tricked; if wanted, limit it to one folder you choose (e.g. `transfers/` next to the program) and never follow links out of it. |
| Security invariants | SFTP runs no commands on the server (it's the SSH "sftp" subsystem), but it **changes files there**, unlike anything TunnelTab does today. CLAUDE.md invariant 7 would name it: only on the user's click, paths only from the user's own browsing, nothing in the background. Invariant 10 (portable) is unaffected: downloads are saved by the browser. |
| Dependency | `github.com/pkg/sftp` (well known, BSD licence) vs. a minimal own client (more code to test and fuzz). |
| Limits | Maximum file size? (Streaming means none is needed for memory.) Maximum items per folder listing (e.g. 10 000, with a note)? |
| Deleting | Confirmation always; recursive folder delete only after showing the count. No "trash" on the server (SFTP has none). |
| Permissions | Show `rwx` only, or also allow **chmod**? Downloading files you can't read shows the server's "permission denied". |
| Locking | Running transfers keep going while locked (like tunnels) or stop? The file list is hidden while locked either way. |
| Editing | Later: open a small text file in an editor in the page and save it back? (Easy to add once the rest exists.) |

### Build phases (sketch)

| # | Phase | Done when |
|---|---|---|
| F1 | **SFTP + listing**: SFTP session on the shared connection; list/stat API with paths validated and cleaned; Files tab with path bar, sorting, hidden files | Tests against the in-process test SSH server with an SFTP subsystem (temp folder); fuzzed path handling; browser test browses folders |
| F2 | **Download and upload**: streamed download with one-time tickets (zip for folders), streamed upload with drag-and-drop, Transfers panel with progress, cancel and conflicts | Tests: large file, cancel mid-way, name conflicts, permission denied, connection drop during a transfer; browser test uploads and downloads a file and compares checksums |
| F3 | **Manage**: new folder, rename, delete (with counts), Activity entries | Tests per operation, including refusing to delete `/` or the home folder without a second confirmation |
| F4 | **Docs + security review**: CLAUDE.md invariant 7, SECURITY.md (what SFTP can change on a server), SECURITY_REVIEW.md, USER_GUIDE | Review done; docs match behaviour |
| F5 | *(optional)* **Two-pane view** with a local folder pane, limited to one chosen folder | Only if decided above; tests that nothing outside that folder can be listed or read |

## 18. v0.8.0 — Tray icon and "when the dashboard tab is closed" (released 2026-10-03)

The user asked (2026-10-03) for TunnelTab to sit in the system tray, so the
dashboard tab can be closed while the program keeps running, and for a
question asking whether closing the tab should quit TunnelTab or leave it
running in the tray.

### What the user sees

- **Tray icon** (notification area by the clock on Windows; Linux desktops
  with a tray: KDE, XFCE, Cinnamon, GNOME with the AppIndicator extension).
  It appears when TunnelTab starts and goes away when it quits. Tooltip:
  "TunnelTab 0.8.0".
  - **Click** → opens the dashboard in the browser, like starting
    tunneltab.exe a second time (a double-click opens one tab, not two).
  - **Right-click** menu: **Open dashboard**, **Lock now**, **Quit**.
  - Where there is no tray (GNOME without the extension, no desktop at
    all) nothing changes: no icon, no error, TunnelTab runs as before.
- **"When the dashboard tab is closed" setting** (Settings → General): **Quit
  TunnelTab** (default, chosen by the user 2026-10-03) or **Keep TunnelTab
  running** in the tray. Browsers don't let a
  page ask anything as its tab closes (only the generic "Leave site?" box,
  with fixed wording and buttons). A one-time question when the dashboard
  opened was built first, but the user preferred no question at all
  (2026-10-03): it's a setting only. A TunnelTab window of its own
  (WebView2) could ask on every close; the user chose to stay in the
  browser.
- **Quit when the tab closes:** closing the last TunnelTab page (dashboard
  tabs and popped-out terminals) quits TunnelTab after a few seconds, like
  pressing Quit. Reloading the page doesn't.

### Decisions

| Topic | Decision |
|---|---|
| Library | `fyne.io/systray` (maintained fork of getlantern/systray; BSD-3) and, on Linux only, its `github.com/godbus/dbus/v5` (BSD-2). Both build with CGO off for Windows and Linux (checked), so the build stays the same. Approved by the user with this plan. |
| Main thread | The tray must run on the program's main thread: `main` runs the tray loop and the rest of the program in a goroutine. Quitting from anywhere (dashboard, tray, Ctrl+C, Update now) ends the program the same way as today, then removes the icon. |
| Portable (invariant 10) | The library's `SetIcon` on Windows writes the icon to the system temp folder. TunnelTab instead writes `tray-icon.ico` into the data folder and passes that path. On Linux the icon is sent over D-Bus from memory. |
| No external programs (invariant 7) | godbus can launch `dbus-launch` when no session bus is known. Before starting the tray, TunnelTab connects with auto-launch off and checks that a tray (`org.kde.StatusNotifierWatcher`) is running; if not, there is no tray. |
| No network (invariant 9) | The tray makes no network calls; its menu calls the same code as the dashboard's buttons. |
| Detecting the close | `pagehide` on the dashboard sends `POST /api/closing` (fetch `keepalive`, with the session token like every request). If the setting is "quit", TunnelTab waits 2 s and quits if no TunnelTab page is connected by then (a reload reconnects in time). Without that request (a browser discarding a sleeping tab, a crash) it keeps running, so tunnels are never dropped by accident. |
| Setting | `settings.json`: `closeAction` = `"quit"` (default) or `"tray"`; `""` (and old settings files) means the default. |
| Turning it off | `--no-tray` command-line flag (also used by the browser tests). |

### Build phases

| # | Phase | Done when |
|---|---|---|
| T1 ✅ | **Tray icon**: `internal/platform` tray (Windows + Linux, no-op elsewhere), icon file, Open / Lock / Quit, main-thread loop, `--no-tray` | Builds for both; unit tests for icon generation and the menu actions; real check on Windows (user, CI zip) |
| T2 ✅ | **Quit on close**: `closeAction` setting in Settings → General, `POST /api/closing` | Server tests (quit after close, not with a page open, not when "tray"); browser test for the setting, a reload (no quit) and a real tab close (quits) |
| T3 ✅ | **Docs**: USER_GUIDE, ARCHITECTURE, SECURITY, CLAUDE.md conventions (new dependency), README, CHANGELOG; PLAN §9/§10 updated | Docs match behaviour |

## 19. v0.8.3 — Fourth review round (released 2026-10-03)

An external review of v0.8.2 found two more issues; both were confirmed in
the code. Both are fixed (85ebcd7); `go test ./...` and the browser
test pass, and both new tests fail on the old code.

| # | Issue | Fix |
|---|---|---|
| R1 (P1) | **Locking leaves old output visible in popped-out terminals.** The terminal page ignored `vault: locked` and relied on its own WebSocket to blank the screen. A terminal whose shell had ended, that was moved to another tab, or that was waiting to reconnect has no socket, so its output stayed on screen while locked. Also, an event stream that came back ("resync") resumed without asking whether TunnelTab was still locked. | `TermView.lock()` blanks every view whatever its state (closes any socket, clears the screen, shows Locked) and remembers what it was showing. `resume()` brings that back: live/disconnected views re-attach, an ended view stays ended (no new shell is opened by itself), "In another tab" stays so. terminal.js calls `lock()` on `vault: locked`, and on "resync" asks `/api/state` whether it is locked before resuming. |
| R2 (P2) | **A check still running when a service is edited or deleted stores its old result afterwards.** `dropCheck` removed the result, but the check then stored and published its own. | Each service has a change counter, raised by `dropCheck`. A check reads the counter *before* reading the service and stores and publishes its result only if the counter is unchanged, under the same lock as `dropCheck` (so its event can't arrive after the "dropped" one). A stale Check answers `{"check": null}`; the dashboard treats that as no result. |

Tests: server test with a service whose check is held open while the
service is edited / deleted (no result stored or published), plus a browser
test locking with an ended popped-out terminal.

## 20. v0.8.4 — Fifth review round (released 2026-10-03)

An external review of v0.8.3 found three issues; all were confirmed in the
code. The fix plan was reviewed too, and its corrections (counters taken
before auto-start's snapshot, tests that open the gap *before* a start is
reserved, the auto-lock test's expectation, a guard for stale resync
answers) are included. All fixed (e9b7f8d); `go test ./...` and
the browser test pass, and every new test fails on the old code.

| # | Issue | Fix |
|---|---|---|
| R1 (P1) | **Dialogs could stay over the unlock screen.** Only the `vault` event and the Lock button closed dialogs; the resync path (`route()`) and a "locked" answer in `loadData` went straight to the unlock screen, leaving e.g. an Edit server form with a pasted key on top. | One `enterLocked()` for every way in: closes (and removes) dialogs, then the unlock screen. "TunnelTab has stopped" closes dialogs too. A resync's `/api/state` answer is dropped if a vault event arrived while asking (as the terminal page does). |
| R2 (P2) | **A tunnel could start with old settings.** Auto-start reads all services, then starts them one by one; Start reads the service, then reserves it. An edit or delete in between found nothing for `StopForward` to cancel. | The v0.8.3 change counter moved to `servicever.go` and now covers starts: `serviceChanged` raises it (lock released) before `StopForward`; Start and auto-start take it before reading services (auto-start: all of them, before its snapshot) and start with `Manager.StartForwardIf`, which compares it under the manager's lock as it publishes the forward. |
| R3 (P2) | **Auto-lock could ignore activity at the timeout.** `LockIfIdle` checked under a read lock, released it, then called `Lock`. | Check and lock under one write lock (`lockHeld`); OnLock callbacks run after it is released; returns whether it locked. |

Tests: `TestLockIfIdleIsOneStep`; `TestAutoStartSkipsChangedService` (an
earlier service's handshake held while a later one is edited / deleted) and
`TestStartSkipsChangedService` (paused between reading the service and
starting); browser test: a dashboard tab with its event stream cut misses
the lock (Edit server dialog open), and a "locked" answer to its data
request — both leave no dialog over the unlock screen.

## 21. v0.8.5 — Sixth review round (released 2026-10-04)

A review of v0.8.4 confirmed the §20 fixes and found two gaps left in the
tunnel-start check (R2 of §20); both were confirmed in the code. Fixed
(368da05); `go test ./...` passes and both new tests fail on the old
code.

| # | Issue | Fix |
|---|---|---|
| R1 (P2) | **A stale Start could return the newer tunnel.** `StartForwardIf` returned an already-running forward before asking `current()`: an old Start paused across an edit and a new Start answered 200 with a link built from the old settings. | `current()` is asked first, under the manager's lock, before the "already running" return (and still again at registration). |
| R2 (P2) | **Deleting a server or project didn't invalidate its services.** Only `StopServer` ran, which can't stop a start not yet reserved; with the connection kept open by other work (a check, Find services) such a start made an orphan tunnel. | The delete collects the removed services (`servicesOn`) in the same vault update and calls `serviceChanged` for each before `StopServer`. |

Tests: `TestStaleStartDoesNotReturnNewerTunnel`;
`TestDeletingParentCancelsStart` (server and project; a check held open by
a slow app keeps the connection in use).

## 22. v0.8.6 — Seventh review round (released 2026-10-04)

A review after v0.8.5 found one more issue, and its reviews of the fix
four more; all were confirmed in the code.
Fixed (bb3fb66) together with 13442e9 (dialogs removed at once
on lock); `go test ./...` passes and every new test fails without its fix.

| # | Issue | Fix |
|---|---|---|
| R1 (P2) | **Editing a server could leave new work on its old connection.** `StopServer` stopped tunnels, shells and holds, but the pooled connection stayed while a check or Find services still used it, and `acquire` handed it to new work without looking at the new settings: Start after changing the port answered 200 over the old connection. | `StopServer` takes the connection out of the pool first (with the stop counter, under the manager's lock), so new work connects with the current settings; work still using the old one finishes and the last release closes it (closing it at once would give connects in progress a "shut down" error instead of their "cancelled" one). A change of address or login also raises the change counters of the server's services, so a check still running over the old connection doesn't keep its result. |
| R2 (P2) | **A retired connection could report on its replacement.** Found in review of the R1 fix: the old connection's last release published "server stopped" under the shared server ID while the new connection was up, so the dashboard dropped its status and health reading. | `StopServer` marks the connection retired (under a per-connection `emitMu`, before taking it out of the pool); all of a connection's own events go through `serverConn.publish`, which drops them once retired. |
| R3 (P2) | **A retired connection could reconnect and stop its replacement.** Found in review of R2: when the old transport dropped, its supervisor reconnected (with the current settings) and, on a permanent failure such as a changed password, `failServer` stopped the server's forwards and hold by ID — the replacement's. | A retired connection doesn't reconnect: when its transport drops (or if it is retired while reconnecting) the supervisor ends, and work still using it fails with `ErrNotConnected`. `failServer` takes the connection and only stops forwards on it and its own hold (`unhold(id, only)`). |
| R4 (P2) | **`failServer` could stop a replacement forward.** Found in review of R3: it checked `f.sc == sc` on a snapshot, then stopped by service ID; a forward stopped and started again in between was removed instead (and a start under way cancelled). | `stopForwardIf(id, only, …)`: under the manager's lock, stops the service's forward only if it is still the one found, and otherwise leaves its forward and any start alone (as `unhold(id, only)` does). |
| R5 (P2) | **A stopped forward's final event could land after its replacement's.** Found in review of R4: the final event was published after `f.close()`, by service ID; a replacement started during the close published "active" first, and the dashboard then dropped the running tunnel. | `Manager.fwdEvents`: every forward event is published under it. Stopping removes the forward and publishes its final event under it, before closing; registration publishes "active" under it only if the forward is still running; `emitForwards` lists forwards under it. |

Tests: `TestServerEditRetiresConnection` (a check held open by a slow app
during the edit; Start afterwards must fail on the closed new port, and
the check's result is dropped); `TestRetiredConnectionStaysQuiet` (the
replacement connects before the old work ends; no event from the old one
afterwards); `TestRetiredConnectionDoesNotReconnect` (old transport dropped
with a password that now fails: no reconnect, the replacement's tunnel and
hold stay); `TestFailServerSparesReplacedForward` (cleanup paused by
`testHookFailing` while the service is stopped and started again);
`TestStoppedForwardEventComesFirst` (stop paused by `testHookForwardRemoved`
after removal, replacement started, then the stop finishes: the last forward
event is "active").

## 23. v0.8.7 — Windows Defender false positive (released 2026-10-09)

Windows Defender quarantined the v0.8.6 `tunneltab.exe` as
`Trojan:Win32/Cloxer` while it was running; VirusTotal showed 3 of 69
(Microsoft `Wacatac.B!ml`, Trapmine, Bkav), all machine-learning guesses. The
release was verified genuine first: valid signature on `SHA256SUMS.txt`, and
the exe rebuilds byte for byte from tag v0.8.6 with the same Go version
(`GOTOOLCHAIN=go1.27.1 bash scripts/build.sh 0.8.6`).

| What | Result |
|---|---|
| Open the browser with `ShellExecute` instead of starting `rundll32` (`internal/platform/browser_windows.go`); `OpenBrowser` refuses anything but `http://127.0.0.1:<port>` (`browser.go`, `TestCheckDashboardURL`) | Microsoft's detection is gone in VirusTotal (Trapmine and Bkav remain: 2 of 36 / 2 of 71); Defender did not quarantine the new builds; the user ran the CI build on Windows for a good while without alarms |
| Keep symbols (no `-s -w`) | No difference in the scores, so the build flags stay |
| Unsigned exe | Still the strongest signal; only a code-signing certificate fixes it for good. Options to look at when wanted: SignPath Foundation (free for open source, built in CI) and Microsoft's Azure Artifact Signing (monthly fee). Either would add a signing step to `release.yml` next to the checksum signing |

**CI:** Go 1.27.2 broke `staticcheck@latest` (export data version 5 vs 4;
reproduced locally: fails on 1.27.2, passes on 1.27.1), so the staticcheck
step alone runs with `GOTOOLCHAIN: go1.27.1`. **To do:** check around
**2026-10-23** (and after each Go release) whether staticcheck works on the
newest Go, then remove the pin. Steps are in `docs/DEVELOPMENT.md` →
Security checks.

**If Defender flags a release again:** check the SHA-256, restore with
*Allow on device*, report at
<https://www.microsoft.com/en-us/wdsi/filesubmission> (Software developer).
After this release, submit the exe that way, and optionally to Trapmine and
Bkav.
