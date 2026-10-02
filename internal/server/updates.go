package server

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"time"

	"github.com/Aerobit/TunnelTab/internal/update"
)

func (s *Server) checkUpdate(ctx context.Context) (update.Result, error) {
	url := s.cfg.UpdateURL
	if url == "" {
		url = update.DefaultURL
	}
	client := &http.Client{Timeout: 20 * time.Second}
	res, err := update.Check(ctx, client, url, s.cfg.Version)
	if err == nil && !s.canInstall() {
		res.CanInstall = false
	}
	return res, err
}

// canInstall reports whether this build can install updates itself.
func (s *Server) canInstall() bool {
	if s.cfg.AppDir == "" || s.cfg.OnUpdateInstalled == nil || update.ExeName() == "" {
		return false
	}
	_, err := s.releaseKey()
	return err == nil
}

func (s *Server) releaseKey() ([]byte, error) {
	if s.cfg.ReleaseKey != nil {
		return s.cfg.ReleaseKey, nil
	}
	return update.PublicKey()
}

// handleCheckUpdates asks GitHub for the latest release. It runs only when
// the user clicks "Check for updates"; TunnelTab never checks by itself.
func (s *Server) handleCheckUpdates(w http.ResponseWriter, r *http.Request) {
	res, err := s.checkUpdate(r.Context())
	switch {
	case errors.Is(err, update.ErrNoRelease):
		writeJSON(w, http.StatusOK, map[string]any{"current": s.cfg.Version, "noRelease": true})
	case err != nil:
		s.log.Info("update check failed")
		writeError(w, http.StatusBadGateway, "update_check_failed", err.Error())
	default:
		s.log.Info("update check", "latest", res.Latest, "newer", res.Newer, "canInstall", res.CanInstall)
		writeJSON(w, http.StatusOK, res)
	}
}

// updateEvent tells every open dashboard how far "Update now" has got:
// update.Progress steps, plus "checking" first, then "restarting" when
// installed, or "failed".
type updateEvent struct {
	Type    string `json:"type"` // "update"
	Version string `json:"version,omitempty"`
	update.Progress
}

func (s *Server) publishUpdate(version string, p update.Progress) {
	s.events.publish(updateEvent{Type: "update", Version: version, Progress: p})
}

// handleInstallUpdate is "Update now": it checks for the latest release
// again, downloads and verifies it (update.Download), replaces the program
// files (update.Install), answers, and then restarts TunnelTab through
// OnUpdateInstalled. It runs only when the user clicks.
func (s *Server) handleInstallUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.installing.CompareAndSwap(false, true) {
		writeError(w, http.StatusConflict, "update_busy", "An update is already being installed.")
		return
	}
	done := false
	defer func() {
		if !done {
			s.installing.Store(false)
			s.publishUpdate("", update.Progress{Step: "failed"})
		}
	}()

	s.publishUpdate("", update.Progress{Step: "checking"})

	res, err := s.checkUpdate(r.Context())
	if err != nil {
		s.log.Info("update check failed")
		writeError(w, http.StatusBadGateway, "update_failed", "Can't check for updates: "+err.Error())
		return
	}
	if !res.CanInstall {
		writeError(w, http.StatusConflict, "update_unavailable",
			"There is no update that TunnelTab can install by itself. Use the release page instead.")
		return
	}
	key, _ := s.releaseKey()
	s.log.Info("installing update", "version", res.Latest)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 10 * time.Minute}
	st, err := update.Download(ctx, client, res, key, filepath.Join(s.cfg.AppDir, ".update"), s.cfg.ExeName,
		func(p update.Progress) { s.publishUpdate(res.Latest, p) })
	if err == nil {
		s.publishUpdate(res.Latest, update.Progress{Step: update.StepInstalling})
		err = update.Install(st, s.cfg.AppDir)
	}
	if err != nil {
		s.log.Warn("update not installed", "version", res.Latest, "error", err)
		writeError(w, http.StatusBadGateway, "update_failed", "The update was not installed: "+err.Error()+".")
		return
	}
	s.log.Info("update installed; restarting", "version", res.Latest)
	done = true
	s.publishUpdate(res.Latest, update.Progress{Step: "restarting"})
	writeJSON(w, http.StatusOK, map[string]string{"status": "restarting", "version": res.Latest})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go s.cfg.OnUpdateInstalled()
}
