# Changelog

All notable changes to TunnelTab are recorded here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and versions follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Fixed

- Locking now also hides a popped-out terminal whose shell had ended, that
  had moved to another tab or that was reconnecting; before, its last output
  stayed readable while TunnelTab was locked. After unlocking, an ended
  terminal stays ended rather than opening a new shell.
- A service check that was still running when the service was edited or
  deleted no longer brings back its old result afterwards.

## [0.8.2] - 2026-10-03

### Fixed

- Saving no longer accepts a vault too large to open again (over 64 MiB):
  the save is refused with a message, and the vault and its backup are left
  as they were.
- Opening a terminal no longer hangs when the server never answers the
  request for a session: it gives up after the connect timeout, or at once
  if the server is edited, deleted or TunnelTab quits.
- Quitting now also closes connections that were still being set up or used
  (a terminal being opened, Find services), and no terminal can open after.
- Terminals opened at the same moment can no longer exceed the limit of 32.
- With `--data` pointing elsewhere, a relative key path such as
  `keys/id_ed25519` again means "in the TunnelTab folder", as documented,
  rather than next to the data folder.

## [0.8.1] - 2026-10-03

### Fixed

- Service checks (Check, Start, Open and Find services) no longer wait past
  their 4-second limit when the server doesn't answer the request to open a
  connection to the service; such a port now shows as not answering.
- Find services no longer merges different services that listen on the same
  port at different addresses, and no longer suggests 127.0.0.1 for a
  service that listens only on another loopback address such as 127.0.0.2
  (its tunnel would have gone to the wrong place). The address is shown
  whenever it isn't 127.0.0.1.
- Locking can no longer leave a terminal showing or accepting typing: a page
  that was attaching just as TunnelTab locked is hidden too, and a hidden
  page's last keystrokes no longer reach the shell.
- A dashboard that was still loading when TunnelTab locked no longer replaces
  the unlock screen with the dashboard.
- Stopping a tunnel (or deleting its service, or locking with "close tunnels
  on lock") no longer hangs when the server never answers a request to reach
  the service.
- Stopping, editing or deleting a service while its tunnel is still starting
  now cancels the start; before, the tunnel came up anyway once connected.
- Unlocking in a second tab while already unlocked no longer brings back an
  older copy of the vault (an edit made meanwhile could be lost on the next
  save), and it still checks the password.
- Closing a terminal (or any other action) that finishes after TunnelTab
  locked no longer brings the dashboard back over the unlock screen.
- Locking while an unlock is still under way now wins: that unlock asks you
  to try again instead of unlocking with an older copy of the vault.
- Editing or deleting a server while Connect or a new terminal is still
  connecting to it now cancels them, instead of leaving a connection or a
  terminal to the old server.
- Server health and Find services now give up after their time limit even
  when the server never answers the request to run the command.
- Notes typed while earlier notes are being saved are no longer marked as
  saved (and then replaced on the next redraw).
- If an update can't start, the rollback now also restores a TunnelTab
  program that was renamed (e.g. to `myssh.exe`), and the clean-up removes
  its `.old` copy.

## [0.8.0] - 2026-10-03

### Added

- **Tray icon.** TunnelTab now shows an icon in the system tray while it runs
  (Windows, and Linux desktops with a tray). Click it to open the dashboard;
  right-click it for Open dashboard, Lock now and Quit TunnelTab. Where there
  is no tray, nothing changes. `--no-tray` turns it off.
- **Settings → General → When the dashboard tab is closed.** By default
  TunnelTab now quits a few seconds after the last TunnelTab tab closes (a
  reload doesn't count); choose "Keep TunnelTab running" to leave it running
  in the tray instead. (Before, closing the tab always left it running.)
- When TunnelTab quits (from the tray, Ctrl+C or closing the last tab), open
  TunnelTab tabs now show "TunnelTab has stopped" instead of reconnecting to
  the next copy started.

## [0.7.4] - 2026-10-02

### Security
- **An old "Trust this server?" question can no longer replace a newer
  key.** If a key was confirmed or replaced for a server (for example in
  another tab) while an earlier question was still open, answering that
  earlier question used to replace the newer key without the "server
  identity changed" warning. Now that answer is refused: the question
  closes and TunnelTab connects again, so you get the question that fits
  the key confirmed now (or it just connects). Answering the same question
  in two tabs no longer saves the vault twice, which replaced its backup.

### Fixed
- **Keyboard focus stays put.** When something changed in the background
  (a health reading, a service check, a change from another tab), the
  dashboard redrew itself and the keyboard focus jumped back to the top of
  the page. It now stays on the button or field you were on, also when a
  button changes (Start becomes Stop).

## [0.7.3] - 2026-10-02

### Fixed
- **Update now finishes even if quitting hangs.** If shutting down took
  more than 15 seconds, the previous version used to exit without starting
  the new one or checking that it starts, and the backup of the previous
  version was then deleted on the next start. Now it still starts the new
  version and puts the previous one back if the new one doesn't start.
- **Only one TunnelTab can use a data folder.** It now locks the folder
  while it runs. Before, two copies could open the same vault (for example
  when `instance.json` was missing, as it briefly is during Update now) and
  the last one to save overwrote the other's changes. A second start now
  opens the running copy's dashboard, waiting up to 20 seconds if that copy
  is still starting or quitting.
- **Update now rolls back more reliably.** The previous version now waits
  until the new one has opened your vault and shows its dashboard (before,
  it only waited for the new version to write `instance.json`, which
  happens earlier). The new version keeps the previous files until that
  check is done, so they can always be put back. On Windows, a rollback
  now waits for the stopped new version to exit and retries restoring the
  files while Windows still holds them.

## [0.7.2] - 2026-10-02

### Fixed
- **Quit now really stops TunnelTab and its tunnels.** A web UI with an open
  connection that it never closes (such as a websocket) made Quit hang, so
  TunnelTab kept running in the background and its tunnels stayed usable.
  Stopping a tunnel now closes such connections at once, and TunnelTab exits
  after at most 15 seconds even if something else hangs.

## [0.7.1] - 2026-10-02

### Added
- **Find services shows its progress**: a bar and the current step
  (connecting, listing ports and containers, then "checking whether each
  app answers (3 of 8)") instead of a dialog that seems to hang.

## [0.7.0] - 2026-10-02

### Added
- **Service checks: does the app answer?** Starting a tunnel now checks
  that the app behind it answers, and the service shows **✓ App answers**,
  **✗ Nothing answers on port …**, **✗ Answers, but not as a web page**, or
  that it answers on https instead of http (or the other way round). A
  **Check** button checks again; **Open** re-checks when the last check
  failed and says so. One `HEAD` request through the SSH connection, only
  after a click, never in the background.
- **Find services checks what it finds**: each port that may be a web app
  is checked; those that answer are ticked and set to http or https as they
  answered, those that don't are left unticked.
- **Update now from test builds**: the zips GitHub Actions builds are now
  named after the last release (e.g. `0.6.1-3-g652f29e`) instead of `ci`,
  and Settings → Updates offers them **Update now** to the next official
  release, with the same signature checks. Going back to the release a
  test build was made after is never offered.

## [0.6.1] - 2026-10-02

### Changed
- The green dot next to the TunnelTab name in the sidebar is gone. It only
  showed that the dashboard could reach the TunnelTab program; when it
  can't, the red "Lost connection" banner already says so.

### Fixed
- The tabs on a server's page (Overview, Services, Terminals…) no longer
  show a small scrollbar on Windows.
- **Overview → Running now** no longer cuts the server name, address and
  traffic short on a normal-width window: each row shows what runs and
  where, with the address and today's traffic on a second line.

## [0.6.0] - 2026-10-02

### Changed
- **Settings: one Servers tab.** The Servers and Server health tabs are
  combined: each server has one row with its Health tick box and its
  fingerprint (with Forget). Fingerprints for addresses no server uses any
  more are listed underneath.
- **Health box: Connect instead of Turn on.** On a server's page, **Connect**
  (**Show health** if it's already connected) connects and shows the
  server's health until you click **Disconnect** or close the dashboard,
  whether or not health is ticked in Settings. Ticking a server in
  **Settings → Servers** still shows it whenever a service or terminal is
  open; that is now the only on/off switch.

## [0.5.0] - 2026-10-02

### Added
- **Find services**: on a server's **Services** tab, **Find services…**
  lists the web apps the server runs, so you don't need to know their
  ports. After you confirm, TunnelTab runs one read-only command on the
  server (`ss` or `netstat`, and `docker ps`) and shows what it found:
  well-known apps (n8n, Grafana, Portainer, Home Assistant and many more)
  get their usual name and protocol and are ticked already; ports that
  already have a service are marked; databases and other non-web ports are
  listed separately. Only the services you tick are added. It runs only
  when you click, never by itself.
- **Update progress**: **Update now** shows a progress bar with each step
  (checking the signature, *Downloading 4.2 of 9.8 MB (43%)*, unpacking,
  installing). After the restart, the old tab says whether the new version
  is running or the previous one came back.

## [0.4.3] - 2026-10-01

### Fixed
- When the connection to a server dropped and came back, a terminal stayed
  ended until you pressed Enter. It now reconnects by itself: the terminal
  keeps the connection, which reconnects in the background like a
  tunnel's, and a new shell starts in the same terminal as soon as the
  server can be reached again, below the old output, ready for typing.
  While it waits, the terminal and the server show *Reconnecting…* (with
  the last reason in grey), not *Failed*. Closing the terminal stops the
  reconnecting.

## [0.4.2] - 2026-10-01

### Fixed
- Auto-lock never locked while a dashboard tab was open, even if nobody
  used it: the background server-health round (every 30 s) counted as
  activity. Background work (health checks, reconnects) no longer postpones
  auto-lock; only clicks, typing and other things you do count.

## [0.4.1] - 2026-10-01

### Changed
- Terminals have an even 5 px margin on every side, in the terminal's own
  colour (it showed as a black frame before).

### Fixed
- On Windows, a server on the same network could show no ping ("—")
  because the clock measured its round trip as 0. It now shows "under 1 ms".

## [0.4.0] - 2026-10-01

### Fixed
- The bottom line of a terminal (often the one with the cursor) was partly
  cut off; 0.3.1's extra padding made it worse. Rows now always fit inside
  the terminal, with room at the bottom.

### Added
- **Server health** (opt-in, off by default): CPU load, memory, disk use and
  uptime on each server's page. Switch it on or off per server in
  **Settings → Server health** or on the server's page. While on, and only
  while the server is connected and the dashboard is open, TunnelTab runs
  one fixed, read-only command on it every 30 seconds. It never connects
  just for this.
- **Ping**: each connected server shows its round-trip time (from the
  keep-alive TunnelTab already sends), in the Overview's server table and on
  the server's page.
- **Traffic**: bytes through each tunnel, counted on your PC. Today's total
  on the Overview and next to each running service, and a chart of the
  last hour on the server's page. Kept in memory only.
- **Server notes**: a **Notes** tab on each server for free text (backup
  times, where the passwords are…), stored in the encrypted vault and
  shown on the server's Overview tab.

## [0.3.1] - 2026-10-01

### Changed
- The **Apps** tab is now called **Services**, matching **+ Service** and the
  service dialogs.
- A little more room around the text in terminals, especially at the bottom.

### Fixed
- TunnelTab could auto-lock while you were moving between pages of the new
  dashboard, because those clicks didn't count as activity. Clicks, typing
  and scrolling in the dashboard now postpone auto-lock.

## [0.3.0] - 2026-10-01

### Changed
- **New dashboard layout.** A sidebar lists your projects and servers with
  their status. **Overview** (the home page) shows how many servers are
  online, tunnels running and terminals open, anything that needs
  attention, everything running now, recent activity and all servers.
  Each server has its own page with **Overview**, **Apps**, **Terminals** and
  **Activity** tabs. The address remembers the page, so reload and Back work. On narrow
  windows the sidebar opens from a Menu button.

### Added
- **Terminals inside the dashboard.** **+ New terminal** opens a shell in the
  server's **Terminals** tab: several per server, **Split** for two side by
  side, and **Pop out ↗** to move one to its own browser tab (and **Bring
  back here**). They keep running while you look at other pages, through
  locking and reloads, and end when the dashboard tab closes.
- Each server shows how long it has been connected and how often it
  reconnected.
- **Recent activity**: connections, tunnels and terminals since TunnelTab
  started. Kept in memory only; gone when TunnelTab quits.

## [0.2.1] - 2026-10-01

### Changed
- Server rows are tidier: **Test connection** and **Edit server** moved into
  a **⋯** menu, next to **Terminal** and **+ Service**.
- **Settings** is split into tabs: General, Password, Updates, Servers.
- After **Check for updates** finds a newer version, a blue dot on
  **Settings** reminds you (it still never checks by itself).
- The logo inside the app no longer has a dark square behind it.
- Clearer unlock error: "Wrong master password. Check Caps Lock and your
  keyboard layout."

### Fixed
- The "TunnelTab has stopped", "Not signed in" and "Updating" screens are
  centred in a card like the unlock screen, and the page background no
  longer shows a darker band at the top or scrolls for no reason.

## [0.2.0] - 2026-10-01

### Added
- **Update now** (Settings → Check for updates): downloads the new version,
  checks that it's signed by TunnelTab, replaces the program files and
  restarts. Nothing happens unless you click; your data folder is never
  touched; if the new version can't start, the previous one comes back.
  Updating *to* 0.2.0 from 0.1.0 is still done by hand.
- Releases are signed: the release workflow publishes `SHA256SUMS.txt.sig`,
  an Ed25519 signature of `SHA256SUMS.txt`.

### Changed
- **Terminal copy and paste work like Windows Terminal:** Ctrl+C copies
  when text is selected (otherwise it still stops the running command),
  Ctrl+V pastes, and right-click copies the selection. A short *Copied*
  message confirms it.

### Fixed
- Ctrl+Shift+C in a terminal no longer opens Firefox's Inspector.

## [0.1.0] - 2026-09-30

First release: a portable SSH terminal and web-UI launcher for your own
servers. Successor to the `local.browser` browser extension.

### Added
- **Portable app** — one folder with `tunneltab.exe` (Windows 10/11 x64) and
  `tunneltab-linux-amd64`, sharing a `data` folder. Nothing to install; the
  dashboard opens in your normal browser. Starting it again while it runs
  opens a new dashboard tab.
- **Encrypted vault** — projects, servers, services, keys, passwords and
  confirmed fingerprints in one file, encrypted with your master password
  (Argon2id + AES-256-GCM). Auto-lock after inactivity, password change,
  crash-safe saves with a backup copy.
- **Dashboard** — projects → servers → services, drag to move servers, live
  status, settings, lock and quit. First-run setup with a strength meter.
- **SSH connections** — login with an SSH agent (Windows OpenSSH agent or
  Linux), a key stored in the vault, a key file or a password; one shared
  connection per server; keep-alives and automatic reconnection.
- **Server fingerprints** — confirmed on first connection before anything is
  sent; a changed fingerprint blocks the connection with a clear warning.
- **Web UI quick launch** — "Open" starts an SSH tunnel on `127.0.0.1` and
  opens the app; stable automatic local ports; optional auto-start after
  unlocking.
- **In-browser terminal** — "Terminal" opens a real SSH shell in a new tab
  (xterm.js): resize, copy/paste, exit status. Sessions survive locking
  (the tab is blanked; programs keep running) and page reloads, and
  re-attach with their recent output. Closing the tab ends the session.
- **Reordering** — drag projects, servers and services by their ⠿ grip (or
  focus it and press ↑/↓); drop a server into another project to move it.
- **Check for updates** — a button in Settings asks GitHub for the latest
  release and links to it. Manual only: TunnelTab never checks by itself.
- **Packaging** — Windows icon and version details, license notices,
  SHA-256 checksums, and a warning when started from inside the ZIP.
- **Documentation** — user guide with troubleshooting and FAQ, architecture,
  security model, security review, development and release guides.

### Security
- The dashboard only listens on `127.0.0.1`, signs in with one-time links
  and a per-origin session token (no cookies), and refuses cross-site,
  cross-origin and DNS-rebinding requests; strict Content Security Policy.
- Secrets never reach the browser, logs, command lines or environment
  variables; logs contain no server addresses.
- The data folder is restricted to your user account; wrong master
  passwords are rate-limited; locking blanks and disconnects terminal tabs.
- Reviewed before release: see `docs/SECURITY_REVIEW.md`. Fuzz tests,
  staticcheck, govulncheck and a browser end-to-end test run in CI.

[Unreleased]: https://github.com/Aerobit/TunnelTab/compare/v0.8.2...HEAD
[0.8.2]: https://github.com/Aerobit/TunnelTab/releases/tag/v0.8.2
[0.8.1]: https://github.com/Aerobit/TunnelTab/releases/tag/v0.8.1
[0.8.0]: https://github.com/Aerobit/TunnelTab/releases/tag/v0.8.0
[0.7.4]: https://github.com/Aerobit/TunnelTab/releases/tag/v0.7.4
[0.7.3]: https://github.com/Aerobit/TunnelTab/releases/tag/v0.7.3
[0.7.2]: https://github.com/Aerobit/TunnelTab/releases/tag/v0.7.2
[0.7.1]: https://github.com/Aerobit/TunnelTab/releases/tag/v0.7.1
[0.7.0]: https://github.com/Aerobit/TunnelTab/releases/tag/v0.7.0
[0.6.1]: https://github.com/Aerobit/TunnelTab/releases/tag/v0.6.1
[0.6.0]: https://github.com/Aerobit/TunnelTab/releases/tag/v0.6.0
[0.5.0]: https://github.com/Aerobit/TunnelTab/releases/tag/v0.5.0
[0.4.3]: https://github.com/Aerobit/TunnelTab/releases/tag/v0.4.3
[0.4.2]: https://github.com/Aerobit/TunnelTab/releases/tag/v0.4.2
[0.4.1]: https://github.com/Aerobit/TunnelTab/releases/tag/v0.4.1
[0.4.0]: https://github.com/Aerobit/TunnelTab/releases/tag/v0.4.0
[0.3.1]: https://github.com/Aerobit/TunnelTab/releases/tag/v0.3.1
[0.3.0]: https://github.com/Aerobit/TunnelTab/releases/tag/v0.3.0
[0.2.1]: https://github.com/Aerobit/TunnelTab/releases/tag/v0.2.1
[0.2.0]: https://github.com/Aerobit/TunnelTab/releases/tag/v0.2.0
[0.1.0]: https://github.com/Aerobit/TunnelTab/releases/tag/v0.1.0
