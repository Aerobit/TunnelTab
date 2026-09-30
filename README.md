# TunnelTab

**Your servers, one click away.** A portable SSH terminal and web-UI launcher
for your own VPSs — no install, no accounts, no cloud.

[![CI](https://github.com/Aerobit/TunnelTab/actions/workflows/ci.yml/badge.svg)](https://github.com/Aerobit/TunnelTab/actions/workflows/ci.yml)
[![Latest release](https://img.shields.io/github/v/release/Aerobit/TunnelTab?sort=semver)](https://github.com/Aerobit/TunnelTab/releases/latest)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

## What it does

- **SSH terminal in your browser** — click a server, get a shell in a new tab.
- **Web UI quick launch** — click *Open* on a service (n8n, Portainer, Grafana…);
  TunnelTab starts an SSH tunnel and opens the page for you.
- **Organised by project** — group servers and their services; drag (or use
  the keyboard) to reorder them.
- **Portable** — one folder for Windows and Linux. Copy it to another PC or a
  USB stick and carry on.
- **Secure by design** — secrets live in a vault encrypted with your master
  password; server fingerprints are verified; everything stays on `127.0.0.1`.
- **Log in your way** — SSH agent, a key stored in the vault, a key file, or a
  password.
- **Stays up** — tunnels reconnect by themselves after network drops or sleep,
  and auto-lock keeps an unattended PC safe.

## Screenshots

<p align="center">
  <img src="docs/images/dashboard.png" alt="TunnelTab dashboard: projects with servers and their web apps; two tunnels running" width="820">
</p>

| | |
|---|---|
| ![In-browser SSH terminal](docs/images/terminal.png) | ![Confirming a new server's fingerprint](docs/images/fingerprint.png) |
| **Terminal** — a real SSH shell in a browser tab | **Fingerprint check** — nothing is sent until you confirm the server |
| ![Adding a server](docs/images/add-server.png) | ![Settings](docs/images/settings.png) |
| **Add a server** — agent, stored key, key file or password | **Settings** — auto-lock, master password, confirmed servers |
| ![First run](docs/images/setup.png) | ![Unlock](docs/images/unlock.png) |
| **First run** — choose a master password | **Unlock** — everything stays encrypted until you do |

<sub>Screenshots use made-up demo data (see [docs/images](docs/images/README.md)).</sub>

## Quick start

**Needs:** Windows 10/11 or Linux (64-bit) and any modern browser. Nothing to
install on your PC or your servers.

1. Download the latest `tunneltab-<version>.zip` from
   [Releases](https://github.com/Aerobit/TunnelTab/releases/latest) and
   **extract** it anywhere (Documents, a USB stick…).
2. Run it:
   - **Windows:** double-click `tunneltab.exe`
     (if SmartScreen appears: *More info → Run anyway* — it isn't code-signed).
   - **Linux:** `./tunneltab-linux-amd64`
3. Your browser opens the dashboard. Create a master password, add a server,
   and click **Terminal ↗** or **Open ↗**.

Full instructions, troubleshooting and FAQ: [docs/USER_GUIDE.md](docs/USER_GUIDE.md).

## How it works

```
Browser tab  ──localhost only──▶  tunneltab (one executable)  ──SSH──▶  your VPS
 dashboard                         vault · SSH engine · tunnels
```

TunnelTab is a single Go program with its own SSH client, so it doesn't need
OpenSSH, sshpass, Node.js or a browser extension. Details:
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Security

Credentials are encrypted with a key derived from your master password
(Argon2id + AES-256-GCM) and never leave the app. The dashboard server only
listens on `127.0.0.1` and rejects requests from other websites. Read
[docs/SECURITY.md](docs/SECURITY.md) for the full threat model — including what
TunnelTab does **not** protect against — and
[docs/SECURITY_REVIEW.md](docs/SECURITY_REVIEW.md) for the pre-release review.

## Building from source

You need [Go](https://go.dev/dl/) 1.27 or newer.

```bash
bash scripts/build.sh          # Linux/macOS/WSL
.\scripts\build.ps1            # Windows PowerShell
```

Both produce `dist/tunneltab/` and `dist/tunneltab-<version>.zip`. See
[docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) for tests, the browser test and
how releases are made. Contributions and AI-assisted edits follow
[CLAUDE.md](CLAUDE.md).

## License

[MIT](LICENSE). Bundled third-party software (Go, x/crypto, x/sys,
coder/websocket, xterm.js) is listed with its licenses in
`THIRD_PARTY_NOTICES.txt` inside every release.

TunnelTab is the successor to the `local.browser` browser extension.
