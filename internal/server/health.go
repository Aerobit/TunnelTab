package server

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/Aerobit/TunnelTab/internal/health"
	"github.com/Aerobit/TunnelTab/internal/model"
	"github.com/Aerobit/TunnelTab/internal/sshx"
)

// Server health: health.Command runs every healthEvery while a dashboard is
// open, for servers that are connected and either
//   - have health switched on in Settings (it never connects by itself: only
//     while a tunnel or terminal keeps the server connected), or
//   - were connected with the Health card's Connect button (sshx.Manager.Hold,
//     dropped by Disconnect or once no dashboard is open).
// Readings are kept in memory only.

var (
	healthEvery   = 30 * time.Second
	healthTimeout = 10 * time.Second
)

type healthReading struct {
	Health *health.Health `json:"health,omitempty"`
	Error  string         `json:"error,omitempty"` // e.g. "Server health needs a Linux server."
	At     time.Time      `json:"at"`
}

type healthEvent struct {
	Type     string         `json:"type"` // "health"
	ServerID string         `json:"serverId"`
	Reading  *healthReading `json:"reading"` // nil: no reading (switched off or disconnected)
}

type healthStore struct {
	mu       sync.Mutex
	readings map[string]healthReading // by server ID
	running  map[string]bool          // a check is in progress
}

// runHealth checks every enabled server each healthEvery until stop closes.
func (s *Server) runHealth(stop <-chan struct{}) {
	t := time.NewTicker(healthEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			s.checkAllHealth()
		}
	}
}

// checkAllHealth checks every server with health switched on or held, if a
// dashboard is open (nobody to show it to otherwise) and the vault is
// unlocked. With no dashboard open, held servers are let go: Connect is
// for looking at them.
func (s *Server) checkAllHealth() {
	if s.events.count() == 0 {
		for _, id := range s.mgr.Held() {
			s.log.Info("disconnecting: no dashboard open", "server", id)
			s.mgr.Unhold(id)
		}
		return
	}
	for _, id := range s.healthServers() {
		go s.checkHealth(id)
	}
}

// healthServers lists the servers with health switched on or held; none
// while the vault is locked.
func (s *Server) healthServers() []string {
	held := s.mgr.Held()
	var ids []string
	if err := s.peek(func(d *model.Data) error {
		ids = append(ids, held...)
		for _, srv := range d.Servers {
			if srv.HealthEnabled && !s.mgr.IsHeld(srv.ID) {
				ids = append(ids, srv.ID)
			}
		}
		return nil
	}); err != nil {
		return nil
	}
	return ids
}

// healthWanted reports whether the server's health should be read: switched
// on or held.
func (s *Server) healthWanted(id string) bool {
	return s.mgr.IsHeld(id) || s.healthEnabled(id)
}

func (s *Server) healthEnabled(id string) bool {
	on := false
	s.peek(func(d *model.Data) error {
		srv, ok := d.Server(id)
		on = ok && srv.HealthEnabled
		return nil
	})
	return on
}

// checkHealth reads one server's health over its existing connection.
func (s *Server) checkHealth(id string) {
	s.health.mu.Lock()
	if s.health.running[id] {
		s.health.mu.Unlock()
		return
	}
	s.health.running[id] = true
	s.health.mu.Unlock()
	defer func() {
		s.health.mu.Lock()
		delete(s.health.running, id)
		s.health.mu.Unlock()
	}()

	out, err := s.mgr.RunIfConnected(id, health.Command, health.MaxOutput, healthTimeout)
	if errors.Is(err, sshx.ErrNotConnected) {
		s.setHealth(id, nil) // shown again once it connects
		return
	}
	reading := healthReading{At: s.now()}
	if err == nil {
		var h health.Health
		if h, err = health.Parse(out); err == nil {
			reading.Health = &h
		}
	}
	switch {
	case errors.Is(err, health.ErrNotLinux):
		reading.Error = "Server health needs a Linux server."
	case err != nil:
		s.log.Info("health check failed", "server", id, "reason", sshx.ErrorKind(err))
		reading.Error = "Couldn't read the server's health."
	}
	// Switched off or disconnected while the check ran: drop it.
	if !s.healthWanted(id) {
		s.setHealth(id, nil)
		return
	}
	s.setHealth(id, &reading)
}

// setHealth stores a reading (nil removes it) and tells the dashboards.
func (s *Server) setHealth(id string, r *healthReading) {
	s.health.mu.Lock()
	_, had := s.health.readings[id]
	if r == nil {
		delete(s.health.readings, id)
	} else {
		s.health.readings[id] = *r
	}
	s.health.mu.Unlock()
	if r != nil || had {
		s.events.publish(healthEvent{Type: "health", ServerID: id, Reading: r})
	}
}

func (s *Server) healthReadings() map[string]healthReading {
	s.health.mu.Lock()
	defer s.health.mu.Unlock()
	out := make(map[string]healthReading, len(s.health.readings))
	for id, r := range s.health.readings {
		out[id] = r
	}
	return out
}

// handleServerHealth switches a server's health check on or off. On, it is
// checked right away (if connected); off, its reading is dropped at once.
func (s *Server) handleServerHealth(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	if err := s.update(func(d *model.Data) error { return d.SetServerHealth(id, req.Enabled) }); err != nil {
		s.writeDataError(w, err)
		return
	}
	s.log.Info("server health switched", "server", id, "on", req.Enabled)
	if req.Enabled {
		go s.checkHealth(id)
	} else if !s.mgr.IsHeld(id) {
		s.setHealth(id, nil)
	}
	w.WriteHeader(http.StatusNoContent)
}

// onServerConnected checks a newly connected server's health right away,
// if it is wanted, instead of waiting for the next round.
func (s *Server) onServerConnected(id string) {
	if s.events.count() > 0 && s.healthWanted(id) {
		go s.checkHealth(id)
	}
}

// handleServerConnect (the Health card's Connect button) connects to the
// server and keeps it connected, reading its health, until Disconnect or
// until no dashboard is open. It may connect, so only on the user's click.
func (s *Server) handleServerConnect(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.view(func(d *model.Data) error {
		if _, ok := d.Server(id); !ok {
			return model.ErrNotFound
		}
		return nil
	}); err != nil {
		s.writeDataError(w, err)
		return
	}
	if err := s.mgr.Hold(id); err != nil {
		s.writeSSHError(w, err)
		return
	}
	s.log.Info("server connected from the dashboard", "server", id)
	go s.checkHealth(id)
	w.WriteHeader(http.StatusNoContent)
}

// handleServerDisconnect drops the hold made by Connect. The connection
// closes unless a tunnel or terminal still uses it; the health reading stays
// only if health is switched on in Settings.
func (s *Server) handleServerDisconnect(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mgr.Unhold(id)
	s.log.Info("server disconnected from the dashboard", "server", id)
	if !s.healthEnabled(id) {
		s.setHealth(id, nil)
	}
	w.WriteHeader(http.StatusNoContent)
}
