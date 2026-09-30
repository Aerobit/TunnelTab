//go:build !windows

package sshx

import (
	"errors"
	"io"
	"net"
	"os"
)

// dialAgent connects to the ssh-agent named by SSH_AUTH_SOCK.
func dialAgent() (io.ReadWriteCloser, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, errors.New("SSH_AUTH_SOCK is not set; start ssh-agent and add your key with ssh-add")
	}
	return net.Dial("unix", sock)
}
