# TunnelTab user guide

> 🚧 **Being written alongside the app.** The troubleshooting and FAQ
> sections arrive with the first release.

## Contents

1. [Installing](#installing)
2. [First run: creating your master password](#first-run-creating-your-master-password)
3. [Unlocking and locking](#unlocking-and-locking)
4. [Projects](#projects)
5. [Adding a server](#adding-a-server)
6. [Choosing how to log in: keys, agent or password](#choosing-how-to-log-in-keys-agent-or-password)
7. [Confirming a server's fingerprint](#confirming-a-servers-fingerprint)
8. [Opening a terminal](#opening-a-terminal)
9. [Services and web UI quick launch](#services-and-web-ui-quick-launch)
10. [Settings](#settings)
11. [Backups and moving to another PC](#backups-and-moving-to-another-pc)
12. [Quitting](#quitting)
13. [Troubleshooting](#troubleshooting)
14. [FAQ](#faq)

## Installing

1. Download `tunneltab-<version>.zip` from the
   [Releases page](https://github.com/Aerobit/TunnelTab/releases).
2. Unzip it anywhere you like — your Documents folder, a USB stick, etc.
   You'll get a `tunneltab` folder.
3. Start it:
   - **Windows:** double-click `tunneltab.exe`. If Windows shows
     *"Windows protected your PC"*, click **More info → Run anyway** (the
     program isn't code-signed).
   - **Linux:** open a terminal in the folder and run `./tunneltab-linux-amd64`.

Nothing is installed on your computer; to remove TunnelTab, delete the folder.

## First run: creating your master password

The first time TunnelTab opens, it asks you to choose a **master password**.
It encrypts everything TunnelTab stores — servers, keys, passwords and
confirmed fingerprints — in `data/vault.enc`.

- Use at least 8 characters; a passphrase of several words is best. The
  meter shows roughly how strong it is.
- **It cannot be recovered.** If you forget it, delete the `data` folder and
  start again (your servers' own passwords and keys are unaffected).

## Unlocking and locking

Each time TunnelTab starts, enter your master password to unlock it.

- **Lock** (top bar) locks immediately. TunnelTab also locks by itself after
  15 minutes without use (change this in Settings).
- While locked, running tunnels keep working. If a connection drops while
  locked, it waits and reconnects after you unlock. To close all tunnels on
  lock instead, turn on *Close all tunnels when TunnelTab locks* in Settings.
- After several wrong passwords you have to wait a little before trying
  again (up to 30 seconds).

## Projects

Projects group your servers — for example *Personal*, *Client A*,
*Production*. Use **+ Project** in the top bar; **Edit** on a project renames
it or deletes it (deleting also deletes its servers and services).

Drag a server onto another project to move it.

## Adding a server

On a project, click **+ Server** and fill in:

| Field | Example |
|---|---|
| Name | Web VPS |
| Host | `vps.example.com` or `203.0.113.10` |
| SSH port | `22` |
| Username | `root` |
| Log in with | see [Choosing how to log in](#choosing-how-to-log-in-keys-agent-or-password) |

When you save, TunnelTab connects once to check everything. The first time,
it shows the server's fingerprint for you to confirm (see below). A green
dot next to a server means it's connected; **Test** checks it again at any
time.

## Choosing how to log in: keys, agent or password

Each server uses one login method. From most to least recommended:

| Method | What you provide | Good to know |
|---|---|---|
| **SSH agent** | Nothing — your key is already loaded in an agent | Windows: enable the *OpenSSH Authentication Agent* service and run `ssh-add`. Linux: the usual `ssh-agent` (`SSH_AUTH_SOCK`). Nothing secret is stored in TunnelTab. Not portable: the agent lives on each PC. |
| **Key stored in TunnelTab** | Paste your private key (and its passphrase, if it has one) | Most portable: the key travels inside the encrypted vault. |
| **Key file** | Path to a private key file (and its passphrase, if any) | A relative path such as `keys/id_ed25519` means "inside the TunnelTab folder", so you can keep the key next to the app on a USB stick. |
| **Password** | The server password | Works everywhere, but keys are safer. Stored only inside the encrypted vault. |

When editing a server, leave a password/key/passphrase field **blank to keep
the saved one**. Switching to a different login method deletes the old
method's saved secrets.

Supported key types: Ed25519 (recommended), ECDSA and RSA, in OpenSSH or PEM
format — the same files the `ssh` command uses.

## Confirming a server's fingerprint

Every SSH server has a *host key* that proves it's really your server. The
first time you connect, TunnelTab shows its **fingerprint**, for example:

```
ssh-ed25519 SHA256:uNiVztksCsDhcc0u9e8BujQXVUpKZIDTMczCvj3tD2s
```

- **Check it** against your VPS provider's console, or run
  `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` on the server, then
  confirm. Your password or key is **not sent** until you do.
- TunnelTab remembers the key (inside the encrypted vault).
- If a remembered server ever shows a **different** key, TunnelTab refuses to
  connect and warns you. That happens legitimately when a server is
  reinstalled — but it's also exactly what an attacker intercepting your
  connection would look like. Only replace the key if you know why it changed.

## Opening a terminal

Click **Terminal ↗** on a server. A new browser tab opens with a shell on
that server, logged in the same way as its tunnels (and reusing the same
connection when one is open). Open as many as you like, side by side.

- **Copy:** select text, then **Ctrl+Shift+C**. **Paste:** **Ctrl+Shift+V**
  (or right-click → Paste). Plain **Ctrl+C** stops the running command, as
  in any terminal.
- The terminal resizes with the window.
- When the session ends (you typed `exit`, the connection dropped, or
  TunnelTab locked), the top bar says why. Press **Enter** or click
  **Reconnect** to start a new session.
- **Locking TunnelTab closes all terminals**, so nobody can use an open shell
  on an unattended PC. Unlock, then reconnect.

## Services and web UI quick launch

A **service** is a web app (or any TCP port) running on your server, such as
n8n, Portainer or Grafana. On a server, click **+ Service**:

| Field | Meaning |
|---|---|
| Name | What you call it, e.g. *n8n* |
| Remote host | Where the app listens, as seen from the server — usually `127.0.0.1`. Use a Docker container name or internal IP if the app runs elsewhere. |
| Remote port | The app's port on the server, e.g. `5678` |
| Local port | The port on your PC. Leave blank for a stable automatic port, or pick one (e.g. `5678`) |
| Protocol | `http` or `https` — how the browser should open it |
| Path | Added to the address, e.g. `/admin` |
| Start automatically | Start this tunnel every time you unlock |

Then:

- **Open ↗** starts the tunnel if needed and opens the app in a new tab.
- **Start** / **Stop** controls the tunnel without opening anything.
- While running, the service shows its address (e.g. `localhost:23720`) —
  click it to open the app again. The label shows *Running*,
  *Reconnecting…* or *Waiting for unlock*.

Tunnels only listen on your own PC (`127.0.0.1`); other devices on your
network can't use them.

*"The local port is already in use"* means another program uses that port:
choose another local port, or leave it blank.

## Settings

**Settings** (top bar):

- **Lock after inactivity** — Never, 5 min … 4 hours (default 15 min).
- **Close all tunnels when TunnelTab locks** — off by default.
- **Dashboard port** — default 47811; applies after restarting TunnelTab.
- **Change master password** — enter the current one and the new one twice.
- **Confirmed servers** — every fingerprint you've trusted. **Forget**
  removes one, so the next connection asks you to confirm again.

## Backups and moving to another PC

Everything TunnelTab saves is in the `data` folder inside the `tunneltab`
folder, encrypted with your master password. To move to another PC, or to back
up, copy the whole `tunneltab` folder. *(More detail in Phase 7.)*

## Quitting

Use **Quit** in the dashboard. This closes all tunnels and stops TunnelTab.
Closing the browser tab does **not** stop it: your tunnels keep running.

To get back to the dashboard after closing the tab, just start TunnelTab
again: it notices it's already running and opens the dashboard in a new tab.

## Troubleshooting

*(Phase 7)*

## FAQ

*(Phase 7)*
