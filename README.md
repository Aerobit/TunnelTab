# TunnelTab

**Your servers, one click away.** A portable SSH terminal and web-UI launcher
for your own VPSs — no install, no accounts, no cloud.

> 🚧 **Status: early development.** The project scaffold is in place; the app
> itself is being built in phases (see [PLAN.md](PLAN.md)). Nothing below works
> yet — this README describes the finished v1.

## What it does

- **SSH terminal in your browser** — click a server, get a shell in a new tab.
- **Web UI quick launch** — click *Open* on a service (n8n, Portainer, Grafana…);
  TunnelTab starts an SSH tunnel and opens the page for you.
- **Organised by project** — group servers and their services; drag to rearrange.
- **Portable** — one folder for Windows and Linux. Copy it to another PC or a
  USB stick and carry on.
- **Secure by design** — secrets live in a vault encrypted with your master
  password; server fingerprints are verified; everything stays on `127.0.0.1`.

## Quick start

1. Download the latest `tunneltab-<version>.zip` from
   [Releases](https://github.com/Aerobit/TunnelTab/releases) and unzip it anywhere.
2. Run it:
   - **Windows:** double-click `tunneltab.exe`
     (if SmartScreen appears: *More info → Run anyway*).
   - **Linux:** `./tunneltab-linux-amd64`
3. Your browser opens the dashboard. Create a master password, add a server,
   and click **Terminal** or **Open**.

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
