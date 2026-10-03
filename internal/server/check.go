package server

import (
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Aerobit/TunnelTab/internal/model"
	"github.com/Aerobit/TunnelTab/internal/webcheck"
)

// Service checks: does the app behind a service answer? One HEAD request
// (internal/webcheck) through the server's SSH connection to the service's
// address, the way its tunnel reaches it. Only after a click: Start (checked
// once the tunnel is up), Check, Open when the last check failed, and Find
// services (each candidate). Never in the background. Results are kept in
// memory only.

// checkTimeout bounds each attempt (https, then http).
const checkTimeout = 4 * time.Second

type checkReading struct {
	webcheck.Result
	At time.Time `json:"at"`
}

type checkEvent struct {
	Type      string        `json:"type"` // "check"
	ServiceID string        `json:"serviceId"`
	Check     *checkReading `json:"check"` // nil: no result (the service changed)
}

type checkStore struct {
	mu       sync.Mutex
	readings map[string]checkReading // by service ID
}

// checkService checks one service and records the result, unless the
// service was edited or deleted since version (serviceVersion) was taken:
// then the result describes settings that no longer exist, and ok is false.
func (s *Server) checkService(svc model.Service, version uint64) (reading checkReading, ok bool, err error) {
	var res webcheck.Result
	err = s.mgr.Through(svc.ServerID, func(dial func(string, int) (net.Conn, error)) {
		res = webcheck.Check(func() (net.Conn, error) { return dial(svc.RemoteHost, svc.RemotePort) }, svc.Path, checkTimeout)
	})
	if err != nil {
		return checkReading{}, false, err
	}
	reading = checkReading{Result: res, At: s.now()}
	// Stored and published under the lock dropCheck takes, so a result can't
	// land (or reach the dashboards) after the service changed.
	s.checks.mu.Lock()
	defer s.checks.mu.Unlock()
	if s.serviceVersion(svc.ID) != version {
		s.log.Info("service check discarded: the service changed", "service", svc.ID)
		return checkReading{}, false, nil
	}
	s.log.Info("service checked", "service", svc.ID, "state", string(res.State))
	s.checks.readings[svc.ID] = reading
	s.events.publish(checkEvent{Type: "check", ServiceID: svc.ID, Check: &reading})
	return reading, true, nil
}

// dropCheck forgets a service's result (it was edited or deleted; see
// serviceChanged).
func (s *Server) dropCheck(id string) {
	s.checks.mu.Lock()
	defer s.checks.mu.Unlock()
	if _, had := s.checks.readings[id]; had {
		delete(s.checks.readings, id)
		s.events.publish(checkEvent{Type: "check", ServiceID: id})
	}
}

func (s *Server) checkReadings() map[string]checkReading {
	s.checks.mu.Lock()
	defer s.checks.mu.Unlock()
	out := make(map[string]checkReading, len(s.checks.readings))
	for id, r := range s.checks.readings {
		out[id] = r
	}
	return out
}

// handleCheckService (the Check button) checks a service now. It may
// connect to the server, like Start.
func (s *Server) handleCheckService(w http.ResponseWriter, r *http.Request) {
	version := s.serviceVersion(r.PathValue("id")) // before reading the service
	var svc model.Service
	if err := s.view(func(d *model.Data) error {
		var ok bool
		if svc, ok = d.Service(r.PathValue("id")); !ok {
			return model.ErrNotFound
		}
		return nil
	}); err != nil {
		s.writeDataError(w, err)
		return
	}
	reading, ok, err := s.checkService(svc, version)
	if err != nil {
		s.writeSSHError(w, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"check": nil}) // the service changed meanwhile
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"check": reading})
}
