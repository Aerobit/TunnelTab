# TunnelTab

**Your servers, one click away.** A portable SSH terminal and web-UI launcher
for your own VPSs — no install, no accounts, no cloud.

> 🚧 **Status: in development, not yet released.** Tunnels, the dashboard and
> the in-browser terminal work; hardening and the first release are next
> (see [PLAN.md](PLAN.md)).

## What it does

- **SSH terminal in your browser** — click a server, get a shell in a new tab.
- **Web UI quick launch** — click *Open* on a service (n8n, Portainer, Grafana…);
  TunnelTab starts an SSH tunnel and opens the page for you.
- **Organised by project** — group servers and their services; drag to rearrange.
- **Portable** — one folder for Windows and Linux. Copy it to another PC or a
  USB stick and carry on.
- **Secure by design** — secrets live in a vault encrypted with your master
  password; server fingerprints are verified; everything stays on `127.0.0.1`.

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

1. Download the latest `tunneltab-<version>.zip` from
   [Releases](https://github.com/Aerobit/TunnelTab/releases) and unzip it anywhere.
2. Run it:
   - **Windows:** double-click `tunneltab.exe`
     (if SmartScreen appears: *More info → Run anyway*).
   - **Linux:** `./tunneltab-linux-amd64`
3. Your browser opens the dashboard. Create a master password, add a server,
   and click **Terminal ↗** or **Open ↗**.

Full instructions: [docs/USER_GUIDE.md](docs/USER_GUIDE.md).

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
TunnelTab does **not** protect against.

## Building from source

```bash
bash scripts/build.sh          # Linux/macOS/WSL
.\scripts\build.ps1            # Windows PowerShell
```

See [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md).

## License

[MIT](LICENSE). TunnelTab is the successor to the `local.browser` extension.
