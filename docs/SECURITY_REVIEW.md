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
| "Check for updates" button (Settings) | Requested feature. Contacts GitHub only when clicked (tested: no request otherwise); response size-limited; links restricted to TunnelTab's release pages. |
| "Update now" (Settings, 0.2.0) | Requested feature. Runs only when clicked, after a confirmation. Downloads only from TunnelTab's GitHub release URLs; size- and time-limited. Installs only if `SHA256SUMS.txt` has a valid Ed25519 signature from the release key built into the program, lists `tunneltab-<exactly that version>.zip`, and the zip's SHA-256 matches (tests: other key, zip swapped, checksums edited, older release relabelled, missing signature, oversized files; all refused with nothing changed). Only the fixed package file names inside the zip's `tunneltab/` folder are extracted, so zip paths can't escape; the data folder is never touched. Only newer release versions; dev builds can't update. Old files are kept as `.old` until the new version has started, and put back if it doesn't start within 30 s (browser test covers both). The private key is a GitHub Actions secret used only in the release workflow, which refuses to publish if it doesn't match the built-in public key. |
| Terminal sessions end ~10 s after their tab closes | A closed tab must not leave a shell running. The page's open event stream marks it as present (browsers don't throttle open connections in background tabs the way they throttle timers). |
| Terminals inside the dashboard (0.3.0) | They belong to the dashboard tab: its event stream carries a random per-tab ID (validated: 16–64 URL-safe characters), and sessions opened with that ID are kept while that stream is open and end ~10 s after it closes. Same attach rules as before: one-time 30 s tickets, Origin checked, refused while locked; locking still detaches and blanks them. The ID only decides how long a session lives, never who may use it (every API call still needs the session token). |
| Server health (0.4.0) | Requested feature, opt-in per server (stored in the vault, off by default). The only command TunnelTab runs on a server by itself: the constant `health.Command` (reads /proc, nproc, df; changes nothing). `Manager.RunIfConnected` only uses an existing connection (tested: no login happens), with a 10 s timeout and a 64 KiB output limit; `health.Parse` is strict (ranges checked, at most 5 disks, NaN/negative rejected) and fuzzed. Runs only while a dashboard event stream is open and the vault is unlocked; switching off drops the reading at once. Logs record the server ID and error kind only. |
| Find services (0.5.0) | Requested feature. The second command TunnelTab can run on a server, the constant `discover.Command` (`ss`, `netstat`, `docker ps` with a fixed three-field format; changes nothing). Runs only from a user click plus a confirmation dialog; `Manager.Run` connects like Test connection, so unknown or changed host keys still need confirmation (tested: no login before). 20 s timeout, 256 KiB output limit. `discover.Parse` treats the output as hostile: addresses parsed with `net.ParseIP` (link-local and zoned ones refused), ports range-checked, port ranges capped at 16, at most 200 candidates, names reduced to printable characters (≤ 60), and every candidate is fuzz-checked to make a valid `model.Service`. Results are not stored; adding goes through `vault.Update` with the usual validation, all or nothing. The dashboard builds the table with text nodes only. Logs record the server ID and the number found. |
| Update progress (0.5.0) | "Update now" now sends `update` events (step names and byte counts only) to the dashboards. After the restart, the old tab polls `GET /api/state` with its old session token (refused by the new program) and then with the session the new tab stored, to say which version runs. No new endpoint, no new network access; the update checks are unchanged. |
| Traffic counters, ping, server notes (0.4.0) | Ping reuses the existing keep-alive; traffic is counted in memory as bytes pass through each tunnel; notes are a new vault field (validated, ≤ 10 000 characters). Nothing new is sent to servers or written outside the vault. |
| Locking detaches and blanks terminal pages instead of closing the shells | Closing killed running work (e.g. a `docker pull`) found in manual testing. Pages still can't read or type while locked, and re-attaching needs the vault unlocked; a session whose tab is closed ends about 10 s later. |

## Accepted risks

| Risk | Why it's accepted |
|---|---|
| Decrypted secrets are Go strings and can't be wiped from memory while unlocked | Only reachable by code already running as you (see SECURITY.md → not protected against malware). The vault key itself is wiped on lock. |
| `/terminal.html` and, since 0.3.0, the dashboard page allow inline *styles* | xterm.js needs them (it has no CSP nonce support), and terminals now show on the dashboard too. Scripts stay `'self'`-only; the dashboard never inserts HTML (text nodes only) and terminal output is drawn as text, so data can't inject styles. |
| Other accounts on the *same* PC can connect to open tunnels on 127.0.0.1 | Inherent to local port forwarding; TunnelTab is designed for single-user PCs (documented). |
| FAT32/exFAT drives have no permissions | The folder can't be restricted there; the vault stays encrypted. A warning is logged. |
| The Windows ssh-agent connection is only compiled in CI, not exercised | Needs a manual check on Windows before release (Phase 7 checklist). |
| The executable is not code-signed | Windows SmartScreen warns; signing costs money. Documented in the README. |

## Re-running the checks

See [DEVELOPMENT.md → Security checks](DEVELOPMENT.md#security-checks).
