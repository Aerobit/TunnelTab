# Changelog

All notable changes to TunnelTab are recorded here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and versions follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- Encrypted vault (`internal/vault`): Argon2id + AES-256-GCM, authenticated
  header, atomic saves with a backup copy, password change, auto-lock.
- Data model (`internal/model`): projects, servers (agent / key file / vault
  key / password login), services; validation, cascading deletes, and a
  public view that never includes secrets.
- Portable data folder, `settings.json` and rotating logs (`internal/config`).
- Crash-safe file writes (`internal/atomicfile`).
- Project scaffold: Go module, package layout, build scripts for Windows and
  Linux, CI and release workflows, documentation skeletons.
