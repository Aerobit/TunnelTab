//go:build windows

package sshx

import (
	"errors"
	"io"
	"os"
)

// windowsAgentPipe is the named pipe of the Windows OpenSSH agent service
// ("OpenSSH Authentication Agent").
const windowsAgentPipe = `\\.\pipe\openssh-ssh-agent`

// dialAgent connects to the Windows OpenSSH agent. Named pipes can be opened
// like files; the agent protocol is strictly request/response, so blocking
// reads and writes are fine.
func dialAgent() (io.ReadWriteCloser, error) {
	f, err := os.OpenFile(windowsAgentPipe, os.O_RDWR, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errors.New(`the OpenSSH Authentication Agent service isn't running; enable it in Windows Services and add your key with ssh-add`)
		}
		return nil, err
	}
	return f, nil
}
