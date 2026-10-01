# Release checklist

Use this for every release. The automated part runs in CI; the manual part
covers what only a real Windows PC and a real server can show.

## 1. Automated (must be green)

- [ ] CI on `main`: *Test (ubuntu)*, *Test (windows)* (includes the data-folder
      permission test and the PowerShell build), *Static analysis and
      vulnerabilities*, *Browser end-to-end*.
- [ ] `CHANGELOG.md` has a `## [x.y.z] - YYYY-MM-DD` section (the release
      notes are taken from it).

## 2. Manual check on Windows

Get the zip from the latest CI run on `main` (GitHub → **Actions** → the run →
**Artifacts** → `tunneltab-portable-…`; it contains `tunneltab-ci.zip`), or build
it yourself with `.\scripts\build.ps1 x.y.z`.

**Package**
- [ ] `tunneltab.exe` shows the TunnelTab icon; *Properties → Details* shows
      the product name, version and copyright.
- [ ] Double-clicking the `.exe` **inside the ZIP** shows the "running from a
      temporary folder" message and doesn't create anything.
- [ ] After extracting: SmartScreen → *More info → Run anyway* works; no
      console window appears; the browser opens the dashboard.

**First use**
- [ ] Create a master password; the `data` folder appears next to the exe.
- [ ] In PowerShell: `icacls data` lists only your account and `SYSTEM`
      (plus nothing inherited).
- [ ] Add a real server with a **key stored in TunnelTab**: the fingerprint
      matches the server's (`ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`).
- [ ] Add a service for a real web app; **Open ↗** shows it.
- [ ] **Terminal ↗** opens a working shell; resize the window; copy with
      Ctrl+Shift+C and paste with Ctrl+Shift+V.
- [ ] **SSH agent** login: with the *OpenSSH Authentication Agent* service
      running and a key added with `ssh-add`, a server set to *SSH agent*
      connects.

**Everyday behaviour**
- [ ] Close the tab, start the exe again: a new dashboard tab opens (no
      second copy starts).
- [ ] Start a long command in a terminal (e.g. `ping -c 60 1.1.1.1`), then
      Lock: the terminal tab goes blank and says *Locked*; tunnels keep
      working. Unlock: the terminal reconnects by itself and the command's
      output continued while locked.
- [ ] Reload a terminal tab: it reconnects to the same session. Close it:
      the session ends within ~15 s (a long command in it stops).
- [ ] Reorder a service and a server by dragging the ⠿ grip, and with
      ↑/↓ on a focused grip; drag a server into another project.
- [ ] Settings → **Check for updates** reports the right thing (after the
      release: "up to date").
- [ ] **Update now** from the previous release (Windows): it restarts on the
      new version, the vault unlocks, and no `*.old` files remain in the
      folder a minute later.
- [ ] Disconnect the network briefly (or sleep the PC): tunnels and an open
      terminal show *Reconnecting…* (not *Failed*) and recover; the terminal
      gets a new prompt below its old output and takes typing at once.
- [ ] Quit: tunnels stop, the process exits, `data/instance.json` is gone.
- [ ] `data/logs/tunneltab.log` contains no server names or addresses.

**Portability**
- [ ] Copy the whole folder to a USB stick or another PC: it opens with the
      same master password and servers.
- [ ] (Optional) Run `tunneltab-linux-amd64` on Linux against the same folder.

## 3. Publish

1. Move the `Unreleased` entries in `CHANGELOG.md` under
   `## [x.y.z] - YYYY-MM-DD`; commit and push.
2. `git tag vx.y.z && git push origin vx.y.z`
3. The *Release* workflow tests, builds, signs the checksums and publishes
   the zip, `SHA256SUMS.txt`, `SHA256SUMS.txt.sig` and the changelog section
   as a GitHub Release. It fails if the `TUNNELTAB_SIGNING_KEY` secret is
   missing or doesn't match `ReleasePublicKey` (DEVELOPMENT.md → Release
   signing key).
4. Check the release page and download link.
