# TunnelTab — instructions for AI assistants

TunnelTab is a portable SSH terminal and web-UI launcher. One Go executable
runs a local web server on 127.0.0.1 and the dashboard opens in the user's
browser. Read `docs/ARCHITECTURE.md` before changing code and `PLAN.md` for
the roadmap and what phase the project is in.

## Commands

```bash
go vet ./...                 # static checks
go test -race ./...          # all tests (must pass before every commit)
gofmt -l .                   # must print nothing
bash scripts/build.sh        # portable folder + zip in dist/ (keep build.ps1 identical)
go run ./cmd/tunneltab       # run a dev build
```

Go lives at `~/.local/go/bin` in the dev container (see `docs/DEVELOPMENT.md`).

## Security invariants — never break these

These are the reason the project exists. If a change seems to require breaking
one, stop and ask the user instead.

1. **No secret ever leaves the vault except to the SSH engine.** Passwords,
   private keys and passphrases are never sent to the browser, written to logs,
   put in error messages, placed in command-line arguments or environment
   variables, or written to disk outside `vault.enc`.
2. **The vault key is never stored.** It is derived from the master password
   (Argon2id) and held only in memory while unlocked; wipe it on lock.
3. **Vault writes are atomic** (write temp file → fsync → rename) and keep one
   backup. Never write `vault.enc` in place.
4. **Everything listens on 127.0.0.1 only** — the dashboard server and every
   port forward. Never `0.0.0.0`, `::` or an empty host.
5. **Every API request and WebSocket is authenticated** with the bearer
   session token (never a cookie: cookies for 127.0.0.1 are sent to every
   port, including tunneled web apps) and checked: `Host` must be
   `127.0.0.1:<port>` or `localhost:<port>` (DNS rebinding); state-changing
   requests need a same-origin `Origin`; cross-site `Sec-Fetch-Site` is
   refused. No CORS headers, ever. Never log launch links or tokens.
6. **Host keys are verified** against the keys confirmed in the vault
   (`model.Data.KnownHosts`, never a plain-text file). Unknown keys need
   explicit user confirmation, and the key stored is exactly the one shown
   (`UnknownHostKeyError.Key`); changed keys are blocked. Never use
   `ssh.InsecureIgnoreHostKey()`, even in tests (use `sshtest`).
7. **No local shell is invoked.** SSH is done in-process with
   `golang.org/x/crypto/ssh`. The only external programs launched are the
   browser opener (`internal/platform`) and, after "Update now", TunnelTab's
   own program (`cmd/tunneltab/restart.go`), with arguments passed as a
   slice. When the vault locks, terminal pages are detached and blanked (the shells
   keep running); re-attaching needs the vault unlocked.
8. **IDs are generated server-side** and all input is validated in
   `internal/model` before use.
9. **No network access except SSH to the user's servers** — with one
   exception: updates (`internal/update`), and only when the user clicks.
   "Check for updates" asks GitHub's release API; it only links to
   TunnelTab's release pages. "Update now" downloads that release's files
   from TunnelTab's GitHub releases and installs them **only** if
   `SHA256SUMS.txt` is signed with the release key (`ReleasePublicKey`)
   and lists the zip for exactly that version with a matching SHA-256.
   Never add automatic checks or downloads, never skip or weaken these
   checks, and never replace anything but the program files (never the
   data folder). The web UI loads nothing from the internet:
   all assets (including xterm.js) are vendored in `web/static/` and
   embedded. Strict CSP, no inline scripts.
10. **Portable mode:** all data lives in the data folder (default `data/` next
    to the executable). Never write to the user's home directory, the registry
    or system locations.

## Conventions

- **Go:** standard library first; minimal, well-known dependencies only
  (in use: `golang.org/x/crypto`, `github.com/coder/websocket`; frontend:
  vendored xterm.js). Ask before adding others.
- All data changes go through `vault.Update` (which validates and saves
  atomically); never write `vault.enc` or `settings.json` directly — use
  `internal/atomicfile`.
- New secret fields must be tagged `secret:"true"` and hidden in
  `internal/model/public.go`.
- Errors: wrap with context (`fmt.Errorf("unlock vault: %w", err)`); never
  include secret values.
- **Logs** (unencrypted, in the data folder): IDs and `sshx.ErrorKind(err)`
  only — never hostnames, addresses, fingerprints, tokens, secrets or raw
  SSH errors. `TestLogsContainNoSecretsOrAddresses` must keep passing.
- Per-OS code goes in `internal/platform` using build tags / `_windows.go`,
  `_linux.go` files.
- **Frontend:** vanilla JS (ES modules) + CSS. No frameworks, no bundlers, no
  inline `<script>`, `on*=` or `style=` attributes (the CSP blocks them), no
  `innerHTML` — build DOM with `h()` from `web/static/js/dom.js`. After any
  dashboard change, run the browser test (`tests/e2e`, see DEVELOPMENT.md).
- **Security checks:** staticcheck, govulncheck and the fuzz tests (see
  docs/DEVELOPMENT.md → Security checks). Keep `testdata/fuzz/` inputs.
- **Tests:** every package has tests; SSH tests use the in-process test SSH
  server in `internal/sshx/sshtest`, never a real host. Security checks (auth, Host/Origin, redaction)
  have their own tests.
- **Docs:** when behaviour or structure changes, update `docs/ARCHITECTURE.md`
  (and `USER_GUIDE.md` / `SECURITY.md` if user-visible or security-relevant)
  and add a line to `CHANGELOG.md` under Unreleased.

## Git

- Never commit or push without the user's explicit approval in chat.
- Never commit `data/`, `dist/`, keys, vaults or real hostnames/credentials
  (tests use fake values).
