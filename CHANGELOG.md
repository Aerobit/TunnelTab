# Changelog

All notable changes to TunnelTab are recorded here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and versions follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

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

[Unreleased]: https://github.com/Aerobit/TunnelTab/compare/v0.3.1...HEAD
[0.3.1]: https://github.com/Aerobit/TunnelTab/releases/tag/v0.3.1
[0.3.0]: https://github.com/Aerobit/TunnelTab/releases/tag/v0.3.0
[0.2.1]: https://github.com/Aerobit/TunnelTab/releases/tag/v0.2.1
[0.2.0]: https://github.com/Aerobit/TunnelTab/releases/tag/v0.2.0
[0.1.0]: https://github.com/Aerobit/TunnelTab/releases/tag/v0.1.0
