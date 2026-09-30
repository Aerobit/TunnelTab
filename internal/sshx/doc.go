// Package sshx is the SSH engine. It keeps one SSH connection per server,
// shared by that server's port forwards and terminal sessions, and handles
// authentication (agent, key file, vault key, password), host-key verification
// against data/known_hosts, local port forwards bound to 127.0.0.1, PTY
// sessions, keep-alives and automatic reconnection.
//
// It uses golang.org/x/crypto/ssh directly: no external ssh or sshpass
// processes are started, and no shell is ever invoked.
package sshx
