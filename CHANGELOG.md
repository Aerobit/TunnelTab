# Changelog

All notable changes to TunnelTab are recorded here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and versions follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
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
