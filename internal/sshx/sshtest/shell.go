package sshtest

import (
	"encoding/binary"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

// handleSession serves a "session" channel with a tiny fake shell, enough to
// test terminals. It needs a pty-req and a shell request, echoes typed
// characters like a terminal, and understands these commands:
//
//	echo <text>   prints <text>
//	size          prints the terminal size as "<cols>x<rows>"
//	exit [code]   ends the session with that exit status (default 0)
func handleSession(nc ssh.NewChannel, opts Options) {
	banner, prompt := opts.Banner, opts.Prompt
	if banner == "" {
		banner = "Welcome to the fake shell"
	}
	if prompt == "" {
		prompt = "$ "
	}
	ch, reqs, err := nc.Accept()
	if err != nil {
		return
	}
	defer ch.Close()

	var mu sync.Mutex
	cols, rows := 0, 0
	started := make(chan struct{})
	var once sync.Once

	go func() {
		for req := range reqs {
			ok := false
			switch req.Type {
			case "pty-req":
				// string term, uint32 cols, uint32 rows, ...
				if _, rest, good := readString(req.Payload); good && len(rest) >= 8 {
					mu.Lock()
					cols, rows = int(binary.BigEndian.Uint32(rest)), int(binary.BigEndian.Uint32(rest[4:]))
					mu.Unlock()
					ok = true
				}
			case "window-change":
				if len(req.Payload) >= 8 {
					mu.Lock()
					cols, rows = int(binary.BigEndian.Uint32(req.Payload)), int(binary.BigEndian.Uint32(req.Payload[4:]))
					mu.Unlock()
					ok = true
				}
			case "shell":
				ok = true
				once.Do(func() { close(started) })
			case "env":
				ok = true
			}
			if req.WantReply {
				req.Reply(ok, nil)
			}
		}
		once.Do(func() { close(started) })
	}()
	<-started

	fmt.Fprint(ch, strings.ReplaceAll(banner, "\n", "\r\n")+"\r\n"+prompt)
	var line []byte
	buf := make([]byte, 1024)
	for {
		n, err := ch.Read(buf)
		if err != nil {
			return
		}
		for _, c := range buf[:n] {
			switch c {
			case '\r', '\n':
				fmt.Fprint(ch, "\r\n")
				cmd := strings.TrimSpace(string(line))
				line = line[:0]
				canned, isCanned := opts.Commands[cmd]
				switch {
				case isCanned:
					fmt.Fprint(ch, strings.ReplaceAll(canned, "\n", "\r\n"))
				case cmd == "size":
					mu.Lock()
					fmt.Fprintf(ch, "%dx%d\r\n", cols, rows)
					mu.Unlock()
				case strings.HasPrefix(cmd, "echo "):
					fmt.Fprintf(ch, "%s\r\n", strings.TrimPrefix(cmd, "echo "))
				case cmd == "exit" || strings.HasPrefix(cmd, "exit "):
					code := 0
					fmt.Sscanf(strings.TrimPrefix(cmd, "exit"), "%d", &code)
					status := make([]byte, 4)
					binary.BigEndian.PutUint32(status, uint32(code))
					ch.SendRequest("exit-status", false, status)
					return
				case cmd != "":
					fmt.Fprintf(ch, "fake-shell: %s: command not found\r\n", cmd)
				}
				fmt.Fprint(ch, prompt)
			case 0x7f, 0x08: // backspace
				if len(line) > 0 {
					line = line[:len(line)-1]
					fmt.Fprint(ch, "\b \b")
				}
			case 0x03: // Ctrl+C
				line = line[:0]
				fmt.Fprint(ch, "^C\r\n"+prompt)
			default:
				line = append(line, c)
				ch.Write([]byte{c})
			}
		}
	}
}
