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

// Opt-in server health: for servers where it is switched on, while the
// server is connected (for a tunnel or terminal — it never connects by
// itself) and a dashboard is open, health.Command runs every healthEvery.
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

// checkAllHealth checks every server with health switched on, if a
// dashboard is open (nobody to show it to otherwise) and the vault is
// unlocked (the setting is in the vault).
func (s *Server) checkAllHealth() {
	if s.events.count() == 0 {
		return
	}
	for _, id := range s.healthServers() {
		go s.checkHealth(id)
	}
}

// healthServers lists the servers with health switched on.
func (s *Server) healthServers() []string {
	var ids []string
	s.peek(func(d *model.Data) error {
		for _, srv := range d.Servers {
			if srv.HealthEnabled {
				ids = append(ids, srv.ID)
			}
		}
		return nil
	})
	return ids
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
	// Switched off while the check ran: drop it.
	if !s.healthEnabled(id) {
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
	} else {
		s.setHealth(id, nil)
	}
	w.WriteHeader(http.StatusNoContent)
}

// onServerConnected checks a newly connected server's health right away,
// if it is switched on, instead of waiting for the next round.
func (s *Server) onServerConnected(id string) {
	if s.events.count() > 0 && s.healthEnabled(id) {
		go s.checkHealth(id)
	}
}
