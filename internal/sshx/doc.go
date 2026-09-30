// Package sshx is the SSH engine. It keeps one SSH connection per server,
// shared by that server's port forwards and terminal sessions, and handles
// authentication (agent, key file, vault key, password), host-key verification
// against the keys confirmed in the vault, local port forwards bound to
// 127.0.0.1, keep-alives and automatic reconnection. Terminal (PTY) sessions
// are added in Phase 5.
//
// The Manager never stores credentials: it asks its TargetFunc (backed by
// the vault) for them on every connect and drops them once connected.
//
// It uses golang.org/x/crypto/ssh directly: no external ssh or sshpass
// processes are started, and no shell is ever invoked.
package sshx
