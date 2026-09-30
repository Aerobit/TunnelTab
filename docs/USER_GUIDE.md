# TunnelTab user guide

> 🚧 **Being written alongside the app.** Sections are filled in as each
> feature is built (see [PLAN.md](../PLAN.md)). Headings below show what the
> finished guide will cover.

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

*(Phase 4)*

## Unlocking and locking

*(Phase 4)*

## Projects

*(Phase 4)*

## Adding a server

*(Phase 4)*

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

*(Phase 5)*

## Services and web UI quick launch

*(Phase 4)*

## Settings

*(Phase 4)*

## Backups and moving to another PC

Everything TunnelTab saves is in the `data` folder inside the `tunneltab`
folder, encrypted with your master password. To move to another PC, or to back
up, copy the whole `tunneltab` folder. *(More detail in Phase 7.)*

## Quitting

*(Phase 3)*

## Troubleshooting

*(Phase 7)*

## FAQ

*(Phase 7)*
