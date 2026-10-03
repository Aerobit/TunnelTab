package sshx

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// Errors the dashboard can act on. Messages are shown to the user and never
// contain secrets.
var (
	// ErrAuthFailed means the server rejected the login.
	ErrAuthFailed = errors.New("login failed: the server rejected the username or credentials")
	// ErrKeyPassphrase means a private key is encrypted and the passphrase
	// is missing or wrong.
	ErrKeyPassphrase = errors.New("the private key is encrypted: enter its passphrase")
	// ErrPortInUse means the service's local port is taken by another program.
	ErrPortInUse = errors.New("the local port is already in use by another program")
	// ErrPaused is returned by a TargetFunc while the vault is locked;
	// connections wait for Resume instead of failing.
	ErrPaused = errors.New("waiting for the vault to be unlocked")
	// ErrClosed is returned after the Manager has been closed.
	ErrClosed = errors.New("SSH engine is shut down")
	// ErrStartCancelled is returned by StartForward when the service was
	// stopped (or its server stopped) while it was starting.
	ErrStartCancelled = errors.New("the tunnel was stopped while it was starting")
)

// UnknownHostKeyError means the server's host key hasn't been confirmed yet.
// The dashboard shows Fingerprint and, if the user accepts, stores Key (the
// exact key seen here, so a different key presented later is still caught)
// and retries.
type UnknownHostKeyError struct {
	Address     string        // normalised known-hosts address
	Key         ssh.PublicKey // the key the server presented
	Fingerprint string        // SHA256:… (as shown by ssh-keygen -l)
}

func (e *UnknownHostKeyError) Error() string {
	return fmt.Sprintf("new server %s: confirm its fingerprint %s %s", e.Address, e.Key.Type(), e.Fingerprint)
}

// HostKeyChangedError means the server presented a key different from the
// confirmed one. This can mean a man-in-the-middle attack, so the connection
// is refused; the user must deliberately replace the key.
type HostKeyChangedError struct {
	Address     string
	Key         ssh.PublicKey // the new key the server presented
	Fingerprint string        // fingerprint of the new key
	Known       []string      // fingerprints of the confirmed keys
}

func (e *HostKeyChangedError) Error() string {
	return fmt.Sprintf("WARNING: the host key for %s has changed (now %s %s, expected %s). "+
		"This could be an attack. Only replace the key if you know the server was reinstalled or its key rotated.",
		e.Address, e.Key.Type(), e.Fingerprint, strings.Join(e.Known, ", "))
}

// permanent reports whether retrying the connection cannot help without the
// user doing something (confirming a key, fixing credentials).
func permanent(err error) bool {
	var unknown *UnknownHostKeyError
	var changed *HostKeyChangedError
	return errors.As(err, &unknown) || errors.As(err, &changed) ||
		errors.Is(err, ErrAuthFailed) || errors.Is(err, ErrKeyPassphrase)
}

// friendlyDialError turns low-level handshake errors into user-facing ones.
func friendlyDialError(address string, err error) error {
	var unknown *UnknownHostKeyError
	var changed *HostKeyChangedError
	switch {
	case errors.As(err, &unknown), errors.As(err, &changed):
		return err
	case strings.Contains(err.Error(), "unable to authenticate"):
		return ErrAuthFailed
	case strings.Contains(err.Error(), "no common algorithm for host key"):
		// We restrict host-key algorithms to the confirmed key's type; a
		// server that can't offer it has changed its key.
		return fmt.Errorf("the server at %s no longer offers the confirmed host key type; its key may have changed", address)
	default:
		return fmt.Errorf("can't connect to %s: %w", address, err)
	}
}

// ErrorKind classifies an error into a short, fixed label for log files.
// Logs live unencrypted in the data folder, so they record the kind of
// problem and IDs only, never hostnames, addresses or server messages; the
// dashboard shows the full error to the user.
func ErrorKind(err error) string {
	var unknown *UnknownHostKeyError
	var changed *HostKeyChangedError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &unknown):
		return "unknown_host_key"
	case errors.As(err, &changed):
		return "host_key_changed"
	case errors.Is(err, ErrAuthFailed):
		return "auth_failed"
	case errors.Is(err, ErrKeyPassphrase):
		return "key_passphrase"
	case errors.Is(err, ErrPortInUse):
		return "port_in_use"
	case errors.Is(err, ErrPaused):
		return "vault_locked"
	case errors.Is(err, ErrClosed):
		return "shut_down"
	default:
		return "connection_error"
	}
}
