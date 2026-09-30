package sshx

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/Aerobit/TunnelTab/internal/model"
)

// maxKeyFileSize bounds how much is read from a private key file.
const maxKeyFileSize = 64 * 1024

// authMethods builds the SSH login methods for a server. The returned closer
// (possibly nil) must be closed when the connection ends (it holds the
// ssh-agent connection).
//
// Secrets are used only here and inside the SSH handshake; they are never
// logged or put into error messages.
func authMethods(auth model.Auth, baseDir string) ([]ssh.AuthMethod, io.Closer, error) {
	switch auth.Type {
	case model.AuthPassword:
		pw := auth.Password
		// Many servers ask for passwords through keyboard-interactive.
		answer := func(_, _ string, questions []string, _ []bool) ([]string, error) {
			answers := make([]string, len(questions))
			for i := range answers {
				answers[i] = pw
			}
			return answers, nil
		}
		return []ssh.AuthMethod{ssh.Password(pw), ssh.KeyboardInteractive(answer)}, nil, nil

	case model.AuthKeyVault:
		signer, err := parseKey([]byte(auth.PrivateKey), auth.Passphrase)
		if err != nil {
			return nil, nil, err
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil, nil

	case model.AuthKeyFile:
		path := auth.KeyPath
		if !filepath.IsAbs(path) {
			path = filepath.Join(baseDir, path)
		}
		pem, err := readKeyFile(path)
		if err != nil {
			return nil, nil, err
		}
		signer, err := parseKey(pem, auth.Passphrase)
		if err != nil {
			return nil, nil, err
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil, nil

	case model.AuthAgent:
		conn, err := dialAgent()
		if err != nil {
			return nil, nil, fmt.Errorf("can't reach ssh-agent: %w", err)
		}
		client := agent.NewClient(conn)
		return []ssh.AuthMethod{ssh.PublicKeysCallback(client.Signers)}, conn, nil

	default:
		return nil, nil, fmt.Errorf("unsupported login method %q", auth.Type)
	}
}

func readKeyFile(path string) ([]byte, error) {
	// Check before opening: opening a pipe or device could block forever.
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("private key file not found: %s", path)
		}
		return nil, fmt.Errorf("can't read private key file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("can't read private key file %s: %w", path, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxKeyFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("can't read private key file %s: %w", path, err)
	}
	if len(data) > maxKeyFileSize {
		return nil, fmt.Errorf("%s is too large to be a private key", path)
	}
	return data, nil
}

// parseKey parses a PEM/OpenSSH private key, decrypting it with passphrase
// if it is encrypted.
func parseKey(pem []byte, passphrase string) (ssh.Signer, error) {
	signer, err := ssh.ParsePrivateKey(pem)
	var missing *ssh.PassphraseMissingError
	switch {
	case err == nil:
		return signer, nil
	case errors.As(err, &missing):
		if passphrase == "" {
			return nil, ErrKeyPassphrase
		}
		signer, err = ssh.ParsePrivateKeyWithPassphrase(pem, []byte(passphrase))
		if err != nil {
			return nil, fmt.Errorf("%w (the passphrase is wrong)", ErrKeyPassphrase)
		}
		return signer, nil
	default:
		return nil, errors.New("the private key could not be read: unsupported or damaged key")
	}
}
