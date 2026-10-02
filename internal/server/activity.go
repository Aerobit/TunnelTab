package server

import (
	"sync"
	"time"

	"github.com/Aerobit/TunnelTab/internal/sshx"
)

// The dashboard's "Recent activity": a short log of connection, tunnel and
// terminal events. It holds IDs and states only (the dashboard looks the
// names up in the vault data), lives in memory only — it is never written to
// disk — and is gone when TunnelTab quits.

const activityMax = 200

type activityEntry struct {
	At         time.Time `json:"at"`
	Kind       string    `json:"kind"` // "server", "forward", "terminal" or "discover"
	ID         string    `json:"id"`   // server, service or terminal ID; a new one per service search
	ServerID   string    `json:"serverId"`
	State      string    `json:"state"`
	Error      string    `json:"error,omitempty"`
	Reconnects int       `json:"reconnects,omitempty"` // server "connected": >0 means a reconnect
	Reason     string    `json:"reason,omitempty"`     // sshx.ErrorKind of Error
}

type activityEvent struct {
	Type string `json:"type"` // "activity"
	activityEntry
}

// activityStates are the states worth a line in the log, per kind.
var activityStates = map[string]map[string]bool{
	"server": {
		string(sshx.StateConnected): true, string(sshx.StateReconnecting): true, string(sshx.StateFailed): true,
		string(sshx.StateStopped): true, string(sshx.StatePaused): true,
	},
	"forward":  {string(sshx.StateActive): true, string(sshx.StateStopped): true, string(sshx.StateFailed): true},
	"terminal": {"opened": true, "ended": true},
	"discover": {"searched": true, "failed": true},
}

type activityLog struct {
	mu      sync.Mutex
	entries []activityEntry   // oldest first
	last    map[string]string // kind + ID → last recorded state
}

// add records e if its state is worth showing and differs from the last
// one recorded for the same item. It reports whether e was recorded.
func (a *activityLog) add(e activityEntry) bool {
	if !activityStates[e.Kind][e.State] {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.last == nil {
		a.last = map[string]string{}
	}
	key := e.Kind + "/" + e.ID
	if a.last[key] == e.State {
		return false
	}
	a.last[key] = e.State
	if e.Kind == "terminal" && e.State == "ended" {
		delete(a.last, key) // terminal IDs are never reused
	}
	if len(a.entries) == activityMax {
		copy(a.entries, a.entries[1:])
		a.entries = a.entries[:activityMax-1]
	}
	a.entries = append(a.entries, e)
	return true
}

// list returns a copy of the log, oldest first.
func (a *activityLog) list() []activityEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]activityEntry{}, a.entries...)
}

// recordActivity logs e and, if it was recorded, sends it to the dashboards.
func (s *Server) recordActivity(e activityEntry) {
	e.At = s.now()
	if s.activity.add(e) {
		s.events.publish(activityEvent{Type: "activity", activityEntry: e})
	}
}

// recordTunnelActivity logs a connection or tunnel event from the SSH engine.
func (s *Server) recordTunnelActivity(e sshx.Event) {
	s.recordActivity(activityEntry{Kind: e.Kind, ID: e.ID, ServerID: e.ServerID, State: string(e.State),
		Error: e.Error, Reconnects: e.Reconnects, Reason: e.Reason})
}
