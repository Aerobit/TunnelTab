# Security review — v0.1 (pre-release)

*Date: 2026-09-30 · Scope: the whole repository at the end of Phase 6.*

This records what was checked before the first release, what was found and
fixed, and which risks are accepted. The threat model itself is in
[SECURITY.md](SECURITY.md).

## Method

- **Manual review** of every package: vault and crypto, data model and
  validation, SSH engine (auth, host keys, forwards, terminals), local web
  server (authentication, request checks, API, events, WebSocket), startup,
  platform code, and the dashboard JavaScript.
- **Automated checks**
  - `staticcheck` — no findings.
  - `govulncheck` — no vulnerabilities in code TunnelTab calls. (It lists
    `golang.org/x/crypto/openpgp`, a package in a dependency that TunnelTab
    doesn't use.)
  - `npm audit` on the test tools — none (they aren't shipped).
  - **Fuzzing** — server/service validation (~11 million inputs), the vault
    file parser (~7.5 million), and API request bodies (~18,000 full
    requests). No crashes, no unsafe value accepted, no server errors.
  - **Tests** — unit and integration tests for every package, including
    security tests (Host/Origin/Sec-Fetch-Site checks, one-time tokens,
    rate limiting, secrets never in API responses, secrets and addresses
    never in logs), a browser walkthrough, and the race detector in CI.
    Security tests were checked to fail when the protection is removed.

## Findings fixed in this review

| # | Finding | Impact | Fix |
|---|---|---|---|
| 1 | Log files recorded server addresses, tunnel targets and connection error text | Someone copying the data folder could learn which servers you use (logs aren't encrypted) | Logs record IDs and error *types* only (`sshx.ErrorKind`); a test runs every action and fails on any secret, token, fingerprint, address or port in the log |
| 2 | On Windows the data folder inherited its parent's permissions | On a shared drive, other Windows users could read `instance.json` and request a dashboard login link | The data folder gets a protected access list: current user + SYSTEM only (Linux: mode 0700). Tested on Windows in CI |
| 3 | Typing in a terminal didn't count as activity | Auto-lock could close a terminal in the middle of work | Terminal input postpones auto-lock |
| 4 | Parallel unlock attempts all ran before the first failure set the delay; change-password had no delay | Faster password guessing for someone who already has a dashboard session | Password checks run one at a time; change-password uses the same back-off |
| 5 | A key-file path could point at a pipe or device | Opening it could hang a connection attempt | Only regular files are read |
| 6 | Folder paths listed their files | Minor information exposure | Folder paths return 404 |
| 7 | No idle timeout on dashboard connections | Minor resource use | 2-minute idle timeout |

## Found and fixed during development

| Finding | Fix (phase) |
|---|---|
| After a password change, `vault.enc.bak` still opened with the old password | Backup re-written under the new password (1) |
| A plain-text `known_hosts` file would reveal which servers are used | Confirmed host keys stored inside the vault (2) |
| A session cookie would be sent to tunneled web apps (cookies for 127.0.0.1 are shared by all ports) | Bearer token in a per-origin store instead (3) |
| A slow dashboard tab could stop receiving events forever | Resync slot always reserved (3) |
| Host-key confirmation could store a different key than the one shown | The server keeps the exact key; the browser only sends a token (3) |
| A redundant pop-up message and unclear terminal end reasons | UI fixes (4, 5) |

## Changed after the review

| Change | Reason |
|---|---|
| Locking detaches and blanks terminal pages instead of closing the shells | Closing killed running work (e.g. a `docker pull`) found in manual testing. Pages still can't read or type while locked, and re-attaching needs the vault unlocked; a detached session is closed if no page returns within a minute of unlocking. |

## Accepted risks

| Risk | Why it's accepted |
|---|---|
| Decrypted secrets are Go strings and can't be wiped from memory while unlocked | Only reachable by code already running as you (see SECURITY.md → not protected against malware). The vault key itself is wiped on lock. |
| `/terminal.html` allows inline *styles* | xterm.js needs them. Scripts stay `'self'`-only; terminal output is drawn as text, not HTML. |
| Other accounts on the *same* PC can connect to open tunnels on 127.0.0.1 | Inherent to local port forwarding; TunnelTab is designed for single-user PCs (documented). |
| FAT32/exFAT drives have no permissions | The folder can't be restricted there; the vault stays encrypted. A warning is logged. |
| The Windows ssh-agent connection is only compiled in CI, not exercised | Needs a manual check on Windows before release (Phase 7 checklist). |
| The executable is not code-signed | Windows SmartScreen warns; signing costs money. Documented in the README. |

## Re-running the checks

See [DEVELOPMENT.md → Security checks](DEVELOPMENT.md#security-checks).
