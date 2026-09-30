package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/Aerobit/TunnelTab/internal/update"
)

// handleCheckUpdates asks GitHub for the latest release. It runs only when
// the user clicks "Check for updates"; TunnelTab never checks by itself.
func (s *Server) handleCheckUpdates(w http.ResponseWriter, r *http.Request) {
	url := s.cfg.UpdateURL
	if url == "" {
		url = update.DefaultURL
	}
	client := &http.Client{Timeout: 20 * time.Second}
	res, err := update.Check(r.Context(), client, url, s.cfg.Version)
	switch {
	case errors.Is(err, update.ErrNoRelease):
		writeJSON(w, http.StatusOK, map[string]any{"current": s.cfg.Version, "noRelease": true})
	case err != nil:
		s.log.Info("update check failed")
		writeError(w, http.StatusBadGateway, "update_check_failed", err.Error())
	default:
		s.log.Info("update check", "latest", res.Latest, "newer", res.Newer)
		writeJSON(w, http.StatusOK, res)
	}
}
