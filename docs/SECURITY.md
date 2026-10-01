# Security

TunnelTab holds the keys to your servers, so security is its most important
feature. This document explains what it protects against, how, and — just as
importantly — what it does **not** protect against.

> The design below is final; implementation status is tracked in
> [PLAN.md](../PLAN.md). Exact parameters are confirmed here as each part is
> built.

The most recent pre-release review is in [SECURITY_REVIEW.md](SECURITY_REVIEW.md).

## Reporting a vulnerability

Please **don't open a public issue** for security problems. Use GitHub's
private reporting instead: the repository's **Security** tab →
**Report a vulnerability**.

## Threat model

### Protected against

| Threat | Protection |
|---|---|
| Someone copies the folder or steals the USB stick | All projects, servers and secrets are in `vault.enc`, encrypted with a key derived from your master password. The key is never stored. Without the password the file is useless. |
| Guessing the master password offline | Argon2id key derivation (memory-hard, tuned to roughly 0.5–1 s per guess on a normal PC). |
| Tampering with the vault file | AES-256-GCM detects any change; a tampered or corrupted vault refuses to open rather than loading bad data. |
| Leaving the PC unlocked | Auto-lock after inactivity (default 15 minutes) wipes the key from memory. |
| Secrets leaking at runtime | Secrets are never sent to the browser and never appear in logs, error messages, command lines or environment variables. |
| Connecting to an impostor server (MITM) | Host keys are checked against the keys you confirmed, stored inside the encrypted vault. A new server shows its fingerprint for you to confirm, and no password or key is sent to it before you do. A changed key is blocked. The client asks the server for the confirmed key type, so a server can't dodge the check by offering a different kind of key. |
| Malicious websites attacking the local dashboard | The server listens on `127.0.0.1` only. Signing in needs a one-time link (valid 2 minutes) that only the program itself opens; it is exchanged for a session token that the dashboard sends in a header on every request. There is no login cookie, so other sites can't make your browser act on your behalf (CSRF). Requests with a wrong `Host` (DNS rebinding), a cross-origin `Origin` or a cross-site `Sec-Fetch-Site` are refused; no CORS headers are ever sent; a strict Content Security Policy blocks injected scripts. |
| A compromised server's web UI (opened through a tunnel) attacking TunnelTab | Tunneled apps run on other ports of `127.0.0.1`. Browsers share *cookies* across those ports, which is why TunnelTab doesn't use a cookie: its session token is stored per-origin (port included), so tunneled pages can't read it, and their requests are refused by the Origin checks. |
| Guessing the master password through the dashboard | Password checks (unlock and change password) run one at a time, and each wrong password doubles the wait before the next attempt (1 s, 2 s, 4 s … up to 30 s). |
| Another user on the same PC opening your dashboard | A second launch gets a new login link only by proving it can read `data/instance.json`. The data folder is restricted to your account: an owner-only access list on Windows (current user + SYSTEM), mode 0700 on Linux. |
| Log files revealing which servers you use | Logs record internal IDs and error types only — never hostnames, addresses, fingerprints, tokens or secrets (enforced by a test). |
| Other devices on your network using your tunnels | Tunnels listen on `127.0.0.1` only. |
| Which servers you use leaking from the folder | Server addresses and fingerprints are only stored inside the encrypted vault; there is no plain-text `known_hosts` file. |
| Credentials lingering in the SSH engine | The engine asks the vault for credentials on each connect and drops them once connected. While the vault is locked, dropped connections wait ("paused") instead of reusing stored credentials. |
| Command injection | SSH runs inside the app; no local shell is ever used. |
| An open terminal on an unattended PC | Locking (manually or automatically) disconnects every terminal page and blanks it: nothing can be read or typed until you unlock. The shells keep running on the server so work in progress isn't lost. |
| Someone else attaching to your terminal | A terminal is connected with a one-time ticket that expires after 30 seconds, only from the dashboard's own origin; unused tickets close their shell. |
| Unwanted network traffic / tracking | TunnelTab contacts only your servers — except for updates in Settings, and only when you click. **Check for updates** asks GitHub's public release API; **Update now** downloads that release from TunnelTab's GitHub releases. Neither sends anything about you or your servers. |
| Usage figures and notes leaking | Ping comes from the keep-alive TunnelTab already sends; traffic is counted on this PC as data passes through each tunnel. Both, like recent activity, are kept in memory only and never written to disk or the log. Server notes are stored inside the encrypted vault, like login details. |
| A tampered update (hacked GitHub account or release page, or a swapped download) | **Update now** installs a release only if its `SHA256SUMS.txt` is signed (Ed25519) with TunnelTab's release key, whose public half is built into the program, and lists the zip for exactly that version with a matching SHA-256. The private key never touches GitHub's release pages or the repository; it is used only by the release workflow. Anything else is refused and nothing is changed. Only newer versions are installed, and only the program files, never the data folder. If the new version doesn't start, the previous one is put back. |
| Supply-chain / CDN compromise of the UI | All web assets, including xterm.js, are bundled in the executable; nothing is loaded from the internet. The two pages that show terminals (the dashboard and the terminal page) allow inline *styles* (xterm.js needs them) but never inline scripts. |

### Not protected against

- **Malware already running as your user** on the PC. It can read memory,
  capture keystrokes (including the master password) or use open tunnels.
- **A weak master password.** Argon2id slows guessing down; it can't make
  `password123` safe. Use a long passphrase.
- **A compromised server.** TunnelTab secures the connection, not what's on
  the other end.
- **Permissions on FAT32/exFAT drives.** These file systems have no
  permissions, so the data folder can't be restricted there (the vault is
  still encrypted).
- **Other local users on a shared PC reaching your tunnels.** Tunnels bind to
  `127.0.0.1`, which other accounts on the *same* machine can connect to while
  a tunnel is open. TunnelTab is designed for single-user PCs.
- **Forgotten master password.** There is no recovery by design; keep a backup
  of your SSH keys elsewhere.
- **Secrets in memory while unlocked.** The derived key is wiped on lock, but
  decrypted secrets are Go strings, which can't be overwritten; they are
  released for garbage collection. Someone who can read the app's memory
  while it runs (see "malware" above) could find them.

## Cryptography

| Purpose | Choice |
|---|---|
| Key derivation | Argon2id (`golang.org/x/crypto/argon2`): 4 passes, 256 MiB, 4 threads, random 16-byte salt. About 0.2 s on a fast desktop, 0.5–1 s on a typical laptop. Parameters are stored in the vault header so they can be raised later; values outside safe bounds are rejected. |
| Encryption | AES-256-GCM, random 12-byte nonce per save. The header (format, KDF parameters, salt) is authenticated as additional data. |
| Master password | At least 8 characters (the dashboard will also show a strength meter). |
| Randomness | `crypto/rand` |
| SSH | `golang.org/x/crypto/ssh` with its default (modern) algorithms; host-key algorithms pinned to the confirmed key's type |

## Recommendations for users

- Prefer **SSH keys** (or ssh-agent) over passwords.
- Use a **long, unique master password**.
- Keep a **backup** of the portable folder (the vault is useless without the
  password, so backups are safe to store).
- Verify a new server's **fingerprint** against your VPS provider's console
  the first time you connect.
