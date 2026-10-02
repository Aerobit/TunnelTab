# TunnelTab user guide

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

**Requirements:** Windows 10 or 11 (64-bit), or 64-bit Linux, and any modern
browser (Chrome, Edge, Firefox, Brave…). Nothing else — no OpenSSH, Node.js
or browser extension.

1. Download `tunneltab-<version>.zip` from the
   [Releases page](https://github.com/Aerobit/TunnelTab/releases).
   *(Optional)* check it against `SHA256SUMS.txt` from the same page:
   `Get-FileHash tunneltab-<version>.zip` (PowerShell) or
   `sha256sum -c SHA256SUMS.txt` (Linux).
2. **Extract** the ZIP anywhere you like — Documents, a USB stick, a synced
   folder. On Windows: right-click → **Extract All…**. You'll get a
   `tunneltab` folder containing:

   | File | |
   |---|---|
   | `tunneltab.exe` | the Windows program |
   | `tunneltab-linux-amd64` | the Linux program |
   | `README.txt` | a short getting-started note |
   | `LICENSE.txt`, `THIRD_PARTY_NOTICES.txt` | licenses |
   | `data\` | created on first run: your encrypted vault, settings and logs |

3. Start it:
   - **Windows:** double-click `tunneltab.exe`. If Windows shows
     *"Windows protected your PC"*, click **More info → Run anyway** (the
     program isn't code-signed).
   - **Linux:** open a terminal in the folder and run `./tunneltab-linux-amd64`
     (if it says *permission denied*, run `chmod +x tunneltab-linux-amd64` once).

   Your browser opens the dashboard.

> **Don't run it from inside the ZIP.** Windows lets you double-click the
> `.exe` inside a ZIP, but then it runs a temporary copy and your data would
> be lost. TunnelTab detects this and asks you to extract the ZIP first.

Nothing is installed on your computer. To remove TunnelTab, delete the folder.

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

- **Lock** (bottom of the sidebar) locks immediately. TunnelTab also locks by itself after
  15 minutes without use (change this in Settings).
- While locked, running tunnels keep working. If a connection drops while
  locked, it waits and reconnects after you unlock. To close all tunnels on
  lock instead, turn on *Close all tunnels when TunnelTab locks* in Settings.
- After several wrong passwords you have to wait a little before trying
  again (up to 30 seconds).

## The dashboard

- **Sidebar** (left): **Overview**, then your projects and their servers. A
  dot shows each server's connection (green connected, amber reconnecting or
  waiting, red failed), with a short note such as *2 running*. Settings,
  Lock and Quit are at the bottom. On a narrow window the sidebar opens from
  the **☰ Menu** button.
- **Overview**: how many servers are online, tunnels running, terminals
  open and traffic today; anything that **needs attention** (a server reconnecting, for
  example); everything **running now**, with Stop and Open; **recent
  activity**; and a table of **all servers** with their status and ping.
- **A server's page** (click it in the sidebar) has tabs:
  - **Overview**: the connection (connected for how long, ping,
    reconnects, address, login method), its services, its **health** (after
    **Connect**, or if you ticked it in Settings), the traffic through its
    tunnels in the last hour, and your notes.
  - **Services**: start, stop, open, edit and reorder its web apps, or
    let TunnelTab [find them](#find-services).
  - **Terminals**: terminals on this server (see [Opening a terminal](#opening-a-terminal)).
  - **Activity**: what happened with this server since TunnelTab started.
  - **Notes**: free text about the server (backup times, where the
    passwords are…). **Save notes** or Ctrl+S. Notes are stored inside the
    encrypted vault, like the server's login details.

The address bar remembers which page you're on, so reloading the tab (or
the browser's Back button) brings you back to it.

**Ping and traffic.** Ping is the round trip of the small "keep-alive"
message TunnelTab already sends to a connected server every 30 seconds.
Traffic is counted on your PC as data passes through each tunnel: today's
total and a chart of the last hour. Like recent activity, both are kept in
memory only: they start from zero when TunnelTab starts and are gone when
it quits. Nothing extra is sent to your servers to measure them.

### Server health

A server's **Health** box shows its CPU load, memory, disk use and uptime.
There are two ways to see it:

- **Connect** in that box (**Show health** if the server is already
  connected) connects to the server and shows its health until you click
  **Disconnect** or close the dashboard. The connection then closes, unless
  a service or terminal still uses it.
- To see it whenever a service or terminal is open, tick the server in
  **Settings → Servers** (off by default). Then it never connects
  just for this: it shows only while the server is already connected.

Either way, and only while the dashboard is open, TunnelTab runs one
read-only command on the server every 30 seconds:
`cat /proc/loadavg /proc/meminfo /proc/uptime; nproc; df -P -k`. The
readings are never saved. It needs a Linux server; on others the box says
so.

## Projects

Projects group your servers — for example *Personal*, *Client A*,
*Production*. Use **+ Project** at the bottom of the sidebar. The **⋯** button
next to a project's name has **Add server** and **Edit project** (rename
or delete it; deleting also deletes its servers and services).

**Reordering.** Every project and server in the sidebar, and every app on a
server's **Services** tab, has a ⠿ grip on its left:

- **Drag** the grip to move the item; a blue line shows where it will go.
  Drop a server onto another project (or between that project's servers) to
  move it there.
- Or **click the grip and press ↑ / ↓** to move it one place at a time.

Services can be reordered within their server. The order is saved in the
vault.

## Adding a server

Use **Add server** in the project's **⋯** menu (or **+ Add a server** under
an empty project) and fill in:

| Field | Example |
|---|---|
| Name | Web VPS |
| Host | `vps.example.com` or `203.0.113.10` |
| SSH port | `22` |
| Username | `root` |
| Log in with | see [Choosing how to log in](#choosing-how-to-log-in-keys-agent-or-password) |

When you save, TunnelTab connects once to check everything. The first time,
it shows the server's fingerprint for you to confirm (see below). A green
dot next to a server means it's connected. The **⋯** button on a server's
page has **Test connection** (checks it again at any time) and **Edit server**.

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

Click **+ New terminal** on a server's page (or **Terminal** in the
Overview's server table). A shell on that server opens in the server's
**Terminals** tab, logged in the same way as its tunnels (and reusing the
same connection when one is open).

- **Several terminals:** each one gets a tab (*Terminal 1*, *Terminal 2*…);
  **+ New** opens another, **×** closes one (and ends what runs in it).
- **Split** shows two terminals side by side.
- **Pop out ↗** moves the terminal to its own browser tab: the same session,
  carrying on where it was. The dashboard then says *open in another tab*;
  **Bring back here** moves it back.
- Terminals keep running while you look at other servers or the Overview,
  and come back after reloading the dashboard. Closing the dashboard tab
  ends them a few seconds later, like closing a terminal window.

- **Copy:** select text with the mouse, then press **Ctrl+C** or
  right-click. *Copied* appears briefly. (**Ctrl+Shift+C** and
  **Ctrl+Insert** work too.)
- **Paste:** **Ctrl+V**, or right-click with nothing selected → **Paste**.
  (**Ctrl+Shift+V** and **Shift+Insert** work too.)
- With **nothing selected, Ctrl+C stops the running command**, as in any
  terminal. This works like Windows Terminal.
- The terminal resizes with the window.
- **Locking TunnelTab hides terminals but keeps them running.** Terminals go
  blank and say *Locked* — nobody can read them or type into them — while
  your commands (an update, a `docker pull`…) carry on on the server. After
  you unlock, they reconnect by themselves and show what happened.
- A popped-out terminal tab reconnects to the same session when reloaded.
  Closing it leaves the terminal running in the dashboard (**Bring back
  here**), as long as the dashboard tab is open.
- **If the connection to the server drops, the terminal reconnects by
  itself.** The terminal and the server show *Reconnecting…* (not
  *Failed*) for as long as the server can't be reached; the server page
  shows the last reason in grey. When it's back, a new shell starts in the
  same terminal, below the old output, ready to type in. The commands that
  were running in the old shell are gone; see the point after next.
  Closing the terminal while it reconnects stops the reconnecting (unless
  a tunnel still uses the server).
- When a session ends (you typed `exit`), a bar above it says why. Press
  **Enter** or click **New session**.
- For jobs that must survive even a dropped connection (hours-long
  upgrades, big transfers), run them inside `tmux` or `screen` on the server,
  as with any SSH client.

## Services and web UI quick launch

A **service** is a web app (or any TCP port) running on your server, such as
n8n, Portainer or Grafana. On the server's page, open the **Services** tab and
click **+ Service** (or let TunnelTab [find them](#find-services)):

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
- **Check** asks whether the app behind the service answers (see below).
- While running, the service shows its address (e.g. `localhost:23720`) —
  click it to open the app again. The label shows *Running*,
  *Reconnecting…* or *Waiting for unlock*.

Tunnels only listen on your own PC (`127.0.0.1`); other devices on your
network can't use them.

*"The local port is already in use"* means another program uses that port:
choose another local port, or leave it blank.

**Does the app answer?** When you start a tunnel, TunnelTab checks that the
app behind it answers, and shows the result next to the service:

- **✓ App answers** — all good.
- **✗ Nothing answers on port …** — the app isn't running, or the remote
  port is wrong.
- **✗ Answers, but not as a web page** — something else uses that port.
- **Answers on https, not http** (or the other way round) — edit the
  service and change its protocol, or the page won't load.

Click **Check** to check again at any time. If the last check failed,
**Open ↗** checks again too and tells you if the app still doesn't answer.
The check is one request for the app's front page (a `HEAD` request,
the way a browser asks "are you there?"), sent through the SSH connection
like the tunnel itself. It never logs in or changes anything, and runs only
after you click Start, Open, Check or Find services — never in the
background.

### Find services

Don't know an app's port? On the **Services** tab click **Find services…**.
TunnelTab asks first, then connects to the server and runs one read-only
command that lists its open ports and Docker containers (`ss` or
`netstat`, and `docker ps`). Nothing on the server is changed.

You get a list of what it found:

- TunnelTab then **checks each port** that may be a web app: those that
  answer like a web page show **✓ answers** and are **ticked already**,
  set to http or https the way they answered; those that don't are
  unticked (**✗ didn't answer**).
- Web apps TunnelTab recognises (n8n, Grafana, Portainer, Home Assistant,
  Uptime Kuma, Jellyfin, Proxmox and many more) get their usual name.
  Other ports are named after their Docker container or program, if the
  server says.
- Ports that already have a service say **Already added**.
- Databases, SSH and other ports that aren't web pages are under **Not web
  pages**, unticked.
- *Open on all addresses* means the port can be reached from outside the
  server too, unless a firewall blocks it. Through TunnelTab you don't
  need that: binding the app to `127.0.0.1` is safer.

Change names or the protocol if you like, tick what you want and click
**Add selected**. Only ticked services are added; the list itself isn't
saved. If Docker is installed but your user may not use it, containers
aren't named (adding the user to the `docker` group shows them). The search
is listed in the server's **Activity**.


## Settings

**Settings** (bottom of the sidebar) has four tabs:

- **General**
  - **Lock after inactivity** — Never, 5 min … 4 hours (default 15 min).
  - **Close all tunnels when TunnelTab locks** — off by default.
  - **Dashboard port** — default 47811; applies after restarting TunnelTab.
- **Password** — change the master password: enter the current one and the
  new one twice.
- **Updates** — shows your version. **Check for updates** asks GitHub for
  the latest release and, if there's a newer one, links to its release notes
  and offers **Update now**. A blue dot on **Settings** then reminds you
  while this dashboard tab stays open. TunnelTab never checks or downloads
  by itself — only when you click. See [How do I update](#faq).
- **Servers** — one row per server: tick **Health** to see its health
  whenever a service or terminal is open (see [Server health](#server-health)),
  and the fingerprint you trusted for it. **Forget** removes a fingerprint,
  so the next connection asks you to confirm again. Fingerprints no server
  uses any more are listed underneath.

## Backups and moving to another PC

Everything TunnelTab saves is in the `data` folder inside the `tunneltab`
folder:

| File | Contents |
|---|---|
| `vault.enc` | projects, servers, services, keys, passwords and confirmed fingerprints — encrypted with your master password |
| `vault.enc.bak` | the previous version of the vault (also encrypted) |
| `settings.json` | auto-lock, dashboard port and similar preferences (nothing secret) |
| `logs/` | technical logs — no secrets, no server addresses |

- **To move or back up TunnelTab**, quit it, then copy the whole `tunneltab`
  folder. Copies are safe to keep in cloud storage or on a USB stick: without
  your master password the vault is useless.
- **The same folder works on Windows and Linux** — the data is shared by both
  programs.
- **If `vault.enc` is ever damaged**, TunnelTab refuses to open it rather than
  loading bad data. Quit, rename `vault.enc.bak` to `vault.enc` and start
  again (you lose only the last change).
- Keys used by the **Key file** login method are separate files — copy them
  too, or keep them inside the `tunneltab` folder and use a relative path such
  as `keys/id_ed25519`.

## Quitting

Use **Quit** in the dashboard. This closes all tunnels and stops TunnelTab.
Closing the browser tab does **not** stop it: your tunnels keep running.

To get back to the dashboard after closing the tab, just start TunnelTab
again: it notices it's already running and opens the dashboard in a new tab.

## Troubleshooting

**"Not signed in — start TunnelTab again"**  
Dashboard links work once and only for two minutes. Start TunnelTab again
(while it's running) to get a fresh link.

**Nothing happens when I start TunnelTab / no browser opens**  
It may already be running with the dashboard in another window, or your
system has no default browser. Look at `data/logs/tunneltab.log`. On Linux
you can also run `./tunneltab-linux-amd64 --no-browser` to print the link.

**"TunnelTab is running from a temporary folder"**  
You opened it from inside the ZIP. Extract the ZIP first (see
[Installing](#installing)).

**Windows protected your PC (SmartScreen)**  
Click **More info → Run anyway**. The program isn't code-signed (signing
costs money); it's built openly from this repository by GitHub Actions, and
you can check the download against `SHA256SUMS.txt`.

**"Wrong master password"**  
Check Caps Lock and your keyboard layout. After several wrong tries you
have to wait a few seconds. If you're sure the password is right, see
*damaged vault* under [Backups](#backups-and-moving-to-another-pc).

**"Login failed: the server rejected the username or credentials"**  
Check the username and password or key. For keys, make sure the matching
public key is in `~/.ssh/authorized_keys` on the server.

**"The private key is encrypted: enter its passphrase"**  
Edit the server and fill in **Key passphrase**.

**"Can't reach ssh-agent" / the agent login fails**  
Windows: open *Services*, set **OpenSSH Authentication Agent** to
*Automatic*, start it, then run `ssh-add` in a terminal. Linux: make sure
`ssh-agent` is running and `SSH_AUTH_SOCK` is set before starting TunnelTab.

**"WARNING: the host key … has changed"**  
TunnelTab refused to connect because the server's identity changed. If you
reinstalled or rebuilt the server, that's expected: confirm the new
fingerprint (tick "I know why"). Otherwise, don't connect — check with your
VPS provider first.

**"The local port is already in use"**  
Another program uses that port. Edit the service and pick another *Local
port*, or leave it blank for an automatic one.

**A service opens but the page doesn't load**  
The tunnel works but nothing answers on the server at that *Remote host* and
*Remote port*. Check the app is running (e.g. `docker ps` in a terminal) and
the port is right. For apps in Docker, the remote host is often `127.0.0.1`
if the port is published, or the container's name/IP otherwise.

**Tunnels show "Reconnecting…"**  
The connection to the server dropped (network change, laptop sleep, server
restart). TunnelTab keeps retrying and recovers by itself.

**Tunnels show "Waiting for unlock"**  
A connection dropped while TunnelTab was locked. Unlock it and they
reconnect.

**My terminal went blank and says "Locked"**  
TunnelTab locked (manually or after inactivity). Your session and anything
running in it keep going; unlock in the dashboard and the terminal comes
back by itself with its output. Typing counts as activity, so an active
terminal keeps TunnelTab unlocked.

**My terminal session ended**  
The connection to the server dropped (network change, server restart), or
the terminal tab was closed. Programs started in it were
stopped by the server; use `tmux` or `screen` for jobs that must survive
that. Press **Enter** for a new session.

**"Could not restrict the data folder to this user" in the log**  
The folder is on a drive without permissions (FAT32/exFAT USB stick).
TunnelTab works normally and the vault stays encrypted, but other accounts
on the same PC could read the folder's files.

## FAQ

**Is this safe?**  
Your secrets are encrypted with your master password (Argon2id +
AES-256-GCM) and never leave the program; server identities are checked;
the dashboard only accepts requests from itself. See [SECURITY.md](SECURITY.md)
for exactly what it protects against — and what it doesn't.

**Can I use it on several PCs?**  
Yes. Copy the folder, or keep it on a USB stick or in a synced folder. Don't
run two copies of the *same* folder at the same time from different PCs
(e.g. via cloud sync): the last one to save wins.

**Does TunnelTab connect to anything besides my servers?**  
Only when you click **Check for updates** or **Update now** in Settings: it
then asks GitHub for the latest release, or downloads it. Those are ordinary
web requests (GitHub sees your IP address, nothing about your servers).
Otherwise it contacts only your own servers.

**Does it need an account, a server component or Tailscale?**  
No. It talks SSH directly to your servers, like the `ssh` command does.

**Which servers work?**  
Any server you can reach with SSH: VPSs, home servers, Raspberry Pis, NAS
boxes. Nothing needs to be installed on the server.

**Why does it open in my browser instead of its own window?**  
So it doesn't need to bundle a browser engine: the program stays small and
portable, and you get your browser's tabs, zoom and password-free comfort.

**Can other devices on my network use my tunnels?**  
No. Tunnels only listen on your own PC (`127.0.0.1`).

**I forgot my master password.**  
There's no way to recover it — that's what keeps a stolen copy useless.
Delete the `data` folder and start again; your servers' own passwords and
keys are unaffected.

**How do I update TunnelTab without losing my servers?**  
Everything you saved is in the `data` folder; updates only replace the
program files.

**The easy way (0.2.0 and later):** Settings → **Check for updates** →
**Update now**. TunnelTab downloads the new version, checks that it's
signed by TunnelTab (anything else is refused), replaces its program files
and restarts in a new tab. A progress bar shows each step and how much is
downloaded; afterwards the old tab says whether the new version is running. Unlock with your master password as usual.
Running tunnels and open terminals close during the restart. If the new
version can't start, the previous one comes back by itself.

**By hand** (e.g. from 0.1.0, which has no **Update now**):

1. **Quit** TunnelTab (Quit button).
2. Back up the `data` folder (copy it anywhere — it's encrypted).
3. Extract the new ZIP somewhere temporary.
4. Copy the new `tunneltab.exe`, `tunneltab-linux-amd64` and `.txt` files
   into your existing `tunneltab` folder, replacing the old ones. Leave
   `data` alone (the ZIP doesn't contain one, so nothing overwrites it).
5. Start TunnelTab and unlock with the same master password.

(Or copy your old `data` folder into the new `tunneltab` folder and use
that.) If a new version changes the vault format, it upgrades it
automatically and the changelog says so. If anything looks wrong, quit and
put your backup `data` folder back.
