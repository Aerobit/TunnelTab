package server

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Aerobit/TunnelTab/internal/discover"
	"github.com/Aerobit/TunnelTab/internal/model"
	"github.com/Aerobit/TunnelTab/internal/webcheck"
)

// "Find services": on the user's click (never by itself), run the constant
// discover.Command on a server and return what it found. Nothing is saved:
// the dashboard shows the list, and only the services the user ticks are
// added, through handleAddServices.

// discoverTimeout bounds one scan, connecting excluded.
const discoverTimeout = 20 * time.Second

// foundService is a candidate plus whether the server already has it.
type foundService struct {
	discover.Candidate
	Added bool `json:"added,omitempty"` // a service for this port exists
	// Check: whether it answers like a web page; nil when not checked
	// (ports known not to be web pages, or beyond maxCandidateChecks).
	Check *webcheck.Result `json:"check,omitempty"`
}

func (s *Server) handleDiscover(w http.ResponseWriter, r *http.Request) {
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
	// The scan and the checks of what it found share one connection.
	var (
		runErr error
		res    discover.Result
		err    error
		checks []*webcheck.Result
	)
	if err := s.mgr.Through(id, func(dial func(string, int) (net.Conn, error)) {
		var out []byte
		if out, runErr = s.mgr.Run(id, discover.Command, discover.MaxOutput, discoverTimeout); runErr != nil {
			return
		}
		if res, err = discover.Parse(out); err == nil {
			checks = checkCandidates(dial, res.Candidates)
		}
	}); err != nil {
		s.writeSSHError(w, err)
		return
	}
	if runErr != nil {
		s.writeSSHError(w, runErr)
		return
	}
	if err != nil {
		s.recordActivity(activityEntry{Kind: "discover", ID: model.NewID(), ServerID: id, State: "failed"})
		msg := "Couldn't read what the server printed."
		if errors.Is(err, discover.ErrNoPorts) {
			msg = "Couldn't list the server's open ports: it has neither ss nor netstat. You can still add services by hand."
		}
		writeError(w, http.StatusUnprocessableEntity, "discover_failed", msg)
		return
	}
	s.log.Info("searched for services", "server", id, "found", len(res.Candidates))
	s.recordActivity(activityEntry{Kind: "discover", ID: model.NewID(), ServerID: id, State: "searched"})

	var services []model.Service
	if err := s.view(func(d *model.Data) error {
		for _, x := range d.Services {
			if x.ServerID == id {
				services = append(services, x)
			}
		}
		return nil
	}); err != nil {
		s.writeDataError(w, err)
		return
	}
	found := make([]foundService, 0, len(res.Candidates))
	for i, c := range res.Candidates {
		f := foundService{Candidate: c, Check: checks[i]}
		for _, x := range services {
			if x.RemotePort == c.Port && sameHost(x.RemoteHost, c.Host) {
				f.Added = true
				break
			}
		}
		found = append(found, f)
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": found, "docker": res.Docker, "truncated": res.Truncated})
}

// maxCandidateChecks and candidateCheckers bound the checks after a scan
// (most likely web pages first, as Parse sorts them).
const (
	maxCandidateChecks = 24
	candidateCheckers  = 8
)

// checkCandidates checks, in parallel, whether each candidate that may be a
// web page answers like one (and on https or http). The result has one
// entry per candidate; nil for those not checked.
func checkCandidates(dial func(string, int) (net.Conn, error), cands []discover.Candidate) []*webcheck.Result {
	out := make([]*webcheck.Result, len(cands))
	sem := make(chan struct{}, candidateCheckers)
	var wg sync.WaitGroup
	n := 0
	for i, c := range cands {
		if c.Kind == discover.KindOther || n == maxCandidateChecks {
			continue
		}
		n++
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r := webcheck.Check(func() (net.Conn, error) { return dial(c.Host, c.Port) }, c.Path, checkTimeout)
			out[i] = &r
		}()
	}
	wg.Wait()
	return out
}

// sameHost compares remote hosts, counting every loopback name as one.
func sameHost(a, b string) bool {
	key := func(h string) string {
		h = strings.ToLower(h)
		if h == "localhost" {
			return "loopback"
		}
		if ip := net.ParseIP(h); ip != nil {
			if ip.IsLoopback() {
				return "loopback"
			}
			return ip.String()
		}
		return h
	}
	return key(a) == key(b)
}

// maxAddServices is how many services one "Add selected" may add.
const maxAddServices = discover.MaxCandidates

// handleAddServices adds several services to one server in one vault
// update: all of them, or none if any is invalid.
func (s *Server) handleAddServices(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Services []model.Service `json:"services"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.Services) == 0 || len(req.Services) > maxAddServices {
		writeError(w, http.StatusBadRequest, "invalid", fmt.Sprintf("add between 1 and %d services", maxAddServices))
		return
	}
	out := make([]model.Service, 0, len(req.Services))
	if err := s.update(func(d *model.Data) error {
		if _, ok := d.Server(id); !ok {
			return model.ErrNotFound
		}
		for i, in := range req.Services {
			in.ServerID = id
			svc, err := d.AddService(in)
			if err != nil {
				var ve *model.ValidationError
				if errors.As(err, &ve) {
					return &model.ValidationError{Field: fmt.Sprintf("services.%d.%s", i, ve.Field), Msg: ve.Msg}
				}
				return err
			}
			out = append(out, svc)
		}
		return nil
	}); err != nil {
		s.writeDataError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}
