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
// A terminal session (termSession) owns an SSH shell and lives independently
// of any browser page. A page *attaches* to it over a WebSocket and can
// detach and re-attach: after a reload, a brief network blip, or — most
// importantly — while TunnelTab is locked. Locking detaches every page (so
// nothing can be seen or typed) but leaves the shell and its programs running
// on the server; unlocking lets the pages re-attach, and the recent output is
// replayed.
//
// A terminal page also keeps an event stream open (GET /api/events?terminal=
// <id>) for as long as the tab exists — locked, hidden or not. Browsers keep
// such connections alive in background tabs (unlike timers). When the tab is
// closed, both connections drop, and the session is closed terminalDetachGrace
// later (long enough for a reload to re-attach first). Closing the tab ends
// the session, like closing a terminal window.
//
// 1. POST /api/terminals {serverId, cols, rows} opens the shell and returns
//    {terminalId, ticket, serverName}. POST /api/terminals/{id}/attach returns
//    a new ticket for an existing session. DELETE /api/terminals/{id} ends it.
// 2. The page connects a WebSocket to /api/terminals/connect?ticket=<ticket>.
//    Browsers can't send headers on WebSockets, so the one-time, 30-second
//    ticket stands in for the session token; the Host and Origin checks
//    still apply.
//
// WebSocket protocol:
//   - binary messages carry terminal data in both directions;
//   - text messages are JSON control messages:
//     browser → app: {"type":"resize","cols":120,"rows":40}
//     app → browser: {"type":"attached"} then the recent output (binary),
//                    {"type":"locked"} before detaching because of a lock,
//                    {"type":"exit","code":0,"message":"…"} when the shell ends.

const (
	terminalReadLimit  = 64 * 1024
	terminalScrollback = 512 * 1024 // output kept for replay on re-attach
	terminalWriteLimit = 10 * time.Second
)

// Timings (variables so tests can shorten them).
var (
	terminalTicketTTL   = 30 * time.Second // how long a WebSocket ticket is valid
	terminalTouchEvery  = 10 * time.Second // how often typing postpones auto-lock
	terminalDetachGrace = 10 * time.Second // how long a session with no page waits before closing
	terminalReapEvery   = 5 * time.Second
)

type terminals struct {
	mu       sync.Mutex
	sessions map[string]*termSession // by terminal ID
	tickets  map[[32]byte]termTicket // ticket hash → terminal
}

type termTicket struct {
	id      string
	expires time.Time
}

type termSession struct {
	id, serverID, serverName string
	shell                    *sshx.Shell

	mu       sync.Mutex
	scroll   []byte          // the last terminalScrollback bytes of output
	conn     *websocket.Conn // the attached page, nil while detached
	watchers int             // open event streams from this session's page
	lastSeen time.Time       // when a page last detached or stopped watching
	ended    bool
}

func newTerminals() terminals {
	return terminals{sessions: map[string]*termSession{}, tickets: map[[32]byte]termTicket{}}
}

// --- API ----------------------------------------------------------------

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
	t := &termSession{id: randomToken(), serverID: req.ServerID, serverName: name, shell: shell, lastSeen: s.now()}
	s.terms.mu.Lock()
	s.terms.sessions[t.id] = t
	s.terms.mu.Unlock()
	go s.runTerminal(t)

	writeJSON(w, http.StatusOK, map[string]string{"terminalId": t.id, "ticket": s.terminalTicket(t.id), "serverName": name})
}

func (s *Server) handleAttachTerminal(w http.ResponseWriter, r *http.Request) {
	if s.vaultState() != "unlocked" {
		writeError(w, http.StatusLocked, "locked", "the vault is locked")
		return
	}
	t := s.terminal(r.PathValue("id"))
	if t == nil {
		writeError(w, http.StatusNotFound, "not_found", "this terminal session has ended")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"terminalId": t.id, "ticket": s.terminalTicket(t.id), "serverName": t.serverName})
}

func (s *Server) handleCloseTerminal(w http.ResponseWriter, r *http.Request) {
	t := s.terminal(r.PathValue("id"))
	if t == nil {
		writeError(w, http.StatusNotFound, "not_found", "this terminal session has ended")
		return
	}
	t.shell.Close()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleConnectTerminal(w http.ResponseWriter, r *http.Request) {
	// Browsers always send Origin on WebSockets; the guard has already
	// checked it matches. Refuse non-browser clients without one.
	if r.Header.Get("Origin") == "" {
		writeError(w, http.StatusForbidden, "bad_origin", "missing Origin header")
		return
	}
	t := s.redeemTerminalTicket(r.URL.Query().Get("ticket"))
	if t == nil {
		writeError(w, http.StatusUnauthorized, "bad_ticket", "this terminal link has expired: open the terminal again")
		return
	}
	if s.vaultState() != "unlocked" {
		writeError(w, http.StatusLocked, "locked", "the vault is locked")
		return
	}
	// websocket.Accept also verifies that Origin matches Host.
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(terminalReadLimit)
	if !t.attach(c) {
		c.Close(websocket.StatusNormalClosure, "session ended")
		return
	}
	s.log.Info("terminal attached", "server", t.serverID)
	s.readTerminalInput(r.Context(), t, c)
}

// --- Sessions -----------------------------------------------------------

func (s *Server) terminal(id string) *termSession {
	s.terms.mu.Lock()
	defer s.terms.mu.Unlock()
	return s.terms.sessions[id]
}

func (s *Server) terminalTicket(id string) string {
	ticket := randomToken()
	s.terms.mu.Lock()
	s.terms.tickets[hashToken(ticket)] = termTicket{id: id, expires: s.now().Add(terminalTicketTTL)}
	s.terms.mu.Unlock()
	return ticket
}

// redeemTerminalTicket returns the session for a ticket (one-time use).
func (s *Server) redeemTerminalTicket(ticket string) *termSession {
	h := hashToken(ticket)
	s.terms.mu.Lock()
	defer s.terms.mu.Unlock()
	tk, ok := s.terms.tickets[h]
	delete(s.terms.tickets, h)
	if !ok || s.now().After(tk.expires) {
		return nil
	}
	return s.terms.sessions[tk.id]
}

// runTerminal forwards the shell's output to the attached page (and keeps it
// for replay) until the shell ends, then tells the page why and forgets the
// session.
func (s *Server) runTerminal(t *termSession) {
	buf := make([]byte, 32*1024)
	for {
		n, err := t.shell.Read(buf)
		if n > 0 {
			t.output(buf[:n])
		}
		if err != nil {
			break
		}
	}
	<-t.shell.Done()
	code, err := t.shell.ExitStatus()
	msg := ""
	switch {
	case errors.Is(err, sshx.ErrShellClosed):
		msg = "the terminal was closed by TunnelTab"
	case err != nil:
		msg = "the connection to the server was lost"
	}
	exit, _ := json.Marshal(map[string]any{"type": "exit", "code": code, "message": msg})

	s.terms.mu.Lock()
	delete(s.terms.sessions, t.id)
	s.terms.mu.Unlock()
	t.end(exit)
}

// readTerminalInput passes the page's keystrokes and resizes to the shell
// until the page goes away; the session then stays detached (not closed).
// Typing counts as activity, so auto-lock doesn't lock a terminal in use.
func (s *Server) readTerminalInput(ctx context.Context, t *termSession, c *websocket.Conn) {
	defer t.detach(c, s.now())
	var lastTouch time.Time
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		if time.Since(lastTouch) >= terminalTouchEvery {
			if v := s.currentVault(); v != nil {
				v.Touch()
			}
			lastTouch = time.Now()
		}
		if typ == websocket.MessageBinary {
			if _, err := t.shell.Write(data); err != nil {
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
			t.shell.Resize(msg.Cols, msg.Rows)
		}
	}
}

// output records terminal output and sends it to the attached page.
func (t *termSession) output(p []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.scroll = append(t.scroll, p...)
	if over := len(t.scroll) - terminalScrollback; over > 0 {
		t.scroll = append([]byte(nil), t.scroll[over:]...)
	}
	if t.conn != nil {
		if err := writeTimeout(t.conn, websocket.MessageBinary, p); err != nil {
			t.conn.CloseNow()
			t.conn, t.lastSeen = nil, time.Now()
		}
	}
}

// attach connects a page, replacing any page already attached, and replays
// the recent output. It reports false if the session has already ended.
func (t *termSession) attach(c *websocket.Conn) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ended {
		return false
	}
	if t.conn != nil {
		closeAsync(t.conn, "opened in another tab")
	}
	t.conn = c
	if writeTimeout(c, websocket.MessageText, []byte(`{"type":"attached"}`)) != nil ||
		(len(t.scroll) > 0 && writeTimeout(c, websocket.MessageBinary, t.scroll) != nil) {
		c.CloseNow()
		t.conn = nil
	}
	return true
}

// detach disconnects c if it is still the attached page.
func (t *termSession) detach(c *websocket.Conn, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.conn == c {
		t.conn, t.lastSeen = nil, now
		closeAsync(c, "")
	}
}

// hide detaches the page because TunnelTab locked, telling it why.
func (t *termSession) hide(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.conn != nil {
		writeTimeout(t.conn, websocket.MessageText, []byte(`{"type":"locked"}`))
		closeAsync(t.conn, "TunnelTab is locked")
		t.conn = nil
	}
	t.lastSeen = now
}

// end reports the exit to the attached page and marks the session ended.
func (t *termSession) end(exit []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ended = true
	if t.conn != nil {
		writeTimeout(t.conn, websocket.MessageText, exit)
		closeAsync(t.conn, "session ended")
		t.conn = nil
	}
}

// closeAsync closes a page's WebSocket without waiting for the page to
// answer the close handshake (which can take seconds, or never come).
func closeAsync(c *websocket.Conn, reason string) {
	go c.Close(websocket.StatusNormalClosure, reason)
}

func writeTimeout(c *websocket.Conn, typ websocket.MessageType, p []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), terminalWriteLimit)
	defer cancel()
	return c.Write(ctx, typ, p)
}

// --- Lock, unlock and clean-up ------------------------------------------

func (s *Server) sessionsSnapshot() []*termSession {
	s.terms.mu.Lock()
	defer s.terms.mu.Unlock()
	list := make([]*termSession, 0, len(s.terms.sessions))
	for _, t := range s.terms.sessions {
		list = append(list, t)
	}
	return list
}

// hideTerminals detaches every page when the vault locks. The shells keep
// running; pending tickets are cancelled.
func (s *Server) hideTerminals() {
	s.terms.mu.Lock()
	s.terms.tickets = map[[32]byte]termTicket{}
	s.terms.mu.Unlock()
	now := s.now()
	for _, t := range s.sessionsSnapshot() {
		t.hide(now)
	}
}

// watchTerminal records that a page's event stream for session id is open,
// and returns a function to call when it closes. Unknown IDs are ignored.
func (s *Server) watchTerminal(id string) func() {
	t := s.terminal(id)
	if t == nil {
		return func() {}
	}
	t.mu.Lock()
	t.watchers++
	t.mu.Unlock()
	return func() {
		t.mu.Lock()
		t.watchers--
		t.lastSeen = s.now()
		t.mu.Unlock()
	}
}

// reapTerminals closes sessions whose page is gone (no WebSocket and no
// event stream) for longer than the grace period — whether or not the vault
// is locked — and drops expired tickets, until stop is closed.
func (s *Server) reapTerminals(stop <-chan struct{}) {
	tick := time.NewTicker(terminalReapEvery)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			s.reapTerminalsOnce()
		}
	}
}

func (s *Server) reapTerminalsOnce() {
	now := s.now()
	s.terms.mu.Lock()
	for h, tk := range s.terms.tickets {
		if now.After(tk.expires) {
			delete(s.terms.tickets, h)
		}
	}
	s.terms.mu.Unlock()
	for _, t := range s.sessionsSnapshot() {
		t.mu.Lock()
		abandoned := t.conn == nil && t.watchers == 0 && !t.ended && now.Sub(t.lastSeen) > terminalDetachGrace
		t.mu.Unlock()
		if abandoned {
			s.log.Info("closing abandoned terminal", "server", t.serverID)
			t.shell.Close()
		}
	}
}
