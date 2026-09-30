# Architecture

How TunnelTab is put together, and where to make changes.

> **Living document.** Sections marked *(planned)* describe the design from
> [PLAN.md](../PLAN.md) and are replaced with the real details as each phase
> is built. Current phase: **0 — scaffold complete**.

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
| `cmd/tunneltab` | Entry point: flags (`--version`, `--data`), startup, shutdown | Scaffold |
| `internal/config` | Portable paths, `settings.json`, rotated logs | *(planned, Phase 1)* |
| `internal/vault` | Master-password KDF, encrypted file format, atomic save, auto-lock | *(planned, Phase 1)* |
| `internal/model` | Project / Server / Service types, ID generation, validation | *(planned, Phase 1)* |
| `internal/sshx` | Connection pool, auth, known_hosts, forwards, PTY sessions, reconnect | *(planned, Phase 2)* |
| `internal/server` | HTTP server, session auth, Host/Origin checks, API, events, terminal WS | *(planned, Phase 3)* |
| `internal/platform` | Open browser, launch system terminal, single instance | *(planned, Phases 3 & 5)* |
| `web` | Embeds `web/static/` into the binary (`web.Files`) | Scaffold |
| `scripts/mkzip` | Build helper: zips the portable folder | Done |

Each package has a `doc.go` describing its job in more detail.

## Data folder

```
data/
├── vault.enc        all projects, servers, services and secrets (encrypted)
├── vault.enc.bak    previous version, kept by every save
├── known_hosts      confirmed server host keys (OpenSSH format)
├── settings.json    non-secret preferences
└── logs/            rotated logs; never contain secrets
```

## Data model *(planned, Phase 1)*

| Type | Fields |
|---|---|
| Project | `id, name, description, order` |
| Server | `id, projectId, name, host, port, username, auth` |
| Server auth | `type`: `agent` \| `keyFile` \| `keyVault` \| `password` (+ key path / key / passphrase / password as applicable) |
| Service | `id, serverId, label, remoteHost (default 127.0.0.1), remotePort, localPort (or auto), protocol (http\|https), path, autoStart` |

## Vault file format *(planned, Phase 1)*

To be documented when implemented: header (format version, KDF parameters,
salt, nonce) followed by the AES-256-GCM ciphertext of the JSON payload.

## HTTP API and events *(planned, Phase 3)*

To be documented when implemented: endpoints, request/response shapes, event
types sent to the dashboard, and the terminal WebSocket protocol.

## Startup and shutdown *(planned, Phase 3)*

To be documented: single-instance detection, launch token → cookie flow,
Quit handling, closing tunnels on exit.

## Where to change what

| I want to… | Look in |
|---|---|
| Change the build or packaging | `scripts/build.sh`, `scripts/build.ps1`, `packaging/` |
| Change CI or releases | `.github/workflows/` |
| Add a command-line flag | `cmd/tunneltab/main.go` |
| Change the dashboard look | `web/static/` *(from Phase 4)* |
| Add a field to servers/services | `internal/model`, then the API in `internal/server`, then `web/static` |
| Change encryption parameters | `internal/vault` — bump the format version and keep reading old files |
| Support another terminal program | `internal/platform` |

This table grows as the code is written.
