// Package vault stores all projects, servers, services and secrets in a single
// encrypted file (data/vault.enc).
//
// The encryption key is derived from the master password with Argon2id and is
// never written to disk. Contents are sealed with AES-256-GCM and written
// atomically, keeping one backup copy. The vault auto-locks after inactivity,
// wiping the in-memory key.
package vault
