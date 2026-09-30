# TunnelTab — Project Plan

> Status: **v0.1.0 released** (2026-09-30). All phases complete; later ideas are listed under Out of scope.
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
- No system tray icon in v1 (adds per-OS complexity); the app runs until **Quit**.
- Dashboard is a browser tab, not a native window — by design, so no extra runtime is bundled.

## 10. Out of scope for v1 (possible later)

System tray icon · macOS build · Linux ARM build · "Open in system terminal" (would need TunnelTab's host-key checks and vault keys handed to an external ssh) · jump hosts / ProxyJump · SOCKS proxy mode · SFTP file browser · import from local.browser or `~/.ssh/config` · code signing.

## 11. v0.2.0 — "Update now" (planned)

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
| U1 | **Release signing** — signing tool (`scripts/` or `internal/devtools`), release workflow signs `SHA256SUMS.txt`, key setup documented in `docs/RELEASE_CHECKLIST.md` / `DEVELOPMENT.md` | A test tag produces `SHA256SUMS.txt.sig` that verifies with the committed public key |
| U2 | **Download + verify** (`internal/update`) — fetch assets, size/time limits, signature + hash check, safe unzip | Tests with a fake release server: good release accepted; bad signature, wrong hash, wrong host, oversized, older version, path-traversal zip all rejected |
| U3 | **Install + restart** — rename/swap, relaunch, wait for port, cleanup of `.old`, rollback on failed start | Tests on Linux in the container; you test on Windows with a real 0.2.0 → 0.2.1 update |
| U4 | **UI** — Update now button, confirm dialog listing open terminals/tunnels, progress and errors | `tests/e2e` walkthrough with the fake release server |
| U5 | **Docs + security** — update CLAUDE.md invariant ("never downloads or runs anything" → "only on click, only signed releases"), SECURITY.md, SECURITY_REVIEW.md, USER_GUIDE (Updates + FAQ), ARCHITECTURE (API `POST /updates/install`), CHANGELOG | Docs match behaviour; security review of the new code done |
