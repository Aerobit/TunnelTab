package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
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
// If the connection to the server drops, the shell ends but the session
// stays: the old shell keeps the connection, which reconnects in the
// background as for tunnels, and once it is back a new shell takes over in
// the same session (the page is told; see reconnectTerminal). Closing the
// terminal meanwhile lets go of the connection.
//
// A terminal page also keeps an event stream open (GET /api/events?terminal=
// <id>) for as long as the tab exists — locked, hidden or not. Browsers keep
// such connections alive in background tabs (unlike timers). When the tab is
// closed, both connections drop, and the session is closed terminalDetachGrace
// later (long enough for a reload to re-attach first). Closing the tab ends
// the session, like closing a terminal window.
//
// Terminals shown inside the dashboard belong to that dashboard tab instead:
// the dashboard's event stream carries a random per-tab ID (?client=<id>),
// POST /api/terminals passes the same ID, and the session is kept while that
// stream is open (and for terminalDetachGrace after, for a reload).
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
//                    {"type":"reconnecting"} when the connection to the server
//                    dropped (sent on attach too while it lasts) and
//                    {"type":"reconnected"} when a new shell took over,
//                    {"type":"exit","code":0,"message":"…"} when the session ends.

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
	terminalReopenEvery = time.Second // how often a session whose connection dropped checks it's back
)

type terminals struct {
	mu       sync.Mutex
	sessions map[string]*termSession // by terminal ID
	tickets  map[[32]byte]termTicket // ticket hash → terminal
	clients  map[string]*termClient  // dashboard tabs (by client ID) that own terminals
}

// termClient is a dashboard tab, present while its event stream is open.
type termClient struct {
	streams  int
	lastSeen time.Time // when its last stream closed
}

// validClientID matches the random per-tab ID the dashboard sends.
var validClientID = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)

type termTicket struct {
	id      string
	expires time.Time
}

type termSession struct {
	id, serverID, serverName string
	openedAt                 time.Time
	owner                    string // client ID of the dashboard tab showing it, if any

	mu           sync.Mutex
	shell        *sshx.Shell     // replaced after a dropped connection comes back
	cols, rows   int             // the page's terminal size, for a new shell
	reconnecting bool            // the connection dropped; waiting for it
	closed       bool            // closed by TunnelTab (close)
	scroll       []byte          // the last terminalScrollback bytes of output
	conn         *websocket.Conn // the attached page, nil while detached
	watchers     int             // open event streams from this session's page
	lastSeen     time.Time       // when a page last detached or stopped watching
	ended        bool
}

func newTerminals() terminals {
	return terminals{sessions: map[string]*termSession{}, tickets: map[[32]byte]termTicket{}, clients: map[string]*termClient{}}
}

// --- API ----------------------------------------------------------------

func (s *Server) handleOpenTerminal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServerID string `json:"serverId"`
		Cols     int    `json:"cols"`
		Rows     int    `json:"rows"`
		Client   string `json:"client"` // the dashboard tab that shows it, if any
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Client != "" && !validClientID.MatchString(req.Client) {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid client ID")
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
	t := &termSession{id: randomToken(), serverID: req.ServerID, serverName: name, shell: shell, cols: req.Cols, rows: req.Rows, lastSeen: s.now(), openedAt: s.now(), owner: req.Client}
	s.terms.mu.Lock()
	s.terms.sessions[t.id] = t
	s.terms.mu.Unlock()
	go s.runTerminal(t)
	s.recordActivity(activityEntry{Kind: "terminal", ID: t.id, ServerID: t.serverID, State: "opened"})

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
	t.close()
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
	// attach checks the vault again: it may have locked since the check above.
	switch t.attach(c, func() bool { return s.vaultState() == "unlocked" }) {
	case attachEnded:
		c.Close(websocket.StatusNormalClosure, "session ended")
		return
	case attachLocked:
		writeTimeout(c, websocket.MessageText, msgLocked)
		c.Close(websocket.StatusNormalClosure, "TunnelTab is locked")
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
// session. If the connection to the server dropped, the session waits for it
// to come back and carries on with a new shell instead.
func (s *Server) runTerminal(t *termSession) {
	buf := make([]byte, 32*1024)
	var code int
	var err error
	for {
		sh := t.currentShell()
		for {
			n, err := sh.Read(buf)
			if n > 0 {
				t.output(buf[:n])
			}
			if err != nil {
				break
			}
		}
		<-sh.Done()
		code, err = sh.ExitStatus()
		if err == nil || errors.Is(err, sshx.ErrShellClosed) {
			break
		}
		if err = s.reconnectTerminal(t, sh); err != nil {
			code = -1
			break
		}
	}
	msg := ""
	switch {
	case errors.Is(err, sshx.ErrShellClosed):
		msg = "the terminal was closed by TunnelTab"
	case err != nil:
		msg = "couldn't reconnect to the server: " + err.Error()
	}
	exit, _ := json.Marshal(map[string]any{"type": "exit", "code": code, "message": msg})

	s.terms.mu.Lock()
	delete(s.terms.sessions, t.id)
	s.terms.mu.Unlock()
	t.end(exit)
	s.recordActivity(activityEntry{Kind: "terminal", ID: t.id, ServerID: t.serverID, State: "ended"})
}

// reconnectTerminal waits for the dropped connection of the session's shell
// (old) to come back — the shell keeps it, so it reconnects in the background
// like a tunnel's — and puts a new shell in the session. It returns why it
// gave up: ErrShellClosed if the terminal was closed meanwhile, or the
// reason reconnecting failed.
func (s *Server) reconnectTerminal(t *termSession, old *sshx.Shell) error {
	t.setReconnecting()
	tick := time.NewTicker(terminalReopenEvery)
	defer tick.Stop()
	for {
		select {
		case <-old.Dropped():
			return sshx.ErrShellClosed
		case <-tick.C:
		}
		cols, rows := t.size()
		sh, err := old.Reopen(cols, rows)
		switch {
		case err == nil:
			if !t.replaceShell(sh) {
				return sshx.ErrShellClosed
			}
			s.log.Info("terminal reconnected", "server", t.serverID)
			return nil
		case errors.Is(err, sshx.ErrReconnecting):
			continue
		default:
			old.Close()
			return err
		}
	}
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
			if !t.write(c, data) {
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
			t.resize(c, msg.Cols, msg.Rows)
		}
	}
}

func (t *termSession) currentShell() *sshx.Shell {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.shell
}

func (t *termSession) size() (cols, rows int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cols, t.rows
}

// write sends keystrokes from page c to the shell. It reports false if the
// shell is gone or c is no longer the attached page (detached, replaced, or
// hidden because TunnelTab locked); while reconnecting, typing is dropped.
func (t *termSession) write(c *websocket.Conn, p []byte) bool {
	t.mu.Lock()
	sh, reconnecting, current := t.shell, t.reconnecting, t.conn == c
	t.mu.Unlock()
	if !current {
		return false
	}
	if reconnecting {
		return true
	}
	_, err := sh.Write(p)
	return err == nil
}

// resize records page c's terminal size and passes it to the shell, if c
// is still the attached page.
func (t *termSession) resize(c *websocket.Conn, cols, rows int) {
	t.mu.Lock()
	if t.conn != c {
		t.mu.Unlock()
		return
	}
	if cols > 0 && rows > 0 && cols <= 1000 && rows <= 1000 {
		t.cols, t.rows = cols, rows
	}
	sh, reconnecting := t.shell, t.reconnecting
	t.mu.Unlock()
	if !reconnecting {
		sh.Resize(cols, rows)
	}
}

// close ends the session (and lets go of the connection if it is waiting
// for it to come back).
func (t *termSession) close() {
	t.mu.Lock()
	t.closed = true
	sh := t.shell
	t.mu.Unlock()
	sh.Close()
}

// Messages telling the page that the connection dropped and came back.
var (
	msgReconnecting = []byte(`{"type":"reconnecting"}`)
	msgReconnected  = []byte(`{"type":"reconnected"}`)
	msgLocked       = []byte(`{"type":"locked"}`)
)

// setReconnecting marks the session as waiting for its connection, with a
// line in the output, and tells the page.
func (t *termSession) setReconnecting() {
	t.mu.Lock()
	t.reconnecting = true
	if t.conn != nil {
		writeTimeout(t.conn, websocket.MessageText, msgReconnecting)
	}
	t.mu.Unlock()
	t.output([]byte("\r\n\x1b[2m[connection to the server lost — reconnecting…]\x1b[0m\r\n"))
}

// replaceShell puts the new shell in the session after a reconnect. It
// reports false (closing sh) if the session was closed meanwhile.
func (t *termSession) replaceShell(sh *sshx.Shell) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		sh.Close()
		return false
	}
	t.shell, t.reconnecting = sh, false
	if t.conn != nil {
		writeTimeout(t.conn, websocket.MessageText, msgReconnected)
	}
	return true
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

// Results of attach.
const (
	attached = iota
	attachEnded
	attachLocked
)

// attach connects a page, replacing any page already attached, and replays
// the recent output. It refuses if the session has already ended, or if
// unlocked reports false.
//
// unlocked is checked under t.mu, which hide also holds: the vault is
// marked locked before the pages are hidden, so either attach sees the lock
// or hide runs after it and detaches c again. A page can't stay attached
// while TunnelTab is locked.
func (t *termSession) attach(c *websocket.Conn, unlocked func() bool) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ended {
		return attachEnded
	}
	if !unlocked() {
		return attachLocked
	}
	if t.conn != nil {
		closeAsync(t.conn, "opened in another tab")
	}
	t.conn = c
	if writeTimeout(c, websocket.MessageText, []byte(`{"type":"attached"}`)) != nil ||
		(len(t.scroll) > 0 && writeTimeout(c, websocket.MessageBinary, t.scroll) != nil) ||
		(t.reconnecting && writeTimeout(c, websocket.MessageText, msgReconnecting) != nil) {
		c.CloseNow()
		t.conn = nil
	}
	return attached
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
		writeTimeout(t.conn, websocket.MessageText, msgLocked)
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

// watchClient records that a dashboard tab's event stream is open, and
// returns a function to call when it closes.
func (s *Server) watchClient(id string) func() {
	s.terms.mu.Lock()
	c := s.terms.clients[id]
	if c == nil {
		c = &termClient{}
		s.terms.clients[id] = c
	}
	c.streams++
	s.terms.mu.Unlock()
	return func() {
		s.terms.mu.Lock()
		c.streams--
		c.lastSeen = s.now()
		s.terms.mu.Unlock()
	}
}

// clientPresent reports whether the dashboard tab id has an event stream
// open, or had one within the grace period (a reload).
func (s *Server) clientPresent(id string, now time.Time) bool {
	if id == "" {
		return false
	}
	s.terms.mu.Lock()
	defer s.terms.mu.Unlock()
	c := s.terms.clients[id]
	return c != nil && (c.streams > 0 || now.Sub(c.lastSeen) <= terminalDetachGrace)
}

func (s *Server) reapTerminalsOnce() {
	now := s.now()
	s.terms.mu.Lock()
	for h, tk := range s.terms.tickets {
		if now.After(tk.expires) {
			delete(s.terms.tickets, h)
		}
	}
	for id, c := range s.terms.clients {
		if c.streams == 0 && now.Sub(c.lastSeen) > terminalDetachGrace {
			delete(s.terms.clients, id)
		}
	}
	s.terms.mu.Unlock()
	for _, t := range s.sessionsSnapshot() {
		ownerHere := s.clientPresent(t.owner, now)
		t.mu.Lock()
		abandoned := t.conn == nil && t.watchers == 0 && !ownerHere && !t.ended && now.Sub(t.lastSeen) > terminalDetachGrace
		t.mu.Unlock()
		if abandoned {
			s.log.Info("closing abandoned terminal", "server", t.serverID)
			t.close()
		}
	}
}

// terminalInfo describes an open terminal session for the dashboard.
type terminalInfo struct {
	ID       string    `json:"id"`
	ServerID string    `json:"serverId"`
	OpenedAt time.Time `json:"openedAt"`
	Attached bool      `json:"attached"`         // a page is showing it right now
	Client   string    `json:"client,omitempty"` // the dashboard tab that shows it
}

// terminalList lists the open terminal sessions, oldest first.
func (s *Server) terminalList() []terminalInfo {
	list := []terminalInfo{}
	for _, t := range s.sessionsSnapshot() {
		t.mu.Lock()
		if !t.ended {
			list = append(list, terminalInfo{ID: t.id, ServerID: t.serverID, OpenedAt: t.openedAt, Attached: t.conn != nil, Client: t.owner})
		}
		t.mu.Unlock()
	}
	sort.Slice(list, func(i, j int) bool { return list[i].OpenedAt.Before(list[j].OpenedAt) })
	return list
}
