package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/Aerobit/TunnelTab/internal/config"
	"github.com/Aerobit/TunnelTab/internal/model"
	"github.com/Aerobit/TunnelTab/internal/sshx"
	"github.com/Aerobit/TunnelTab/internal/vault"
)

// routes registers every endpoint. See docs/ARCHITECTURE.md for the API
// reference.
func (s *Server) routes(mux *http.ServeMux) {
	mux.Handle("GET /", s.static)

	// No session needed: these create one.
	mux.HandleFunc("POST /api/session", s.handleSession)
	mux.HandleFunc("POST /api/instance/launch", s.handleInstanceLaunch)

	a := func(pattern string, fn http.HandlerFunc) { mux.HandleFunc(pattern, s.authed(fn)) }
	a("GET /api/state", s.handleState)
	a("GET /api/events", s.handleEvents)
	a("POST /api/quit", s.handleQuit)
	a("POST /api/touch", s.handleTouch)
	a("GET /api/traffic", s.handleTraffic)
	a("POST /api/updates/check", s.handleCheckUpdates)
	a("POST /api/updates/install", s.handleInstallUpdate)
	a("GET /api/settings", s.handleGetSettings)
	a("PUT /api/settings", s.handlePutSettings)

	a("POST /api/vault/create", s.handleVaultCreate)
	a("POST /api/vault/unlock", s.handleVaultUnlock)
	a("POST /api/vault/lock", s.handleVaultLock)
	a("POST /api/vault/password", s.handleVaultPassword)

	a("GET /api/data", s.handleData)
	a("POST /api/projects", s.handleAddProject)
	a("PUT /api/projects/{id}", s.handleUpdateProject)
	a("DELETE /api/projects/{id}", s.handleDeleteProject)
	a("POST /api/servers", s.handleAddServer)
	a("PUT /api/servers/{id}", s.handleUpdateServer)
	a("DELETE /api/servers/{id}", s.handleDeleteServer)
	a("POST /api/servers/{id}/move", s.handleMoveServer)
	a("POST /api/servers/{id}/clear-passphrase", s.handleClearPassphrase)
	a("PUT /api/servers/{id}/notes", s.handleServerNotes)
	a("PUT /api/servers/{id}/health", s.handleServerHealth)
	a("POST /api/servers/{id}/connect", s.handleServerConnect)
	a("DELETE /api/servers/{id}/connect", s.handleServerDisconnect)
	a("POST /api/servers/{id}/test", s.handleTestServer)
	a("POST /api/servers/{id}/discover", s.handleDiscover)
	a("POST /api/servers/{id}/services", s.handleAddServices)
	a("POST /api/services", s.handleAddService)
	a("PUT /api/services/{id}", s.handleUpdateService)
	a("DELETE /api/services/{id}", s.handleDeleteService)
	a("POST /api/services/{id}/start", s.handleStartService)
	a("POST /api/services/{id}/stop", s.handleStopService)
	a("POST /api/hostkeys/confirm", s.handleConfirmHostKey)
	a("PUT /api/projects/order", s.handleOrderProjects)
	a("PUT /api/projects/{id}/servers/order", s.handleOrderServers)
	a("PUT /api/servers/{id}/services/order", s.handleOrderServices)
	a("POST /api/hostkeys/forget", s.handleForgetHostKey)
	a("POST /api/terminals", s.handleOpenTerminal)
	a("POST /api/terminals/{id}/attach", s.handleAttachTerminal)
	a("DELETE /api/terminals/{id}", s.handleCloseTerminal)
	// Authenticated by its one-time ticket instead of the session header.
	mux.HandleFunc("GET /api/terminals/connect", s.handleConnectTerminal)
}

// --- JSON helpers -----------------------------------------------------------

type apiError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, apiError{Error: code, Message: msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "request is too large")
		} else {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		}
		return false
	}
	return true
}

// writeDataError maps vault/model errors to HTTP responses.
func (s *Server) writeDataError(w http.ResponseWriter, err error) {
	var ve *model.ValidationError
	switch {
	case errors.As(err, &ve):
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid", Message: err.Error(), Field: ve.Field})
	case errors.Is(err, model.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, vault.ErrLocked):
		writeError(w, http.StatusLocked, "locked", "the vault is locked")
	case errors.Is(err, errNoVault):
		writeError(w, http.StatusConflict, "no_vault", "create a vault first")
	default:
		s.log.Error("request failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "something went wrong; see the log file")
	}
}

var errNoVault = errors.New("no vault")

// view and update run fn against the vault, reporting "no vault" if none.
func (s *Server) view(fn func(d *model.Data) error) error {
	v := s.currentVault()
	if v == nil {
		return errNoVault
	}
	return v.View(fn)
}

// peek is view for background work: it doesn't postpone auto-lock.
func (s *Server) peek(fn func(d *model.Data) error) error {
	v := s.currentVault()
	if v == nil {
		return errNoVault
	}
	return v.Peek(fn)
}

func (s *Server) update(fn func(d *model.Data) error) error {
	v := s.currentVault()
	if v == nil {
		return errNoVault
	}
	if err := v.Update(fn); err != nil {
		return err
	}
	s.events.publish(dataEvent{Type: "data"})
	return nil
}

// --- Session and instance ---------------------------------------------------

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Launch string `json:"launch"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	sess, ok := s.redeemLaunchToken(req.Launch)
	if !ok {
		writeError(w, http.StatusUnauthorized, "bad_launch_token", "this link has expired or was already used: start TunnelTab again")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"session": sess})
}

// handleInstanceLaunch lets a second launch of the program get a fresh
// login link from this (running) instance.
func (s *Server) handleInstanceLaunch(w http.ResponseWriter, r *http.Request) {
	if !s.checkInstanceSecret(r.Header.Get(instanceHeader)) {
		writeError(w, http.StatusForbidden, "forbidden", "forbidden")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": s.LaunchURL()})
}

// instanceHeader carries the instance secret on /api/instance/launch.
const instanceHeader = "X-TunnelTab-Instance"

// --- State, settings, quit --------------------------------------------------

func (s *Server) vaultState() string {
	v := s.currentVault()
	switch {
	case v == nil:
		return "none"
	case v.Unlocked():
		return "unlocked"
	default:
		return "locked"
	}
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"vault":          s.vaultState(),
		"version":        s.cfg.Version,
		"unlockWaitMs":   s.unlockWait().Milliseconds(),
		"minPasswordLen": vault.MinPasswordLen,
	})
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	st := s.settings
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var st config.Settings
	if !readJSON(w, r, &st) {
		return
	}
	if err := st.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid", Message: err.Error()})
		return
	}
	if err := config.SaveSettings(s.cfg.Paths.Settings, st); err != nil {
		s.writeDataError(w, err)
		return
	}
	s.mu.Lock()
	restart := st.Port != s.settings.Port
	s.settings = st
	v := s.vault
	s.mu.Unlock()
	if v != nil {
		v.SetAutoLock(time.Duration(st.AutoLockMinutes) * time.Minute)
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": st, "restartRequired": restart})
}

func (s *Server) handleQuit(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "quitting"})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	if s.cfg.OnQuit != nil {
		go s.cfg.OnQuit()
	}
}

// --- Vault ------------------------------------------------------------------

type passwordRequest struct {
	Password string `json:"password"`
}

func (s *Server) handleVaultCreate(w http.ResponseWriter, r *http.Request) {
	var req passwordRequest
	if !readJSON(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.vault != nil {
		writeError(w, http.StatusConflict, "vault_exists", "a vault already exists")
		return
	}
	v, err := vault.Create(s.cfg.Paths.Vault, s.cfg.Paths.VaultBackup, []byte(req.Password), s.vaultOptions())
	switch {
	case errors.Is(err, vault.ErrWeakPassword):
		writeError(w, http.StatusBadRequest, "weak_password", err.Error())
		return
	case err != nil:
		s.log.Error("create vault", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create the vault: "+err.Error())
		return
	}
	v.OnLock(s.onVaultLocked)
	s.vault = v
	s.log.Info("vault created")
	s.events.publish(vaultEvent{Type: "vault", State: "unlocked"})
	writeJSON(w, http.StatusOK, map[string]string{"vault": "unlocked"})
}

func (s *Server) handleVaultUnlock(w http.ResponseWriter, r *http.Request) {
	var req passwordRequest
	if !readJSON(w, r, &req) {
		return
	}
	v := s.currentVault()
	if v == nil {
		writeError(w, http.StatusConflict, "no_vault", "create a vault first")
		return
	}
	s.unlockMu.Lock()
	defer s.unlockMu.Unlock()
	if s.tooManyAttempts(w) {
		return
	}
	err := v.Unlock([]byte(req.Password))
	switch {
	case errors.Is(err, vault.ErrWrongPassword):
		s.unlockFailed()
		s.log.Warn("wrong master password")
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"error": "wrong_password", "message": "Wrong master password. Check Caps Lock and your keyboard layout.", "retryAfterMs": s.unlockWait().Milliseconds(),
		})
		return
	case err != nil:
		s.writeDataError(w, err)
		return
	}
	s.unlockSucceeded()
	s.log.Info("vault unlocked")
	s.mgr.Resume()
	go s.autoStart()
	s.events.publish(vaultEvent{Type: "vault", State: "unlocked"})
	writeJSON(w, http.StatusOK, map[string]string{"vault": "unlocked"})
}

func (s *Server) handleVaultLock(w http.ResponseWriter, r *http.Request) {
	if v := s.currentVault(); v != nil {
		v.Lock()
	}
	writeJSON(w, http.StatusOK, map[string]string{"vault": s.vaultState()})
}

func (s *Server) handleVaultPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	v := s.currentVault()
	if v == nil {
		writeError(w, http.StatusConflict, "no_vault", "create a vault first")
		return
	}
	s.unlockMu.Lock()
	defer s.unlockMu.Unlock()
	if s.tooManyAttempts(w) {
		return
	}
	err := v.ChangePassword([]byte(req.Old), []byte(req.New))
	switch {
	case errors.Is(err, vault.ErrWrongPassword):
		s.unlockFailed()
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"error": "wrong_password", "message": "The current master password is wrong.", "retryAfterMs": s.unlockWait().Milliseconds(),
		})
	case errors.Is(err, vault.ErrWeakPassword):
		writeError(w, http.StatusBadRequest, "weak_password", err.Error())
	case err != nil:
		s.writeDataError(w, err)
	default:
		s.log.Info("master password changed")
		writeJSON(w, http.StatusOK, map[string]string{"status": "changed"})
	}
}

// tooManyAttempts answers 429 if a wrong-password delay is still running.
func (s *Server) tooManyAttempts(w http.ResponseWriter) bool {
	wait := s.unlockWait()
	if wait <= 0 {
		return false
	}
	writeJSON(w, http.StatusTooManyRequests, map[string]any{
		"error": "too_many_attempts", "message": "too many wrong passwords: wait a moment", "retryAfterMs": wait.Milliseconds(),
	})
	return true
}

// autoStart starts services marked "start after unlocking".
func (s *Server) autoStart() {
	var services []model.Service
	s.view(func(d *model.Data) error {
		for _, svc := range d.Services {
			if svc.AutoStart {
				services = append(services, svc)
			}
		}
		return nil
	})
	for _, svc := range services {
		if _, err := s.mgr.StartForward(svc); err != nil {
			s.log.Info("auto-start failed", "service", svc.ID, "reason", sshx.ErrorKind(err))
			s.events.publish(tunnelEvent{Type: "tunnel", Event: sshx.Event{
				Kind: "forward", ID: svc.ID, ServerID: svc.ServerID, State: sshx.StateFailed, Error: err.Error(),
			}})
		}
	}
}

// --- Data -------------------------------------------------------------------

func (s *Server) handleData(w http.ResponseWriter, r *http.Request) {
	var pub *model.PublicData
	if err := s.view(func(d *model.Data) error { pub = d.Public(); return nil }); err != nil {
		s.writeDataError(w, err)
		return
	}
	forwards := s.mgr.Forwards()
	if forwards == nil {
		forwards = []sshx.ForwardStatus{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": pub, "forwards": forwards, "servers": s.mgr.Servers(),
		"terminals": s.terminalList(), "activity": s.activity.list(), "health": s.healthReadings(),
	})
}

func (s *Server) handleAddProject(w http.ResponseWriter, r *http.Request) {
	var in model.Project
	if !readJSON(w, r, &in) {
		return
	}
	var out model.Project
	if err := s.update(func(d *model.Data) (err error) { out, err = d.AddProject(in); return }); err != nil {
		s.writeDataError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	var in model.Project
	if !readJSON(w, r, &in) {
		return
	}
	in.ID = r.PathValue("id")
	var out model.Project
	if err := s.update(func(d *model.Data) (err error) { out, err = d.UpdateProject(in); return }); err != nil {
		s.writeDataError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	var removed []string
	if err := s.update(func(d *model.Data) (err error) { removed, err = d.DeleteProject(r.PathValue("id")); return }); err != nil {
		s.writeDataError(w, err)
		return
	}
	for _, id := range removed {
		s.mgr.StopServer(id)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAddServer(w http.ResponseWriter, r *http.Request) {
	var in model.Server
	if !readJSON(w, r, &in) {
		return
	}
	var out model.Server
	if err := s.update(func(d *model.Data) (err error) { out, err = d.AddServer(in); return }); err != nil {
		s.writeDataError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out.Public())
}

func (s *Server) handleUpdateServer(w http.ResponseWriter, r *http.Request) {
	var in model.Server
	if !readJSON(w, r, &in) {
		return
	}
	in.ID = r.PathValue("id")
	var old, out model.Server
	if err := s.update(func(d *model.Data) (err error) {
		old, _ = d.Server(in.ID)
		out, err = d.UpdateServer(in)
		return
	}); err != nil {
		s.writeDataError(w, err)
		return
	}
	// If the address, username or login changed, the open connection (and
	// its tunnels) no longer matches: close it. A rename keeps it running.
	if old.Host != out.Host || old.Port != out.Port || old.Username != out.Username || old.Auth != out.Auth {
		s.mgr.StopServer(out.ID)
	}
	writeJSON(w, http.StatusOK, out.Public())
}

func (s *Server) handleDeleteServer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.update(func(d *model.Data) error { return d.DeleteServer(id) }); err != nil {
		s.writeDataError(w, err)
		return
	}
	s.mgr.StopServer(id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMoveServer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID string `json:"projectId"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.update(func(d *model.Data) error { return d.MoveServer(r.PathValue("id"), req.ProjectID) }); err != nil {
		s.writeDataError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleClearPassphrase(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.update(func(d *model.Data) error { return d.ClearPassphrase(id) }); err != nil {
		s.writeDataError(w, err)
		return
	}
	s.mgr.StopServer(id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTestServer(w http.ResponseWriter, r *http.Request) {
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
	if err := s.mgr.TestConnection(id); err != nil {
		s.writeSSHError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleAddService(w http.ResponseWriter, r *http.Request) {
	var in model.Service
	if !readJSON(w, r, &in) {
		return
	}
	var out model.Service
	if err := s.update(func(d *model.Data) (err error) { out, err = d.AddService(in); return }); err != nil {
		s.writeDataError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleUpdateService(w http.ResponseWriter, r *http.Request) {
	var in model.Service
	if !readJSON(w, r, &in) {
		return
	}
	in.ID = r.PathValue("id")
	var out model.Service
	if err := s.update(func(d *model.Data) (err error) { out, err = d.UpdateService(in); return }); err != nil {
		s.writeDataError(w, err)
		return
	}
	s.mgr.StopForward(out.ID) // settings changed: the user restarts it
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteService(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.update(func(d *model.Data) error { return d.DeleteService(id) }); err != nil {
		s.writeDataError(w, err)
		return
	}
	s.mgr.StopForward(id)
	w.WriteHeader(http.StatusNoContent)
}

// --- Ordering ---------------------------------------------------------------

type orderRequest struct {
	IDs []string `json:"ids"`
}

func (s *Server) handleOrderProjects(w http.ResponseWriter, r *http.Request) {
	s.handleOrder(w, r, func(d *model.Data, ids []string) error { return d.ReorderProjects(ids) })
}

// handleOrderServers also moves servers from other projects that are listed.
func (s *Server) handleOrderServers(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.handleOrder(w, r, func(d *model.Data, ids []string) error { return d.ReorderServers(id, ids) })
}

func (s *Server) handleOrderServices(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.handleOrder(w, r, func(d *model.Data, ids []string) error { return d.ReorderServices(id, ids) })
}

func (s *Server) handleOrder(w http.ResponseWriter, r *http.Request, fn func(d *model.Data, ids []string) error) {
	var req orderRequest
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.update(func(d *model.Data) error { return fn(d, req.IDs) }); err != nil {
		s.writeDataError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Tunnels ----------------------------------------------------------------

func (s *Server) handleStartService(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var svc model.Service
	if err := s.view(func(d *model.Data) error {
		var ok bool
		if svc, ok = d.Service(id); !ok {
			return model.ErrNotFound
		}
		return nil
	}); err != nil {
		s.writeDataError(w, err)
		return
	}
	st, err := s.mgr.StartForward(svc)
	if err != nil {
		s.writeSSHError(w, err)
		return
	}
	url := string(svc.Protocol) + "://127.0.0.1:" + strconv.Itoa(st.LocalPort) + svc.Path
	writeJSON(w, http.StatusOK, map[string]any{"forward": st, "url": url})
}

func (s *Server) handleStopService(w http.ResponseWriter, r *http.Request) {
	s.mgr.StopForward(r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

// --- Host keys --------------------------------------------------------------

// pendingKey is a host key shown to the user and waiting for confirmation.
// The key itself stays on the server; the dashboard only sends back the
// token, so the key stored is exactly the one that was shown.
type pendingKey struct {
	address string
	key     ssh.PublicKey
	changed bool
	expires time.Time
}

func (s *Server) addPendingKey(address string, key ssh.PublicKey, changed bool) string {
	tok := randomToken()
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	now := s.now()
	for k, p := range s.pendingKeys {
		if now.After(p.expires) {
			delete(s.pendingKeys, k)
		}
	}
	s.pendingKeys[tok] = pendingKey{address: address, key: key, changed: changed, expires: now.Add(pendingKeyTTL)}
	return tok
}

// writeSSHError maps SSH engine errors to HTTP responses. Host-key errors
// return a token the dashboard can use to confirm the key.
func (s *Server) writeSSHError(w http.ResponseWriter, err error) {
	var unknown *sshx.UnknownHostKeyError
	var changed *sshx.HostKeyChangedError
	switch {
	case errors.As(err, &unknown):
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "unknown_host_key", "message": err.Error(),
			"address": unknown.Address, "keyType": unknown.Key.Type(), "fingerprint": unknown.Fingerprint,
			"token": s.addPendingKey(unknown.Address, unknown.Key, false),
		})
	case errors.As(err, &changed):
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "host_key_changed", "message": err.Error(),
			"address": changed.Address, "keyType": changed.Key.Type(), "fingerprint": changed.Fingerprint,
			"known": changed.Known, "token": s.addPendingKey(changed.Address, changed.Key, true),
		})
	case errors.Is(err, sshx.ErrAuthFailed):
		writeError(w, http.StatusBadGateway, "auth_failed", err.Error())
	case errors.Is(err, sshx.ErrKeyPassphrase):
		writeError(w, http.StatusBadRequest, "key_passphrase", err.Error())
	case errors.Is(err, sshx.ErrPortInUse):
		writeError(w, http.StatusConflict, "port_in_use", err.Error())
	case errors.Is(err, sshx.ErrPaused):
		writeError(w, http.StatusLocked, "locked", "the vault is locked")
	default:
		writeError(w, http.StatusBadGateway, "ssh_error", err.Error())
	}
}

// handleForgetHostKey removes the confirmed keys for an address; the next
// connection asks for confirmation again.
func (s *Server) handleForgetHostKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host string `json:"host"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.update(func(d *model.Data) error {
		if len(d.HostKeysFor(req.Host)) == 0 {
			return model.ErrNotFound
		}
		d.ForgetHostKey(req.Host)
		return nil
	}); err != nil {
		s.writeDataError(w, err)
		return
	}
	s.log.Info("host key forgotten")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleConfirmHostKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token   string `json:"token"`
		Replace bool   `json:"replace"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s.pendingMu.Lock()
	p, ok := s.pendingKeys[req.Token]
	if ok && s.now().After(p.expires) {
		delete(s.pendingKeys, req.Token)
		ok = false
	}
	s.pendingMu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "this confirmation has expired: connect again")
		return
	}
	if p.changed && !req.Replace {
		writeError(w, http.StatusBadRequest, "replace_required", "this server's key changed: replacing it must be confirmed explicitly")
		return
	}
	if err := s.update(func(d *model.Data) error { _, err := d.SetHostKey(p.address, p.key); return err }); err != nil {
		s.writeDataError(w, err)
		return
	}
	s.pendingMu.Lock()
	delete(s.pendingKeys, req.Token)
	s.pendingMu.Unlock()
	s.log.Info("host key confirmed", "replaced", p.changed)
	writeJSON(w, http.StatusOK, map[string]string{"status": "confirmed", "fingerprint": ssh.FingerprintSHA256(p.key)})
}

// handleTouch records that the user is working in the dashboard (clicking,
// typing, scrolling — the dashboard sends it at most every 30 s), so
// auto-lock doesn't lock while they move between pages, which needs no other
// request. It does nothing while locked.
func (s *Server) handleTouch(w http.ResponseWriter, r *http.Request) {
	if v := s.currentVault(); v != nil && s.vaultState() == "unlocked" {
		v.Touch()
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTraffic reports the bytes through each tunnel (today, and per
// minute for the last hour), counted on this PC. Service IDs only.
func (s *Server) handleTraffic(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"services": s.mgr.Traffic()})
}

// handleServerNotes saves a server's notes (stored in the vault).
func (s *Server) handleServerNotes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Notes string `json:"notes"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	if err := s.update(func(d *model.Data) error { return d.SetServerNotes(id, req.Notes) }); err != nil {
		s.writeDataError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
