# Development guide

How to build, test, run and release TunnelTab.

## Prerequisites

- **Go 1.27 or newer** — the only requirement. No C compiler, Node.js or
  other tools are needed to build.
- **Git** — optional, used to stamp the version into builds.

### Installing Go

**Windows:** install from <https://go.dev/dl/> (the `.msi`), then open a new
terminal and check `go version`.

**Linux / dev container (no admin rights needed):**

```bash
V=$(curl -s 'https://go.dev/VERSION?m=text' | head -1)      # e.g. go1.27.1
curl -sSLO "https://go.dev/dl/${V}.linux-amd64.tar.gz"
# Verify the SHA-256 against https://go.dev/dl/ before extracting.
mkdir -p ~/.local && rm -rf ~/.local/go
tar -C ~/.local -xzf "${V}.linux-amd64.tar.gz" && rm "${V}.linux-amd64.tar.gz"
echo 'export PATH="$HOME/.local/go/bin:$HOME/go/bin:$PATH"' >> ~/.bashrc
source ~/.bashrc && go version
```

> In the dev container Go is installed at `~/.local/go`. It lives in the
> container, so if the container is recreated, run the steps above again.

**VS Code:** install the recommended extensions when prompted
(`golang.go`, `editorconfig.editorconfig`).

## Everyday commands

| Task | Command |
|---|---|
| Run a dev build | `go run ./cmd/tunneltab` (data goes to `./data`) |
| Run without opening a browser | `go run ./cmd/tunneltab --no-browser` (prints the login link) |
| Show the version | `go run ./cmd/tunneltab --version` |
| All tests | `go test ./...` |
| Tests + race detector (what CI runs) | `go test -race ./...` (needs a C compiler; skip `-race` if you have none) |
| One package, verbose | `go test -v ./internal/vault` |
| Static checks | `go vet ./...` |
| Formatting check | `gofmt -l .` (prints nothing when clean) |
| Format everything | `gofmt -w .` |

## Trying the app from the dev container

The dev container has no browser, but VS Code can forward the dashboard
port to your PC:

1. In the VS Code terminal: `go run ./cmd/tunneltab --no-browser`
2. VS Code shows "Your application running on port 47811 is available" —
   or open the **Ports** panel and forward 47811.
3. Open the printed link on your PC, replacing `127.0.0.1` with `localhost`
   if the forwarded address uses it (both are accepted). The link works once
   and for 2 minutes; run the program again (while it's running) for a new one.
4. Stop with Ctrl+C or the Quit button.

`data/` is git-ignored, so dev vaults are never committed.

## Trying the dashboard without a VPS

`internal/devtools/fakessh` runs a local SSH server with a demo web page
behind it:

```bash
go run ./internal/devtools/fakessh      # prints host, port, username, password
                                        # -port 2222 for a fixed port, -demo for a realistic prompt
                                        # type drop / down / up + Enter to cut or refuse connections
go run ./cmd/tunneltab --no-browser     # in a second terminal
```

Add a server with the printed details (password login), confirm the
printed fingerprint, then add a service with the printed remote port.

## Browser end-to-end test

`tests/e2e/run.js` drives the real program in headless Chromium: vault,
project, server, fingerprint, service, opening through the tunnel,
security checks, password change, lock/unlock, quit. It fails on any
JavaScript error or CSP violation and saves screenshots to
`tests/e2e/screenshots/`.

```bash
cd tests/e2e
npm ci
npx playwright install --with-deps chromium   # once; drop --with-deps without admin rights
node run.js
node update.js    # "Update now": real 0.1.0 → signed 0.2.0 update, then a rollback
```

`update.js` builds two versions with a throwaway release key, serves a
signed release from a fake GitHub (`--update-url`), clicks **Update now**
and checks the restart, the vault, the cleanup and (on Linux) the rollback
of a release that can't start.

CI runs both on every push (job *Browser end-to-end*); the screenshots are
attached to the run as an artifact. Run them after any dashboard change.

### README screenshots

`node readme-shots.js` (same folder) regenerates `docs/images/*.png` with
made-up demo data. Run it when the UI changes and commit the images.

## Security checks

```bash
go run honnef.co/go/tools/cmd/staticcheck@latest ./...   # static analysis
go run golang.org/x/vuln/cmd/govulncheck@latest ./...    # known vulnerabilities
go test -run '^$' -fuzz '^FuzzServerValidate$' -fuzztime 60s ./internal/model
go test -run '^$' -fuzz '^FuzzServiceValidate$' -fuzztime 60s ./internal/model
go test -run '^$' -fuzz '^FuzzParse$' -fuzztime 60s ./internal/vault
go test -run '^$' -fuzz '^FuzzAPIBodies$' -fuzztime 60s ./internal/server
go test -run '^$' -fuzz '^FuzzParse$' -fuzztime 60s ./internal/health
```

The fuzz tests' seed inputs also run as ordinary tests in `go test ./...`.
Inputs that ever made a fuzz test fail are saved under `testdata/fuzz/` —
commit them, they become permanent regression tests. CI runs staticcheck
and govulncheck on every push (job *Static analysis and vulnerabilities*).

The last review is recorded in [SECURITY_REVIEW.md](SECURITY_REVIEW.md).

## Building the portable package

```bash
bash scripts/build.sh            # Linux / WSL / macOS
bash scripts/build.sh 0.2.0      # explicit version
```

```powershell
.\scripts\build.ps1              # Windows PowerShell
.\scripts\build.ps1 0.2.0
```

Both scripts produce the same output:

```
dist/
├── tunneltab/                      the portable folder
│   ├── tunneltab.exe               Windows x64 (no console window)
│   ├── tunneltab-linux-amd64       Linux x64
│   ├── README.txt                  from packaging/README.txt
│   ├── LICENSE.txt
│   └── THIRD_PARTY_NOTICES.txt     generated by scripts/notices
├── tunneltab-<version>.zip         the folder, zipped (Linux binary keeps +x)
└── SHA256SUMS.txt                  checksum of the zip
```

Build details:

- `CGO_ENABLED=0` → fully static binaries, no DLLs or shared libraries.
- `-trimpath` → no local file paths embedded in the binary.
- `-ldflags "-s -w -X main.version=…"` → smaller binary, version stamped in.
- `-H windowsgui` (Windows only) → no console window when double-clicked.
  Because of this, `tunneltab.exe --version` prints nothing when run from a
  Windows terminal; use `go run ./cmd/tunneltab --version` instead.
- The version defaults to `git describe --tags --always --dirty`, or `dev`.
- Zipping uses `scripts/mkzip` (a small Go program), so no `zip` tool is needed.

## Project layout

See [ARCHITECTURE.md](ARCHITECTURE.md) for what each package does.

```
cmd/tunneltab/     entry point
internal/          application packages (not importable by other modules)
web/static/        dashboard assets, embedded into the binary
scripts/           build scripts, the mkzip helper and the signsums release tool
packaging/         files copied into the portable folder
docs/              documentation
.github/workflows/ CI (ci.yml) and releases (release.yml)
```

## Continuous integration

`.github/workflows/ci.yml` runs on every push to `main` and every pull request,
on both Ubuntu and Windows:

1. `gofmt` check (Ubuntu)
2. `go vet ./...`
3. `go test -race ./...` (includes the Windows-only permission test on Windows)
4. Full portable build (Ubuntu)

plus two more jobs: *Static analysis and vulnerabilities* (staticcheck,
govulncheck) and *Browser end-to-end* (see above).

## Making a release

Follow [RELEASE_CHECKLIST.md](RELEASE_CHECKLIST.md). In short:

1. Move the entries under **Unreleased** in `CHANGELOG.md` into a new version
   section, e.g. `## [0.1.0] - 2026-10-15`. The release notes are taken from
   this section, so the release fails if it's missing.
2. Commit, then tag and push:
   ```bash
   git tag v0.1.0
   git push origin v0.1.0
   ```
3. `.github/workflows/release.yml` runs the tests, builds
   `tunneltab-0.1.0.zip`, signs `SHA256SUMS.txt` (see below), and publishes
   the zip, `SHA256SUMS.txt` and `SHA256SUMS.txt.sig` as a GitHub Release.

### Release signing key

"Update now" installs only a zip whose SHA-256 is listed
in a `SHA256SUMS.txt` signed with TunnelTab's release key. The key is an
Ed25519 key pair:

- the **private key** is the repository secret `TUNNELTAB_SIGNING_KEY`, used
  only by the release workflow. It is never committed.
- the **public key** is `ReleasePublicKey` in
  `internal/update/signature.go`, built into every TunnelTab.

The release workflow refuses to publish if the secret is missing or doesn't
match `ReleasePublicKey`. The signature covers a fixed context string plus
the exact bytes of `SHA256SUMS.txt`; `SHA256SUMS.txt.sig` holds it as one
base64 line (`internal/update.SignSums` / `VerifySums`).

**Creating the key (once):**

1. In the dev container terminal, from the repo folder:
   ```bash
   go run ./scripts/signsums genkey ../tunneltab-signing-key.txt
   ```
   This writes the private key **outside the repo** and prints the public key.
2. On GitHub: repository → *Settings* → *Secrets and variables* → *Actions*
   → *New repository secret*. Name `TUNNELTAB_SIGNING_KEY`, value: the one
   line in `tunneltab-signing-key.txt`.
3. Put the printed public key in `ReleasePublicKey` and commit.
4. Keep a backup of `tunneltab-signing-key.txt` somewhere safe (e.g. a
   password manager), then delete the file from the workspace.

**If the private key is lost or leaked:** make a new pair as above and
release a version with the new public key. Existing installs trust only the
old key, so that one update has to be done by hand (download the zip).

To check a release by hand:
`go run ./scripts/signsums verify SHA256SUMS.txt` (next to its `.sig`).

### What the build adds

- **Windows resources** — `go-winres` (run by the build scripts, not a
  dependency of the program) embeds the icon (`packaging/icon.png`), a
  manifest, and version details shown in *Properties → Details*. The version
  there is the numeric part of the release version (`0.0.0` for dev builds).
  The generated `.syso` file is deleted after the build and git-ignored.
- **Licenses** — `LICENSE.txt`, and `THIRD_PARTY_NOTICES.txt` generated by
  `scripts/notices` from the modules actually compiled into the executables
  (plus Go itself and the vendored xterm.js).
- **Text files** use Windows line endings; `SHA256SUMS.txt` uses plain LF.
- `build.sh` and `build.ps1` produce byte-for-byte identical files.

## Conventions

The coding rules and security invariants are in [CLAUDE.md](../CLAUDE.md).
They apply to human contributors too. In short:

- Standard library first; ask before adding dependencies.
- Never let secrets reach logs, errors, the browser, argv or env vars.
- Listen on `127.0.0.1` only; authenticate and check `Host`/`Origin` on every request.
- Vanilla JS + CSS, no inline scripts, no CDNs.
- Every change keeps `go vet`, `gofmt` and `go test -race` clean, and updates
  the docs and `CHANGELOG.md`.
