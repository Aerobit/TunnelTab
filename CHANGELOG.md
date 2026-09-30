# Changelog

All notable changes to TunnelTab are recorded here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and versions follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Security
- Logs no longer record server addresses, tunnel targets or connection
  error text — only IDs and error types (enforced by a test).
- The data folder is restricted to the current user (Windows access list,
  Linux 0700).
- Password checks run one at a time; changing the master password is
  rate-limited like unlocking.
- Key files must be regular files; folder paths no longer list files;
  idle timeout for dashboard connections.
- Fuzz tests for validation, the vault parser and the API; staticcheck and
  govulncheck in CI. Review recorded in docs/SECURITY_REVIEW.md.

### Fixed
- Typing in a terminal now counts as activity, so auto-lock doesn't close
  a terminal in use.

### Added
- In-browser SSH terminal: "Terminal ↗" opens a tab with xterm.js over a
  WebSocket (one-time 30-second tickets), sharing the server's connection;
  resize, copy/paste, reconnect, exit status; all terminals close when
  TunnelTab locks.
- `github.com/coder/websocket` dependency; xterm.js 6 vendored.
- README screenshots (`docs/images/`), generated with demo data by
  `tests/e2e/readme-shots.js`; `fakessh` gained `-port` and `-demo`.
- Dashboard (`web/static`): first-run setup with strength meter, unlock with
  rate-limit countdown, projects/servers/services with drag-to-move, live
  tunnel status, one-click Open, fingerprint confirmation (with an explicit
  warning flow for changed keys), settings (auto-lock, master password,
  confirmed servers), lock and quit.
- `POST /api/hostkeys/forget` to remove a confirmed fingerprint.
- `fakessh` development server and a Playwright browser walkthrough
  (`tests/e2e`), also run in CI.

### Changed
- Editing a server only restarts its tunnels when the address, username or
  login changes (a rename keeps them running).
- Local web server and JSON API (`internal/server`): one-time launch links,
  bearer-token sessions, Host/Origin/Sec-Fetch-Site checks, strict security
  headers, unlock rate limiting, host-key confirmation flow, live event
  stream, settings, and Quit.
- Program startup (`cmd/tunneltab`): portable data folder, logging,
  single instance (a second launch opens the running dashboard),
  `--no-browser`, `--port`, clean shutdown.
- `internal/platform`: open the browser, error dialogs on Windows,
  instance file.
- SSH engine (`internal/sshx`): one shared connection per server, login by
  password (incl. keyboard-interactive), key file, vault-stored key or
  ssh-agent (Linux socket / Windows OpenSSH agent), host-key confirmation
  and change detection, 127.0.0.1-only port forwards with stable auto
  ports, keep-alives, reconnection with back-off, and pausing while the
  vault is locked.
- Confirmed host keys are stored in the encrypted vault instead of a
  plain-text known_hosts file.
- In-process test SSH server (`internal/sshx/sshtest`).
- Encrypted vault (`internal/vault`): Argon2id + AES-256-GCM, authenticated
  header, atomic saves with a backup copy, password change, auto-lock.
- Data model (`internal/model`): projects, servers (agent / key file / vault
  key / password login), services; validation, cascading deletes, and a
  public view that never includes secrets.
- Portable data folder, `settings.json` and rotating logs (`internal/config`).
- Crash-safe file writes (`internal/atomicfile`).
- Project scaffold: Go module, package layout, build scripts for Windows and
  Linux, CI and release workflows, documentation skeletons.
