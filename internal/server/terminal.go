package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/Aerobit/TunnelTab/internal/model"
	"github.com/Aerobit/TunnelTab/internal/sshx"
)

// Terminals
//
// 1. POST /api/terminals {serverId, cols, rows} opens the SSH shell (so
//    connection and host-key errors come back as normal API errors) and
//    returns a one-time ticket, valid for 30 seconds.
// 2. The terminal page connects a WebSocket to
//    /api/terminals/connect?ticket=<ticket>. Browsers can't send headers on
//    WebSockets, so the ticket stands in for the session token; the Host
//    and Origin checks still apply.
//
// WebSocket protocol:
//   - binary messages carry terminal data in both directions;
//   - text messages are JSON control messages:
//     browser → app: {"type":"resize","cols":120,"rows":40}
//     app → browser: {"type":"exit","code":0,"message":"…"} just before closing.

const terminalReadLimit = 64 * 1024

// terminalTicketTTL is how long a terminal ticket stays valid (a variable so
// tests can shorten it).
var terminalTicketTTL = 30 * time.Second

type terminals struct {
	mu      sync.Mutex
	pending map[[32]byte]*sshx.Shell // ticket hash → shell waiting for its WebSocket
}

func (s *Server) handleOpenTerminal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServerID string `json:"serverId"`
		Cols     int    `json:"cols"`
		Rows     int    `json:"rows"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	var name string
	if err := s.view(func(d *model.Data) error {
		srv, ok := d.Server(req.ServerID)
		if !ok {
			return model.ErrNotFound
		}
		name = srv.Name
		return nil
	}); err != nil {
		s.writeDataError(w, err)
		return
	}
	shell, err := s.mgr.OpenShell(req.ServerID, req.Cols, req.Rows)
	if err != nil {
		s.writeSSHError(w, err)
		return
	}

	ticket := randomToken()
	h := hashToken(ticket)
	s.terms.mu.Lock()
	s.terms.pending[h] = shell
	s.terms.mu.Unlock()
	// Close the shell if the page never connects.
	time.AfterFunc(terminalTicketTTL, func() {
		if sh := s.takeTerminal(h); sh != nil {
			sh.Close()
		}
	})
	writeJSON(w, http.StatusOK, map[string]string{"ticket": ticket, "serverName": name})
}

// takeTerminal removes and returns the shell for a ticket (one-time use).
func (s *Server) takeTerminal(h [32]byte) *sshx.Shell {
	s.terms.mu.Lock()
	defer s.terms.mu.Unlock()
	sh := s.terms.pending[h]
	delete(s.terms.pending, h)
	return sh
}

// closePendingTerminals ends shells whose page hasn't connected yet.
func (s *Server) closePendingTerminals() {
	s.terms.mu.Lock()
	pending := s.terms.pending
	s.terms.pending = map[[32]byte]*sshx.Shell{}
	s.terms.mu.Unlock()
	for _, sh := range pending {
		sh.Close()
	}
}

func (s *Server) handleConnectTerminal(w http.ResponseWriter, r *http.Request) {
	// Browsers always send Origin on WebSockets; the guard has already
	// checked it matches. Refuse non-browser clients without one.
	if r.Header.Get("Origin") == "" {
		writeError(w, http.StatusForbidden, "bad_origin", "missing Origin header")
		return
	}
	shell := s.takeTerminal(hashToken(r.URL.Query().Get("ticket")))
	if shell == nil {
		writeError(w, http.StatusUnauthorized, "bad_ticket", "this terminal link has expired: open the terminal again")
		return
	}
	// websocket.Accept also verifies that Origin matches Host.
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		shell.Close()
		return
	}
	c.SetReadLimit(terminalReadLimit)
	s.log.Info("terminal connected", "server", shell.ServerID())
	s.pumpTerminal(r.Context(), c, shell)
}

// pumpTerminal copies data between the WebSocket and the shell until either
// side ends.
func (s *Server) pumpTerminal(ctx context.Context, c *websocket.Conn, shell *sshx.Shell) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer shell.Close()

	// Shell → browser.
	outputDone := make(chan struct{})
	go func() {
		defer close(outputDone)
		buf := make([]byte, 32*1024)
		for {
			n, err := shell.Read(buf)
			if n > 0 {
				if werr := c.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// Browser → shell.
	inputDone := make(chan struct{})
	go func() {
		defer close(inputDone)
		for {
			typ, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			if typ == websocket.MessageBinary {
				if _, err := shell.Write(data); err != nil {
					return
				}
				continue
			}
			var msg struct {
				Type string `json:"type"`
				Cols int    `json:"cols"`
				Rows int    `json:"rows"`
			}
			if json.Unmarshal(data, &msg) == nil && msg.Type == "resize" {
				shell.Resize(msg.Cols, msg.Rows)
			}
		}
	}()

	select {
	case <-inputDone: // browser closed the page or the socket
		c.Close(websocket.StatusNormalClosure, "")
		return
	case <-shell.Done():
	}
	<-outputDone // flush the last output before reporting the exit
	code, err := shell.ExitStatus()
	msg := ""
	switch {
	case errors.Is(err, sshx.ErrShellClosed) && s.vaultState() == "locked":
		msg = "TunnelTab was locked"
	case errors.Is(err, sshx.ErrShellClosed):
		msg = "the terminal was closed by TunnelTab"
	case err != nil:
		msg = "the connection to the server was lost"
	}
	exit, _ := json.Marshal(map[string]any{"type": "exit", "code": code, "message": msg})
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelWrite()
	if werr := c.Write(writeCtx, websocket.MessageText, exit); werr != nil && !errors.Is(werr, context.Canceled) {
		s.log.Debug("terminal exit message not delivered", "error", werr)
	}
	c.Close(websocket.StatusNormalClosure, "session ended")
}
