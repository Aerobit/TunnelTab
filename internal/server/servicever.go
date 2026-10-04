package server

import (
	"slices"
	"sync"

	"github.com/Aerobit/TunnelTab/internal/model"
	"github.com/Aerobit/TunnelTab/internal/sshx"
)

// Work started from a service's settings — a check, a tunnel start — can
// finish after the service was edited or deleted, and must not then record
// or publish what it got for the old settings. Each service has a change
// count: the work takes it *before* reading the service and keeps its result
// only if the count is still the same. Edits and deletes raise the count
// (serviceChanged) after saving, so a read that saw the old settings always
// comes with the old count.

type serviceVersions struct {
	mu sync.Mutex
	n  map[string]uint64 // by service ID: edits and deletes so far
}

// serviceVersion is the service's change count; see above.
func (s *Server) serviceVersion(id string) uint64 {
	s.versions.mu.Lock()
	defer s.versions.mu.Unlock()
	return s.versions.n[id]
}

// allServiceVersions is serviceVersion for every service at once (for work
// on a snapshot of all services: auto-start).
func (s *Server) allServiceVersions() map[string]uint64 {
	s.versions.mu.Lock()
	defer s.versions.mu.Unlock()
	out := make(map[string]uint64, len(s.versions.n))
	for id, n := range s.versions.n {
		out[id] = n
	}
	return out
}

// serviceChanged is called once an edit or delete of the service is saved:
// it raises the change count, then drops the check result and stops the
// tunnel. The count is raised first (and its lock released, as
// StartForwardIf asks it under the manager's lock): a start that read the
// old settings is then either published before StopForward, which stops
// it, or refused.
func (s *Server) serviceChanged(id string) {
	s.versions.mu.Lock()
	s.versions.n[id]++
	s.versions.mu.Unlock()
	s.dropCheck(id)
	s.mgr.StopForward(id)
}

// servicesOn lists the services of the given servers, for a delete that
// removes them with their server or project: call it inside the update,
// before the delete, and pass the result to serviceChanged afterwards.
// StopServer alone doesn't stop a start that hasn't reserved its service
// yet, and a connection kept open by other work (a check, Find services)
// would let that start through.
func servicesOn(d *model.Data, serverIDs ...string) []string {
	var out []string
	for _, svc := range d.Services {
		if slices.Contains(serverIDs, svc.ServerID) {
			out = append(out, svc.ID)
		}
	}
	return out
}

// startForward starts the service's tunnel unless the service changed since
// version was taken (sshx.ErrStartCancelled).
func (s *Server) startForward(svc model.Service, version uint64) (sshx.ForwardStatus, error) {
	if testHookStarting != nil {
		testHookStarting(svc.ID)
	}
	return s.mgr.StartForwardIf(svc, func() bool { return s.serviceVersion(svc.ID) == version })
}

// testHookStarting, if set (by tests), runs between reading a service and
// starting its tunnel.
var testHookStarting func(serviceID string)
