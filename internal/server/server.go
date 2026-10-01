package server

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Aerobit/TunnelTab/internal/config"
	"github.com/Aerobit/TunnelTab/internal/model"
	"github.com/Aerobit/TunnelTab/internal/sshx"
	"github.com/Aerobit/TunnelTab/internal/vault"
	"github.com/Aerobit/TunnelTab/web"
)

// Limits and timings.
const (
	launchTokenTTL  = 2 * time.Minute  // how long a login link works
	pendingKeyTTL   = 10 * time.Minute // how long a host-key confirmation stays valid
	maxSessions     = 20
	maxBodyBytes    = 1 << 20
	unlockBaseDelay = time.Second      // first delay after a wrong master password
	unlockMaxDelay  = 30 * time.Second // cap for repeated failures
)

// Config configures a Server.
type Config struct {
	Paths          config.Paths
	BaseDir        string // the portable folder (relative key paths resolve here)
	Settings       config.Settings
	Logger         *slog.Logger
	Version        string
	VaultOptions   vault.Options // Argon2id costs (tests use cheap ones)
	InstanceSecret string        // authenticates a second launch (see platform.Instance)
	OnQuit         func()        // called after the Quit request is answered
	UpdateURL      string        // releases/latest API for "Check for updates" (default: GitHub)

	// "Update now". AppDir is the folder with the running program and
	// ExeName its file name; an empty AppDir or a nil OnUpdateInstalled
	// (e.g. tests, dev builds) turns it off. OnUpdateInstalled restarts
	// TunnelTab; it is called after the request is answered.
	AppDir            string
	ExeName           string
	OnUpdateInstalled func()
	ReleaseKey        ed25519.PublicKey // nil = update.PublicKey() (tests use their own)

	// Timing overrides for tests (zero = defaults).
	UnlockBaseDelay time.Duration
	Now             func() time.Time
}

// Server is the local web server: dashboard files, JSON API and event stream.
type Server struct {
	cfg        Config
	installing atomic.Bool // "Update now" is running
	log        *slog.Logger
	now        func() time.Time
	static     http.Handler
	events     *broker
	mgr        *sshx.Manager

	addrMu sync.RWMutex
	hosts  map[string]bool // allowed Host headers, e.g. "127.0.0.1:47811"

	mu       sync.Mutex
	vault    *vault.Vault // nil until created
	settings config.Settings

	// unlockMu makes password attempts (unlock, change password) run one at
	// a time, so parallel requests can't bypass the back-off.
	unlockMu sync.Mutex

	authMu        sync.Mutex
	launchTokens  map[[32]byte]time.Time // hash → expiry
	sessions      map[[32]byte]time.Time // hash → created
	unlockFails   int
	unlockAllowed time.Time

	pendingMu   sync.Mutex
	pendingKeys map[string]pendingKey

	terms      terminals
	activity   activityLog // recent events, in memory only
	health     healthStore // opt-in server health readings, in memory only
	stopReaper chan struct{}
	closeOnce  sync.Once
}

// New creates a Server. It opens the vault if one exists (locked).
func New(cfg Config) (*Server, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.UnlockBaseDelay == 0 {
		cfg.UnlockBaseDelay = unlockBaseDelay
	}
	staticFS, err := fs.Sub(web.Files, "static")
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg:          cfg,
		log:          cfg.Logger,
		now:          cfg.Now,
		static:       noDirListing(http.FileServer(http.FS(staticFS))),
		events:       newBroker(),
		settings:     cfg.Settings,
		hosts:        map[string]bool{},
		launchTokens: map[[32]byte]time.Time{},
		sessions:     map[[32]byte]time.Time{},
		pendingKeys:  map[string]pendingKey{},
		terms:        newTerminals(),
		health:       healthStore{readings: map[string]healthReading{}, running: map[string]bool{}},
		stopReaper:   make(chan struct{}),
	}
	s.mgr = sshx.NewManager(sshx.Config{
		Targets: s.target,
		BaseDir: cfg.BaseDir,
		Logger:  cfg.Logger,
		OnEvent: func(e sshx.Event) {
			s.events.publish(tunnelEvent{Type: "tunnel", Event: e})
			s.recordTunnelActivity(e)
			if e.Kind == "server" && (e.State == sshx.StateStopped || e.State == sshx.StateFailed) {
				s.setHealth(e.ID, nil) // disconnected: no stale reading
			}
			if e.Kind == "server" && e.State == sshx.StateConnected && e.PingMs == 0 {
				go s.onServerConnected(e.ID) // a new connection (ping updates come later)
			}
		},
	})
	if vault.Exists(cfg.Paths.Vault) {
		v, err := vault.Open(cfg.Paths.Vault, cfg.Paths.VaultBackup, s.vaultOptions())
		if err != nil {
			return nil, err
		}
		s.attachVault(v)
	}
	go s.reapTerminals(s.stopReaper)
	go s.runHealth(s.stopReaper)
	return s, nil
}

// SetAddr records the address the server listens on; only requests whose
// Host header names it (as 127.0.0.1:port or localhost:port) are served.
func (s *Server) SetAddr(addr net.Addr) {
	port := addr.(*net.TCPAddr).Port
	s.addrMu.Lock()
	s.hosts = map[string]bool{
		"127.0.0.1:" + strconv.Itoa(port): true,
		"localhost:" + strconv.Itoa(port): true,
	}
	s.addrMu.Unlock()
}

// Close stops all tunnels, terminals and connections.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		close(s.stopReaper)
		s.mgr.Close() // ends every shell; their sessions report the exit and go away
		s.events.close()
	})
}

// Manager exposes the SSH engine (used by main for auto-start and tests).
func (s *Server) Manager() *sshx.Manager { return s.mgr }

// --- Launch tokens and sessions ---------------------------------------------
//
// Authentication uses a bearer token, not a cookie. Browsers send cookies for
// 127.0.0.1 to every port, including tunneled web apps, and treat all ports
// as the same site; a header token stored per-origin by the dashboard avoids
// both problems and makes cross-site request forgery impossible.
//
// 1. main opens the browser at /?launch=<token> (one-time, 2 minutes).
// 2. The dashboard POSTs it to /api/session and receives a session token.
// 3. Every API request carries "Authorization: Bearer <session>".

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(t string) [32]byte { return sha256.Sum256([]byte(t)) }

// LaunchURL returns a one-time login link for the dashboard.
func (s *Server) LaunchURL() string {
	tok := randomToken()
	s.authMu.Lock()
	s.launchTokens[hashToken(tok)] = s.now().Add(launchTokenTTL)
	s.authMu.Unlock()
	s.addrMu.RLock()
	host := ""
	for h := range s.hosts {
		if strings.HasPrefix(h, "127.0.0.1:") {
			host = h
		}
	}
	s.addrMu.RUnlock()
	return "http://" + host + "/?launch=" + tok
}

// redeemLaunchToken exchanges a valid launch token for a new session token.
func (s *Server) redeemLaunchToken(tok string) (string, bool) {
	h := hashToken(tok)
	s.authMu.Lock()
	defer s.authMu.Unlock()
	now := s.now()
	for k, exp := range s.launchTokens {
		if now.After(exp) {
			delete(s.launchTokens, k)
		}
	}
	if _, ok := s.launchTokens[h]; !ok {
		return "", false
	}
	delete(s.launchTokens, h) // one-time
	if len(s.sessions) >= maxSessions {
		var oldest [32]byte
		var oldestT time.Time
		for k, t := range s.sessions {
			if oldestT.IsZero() || t.Before(oldestT) {
				oldest, oldestT = k, t
			}
		}
		delete(s.sessions, oldest)
	}
	sess := randomToken()
	s.sessions[hashToken(sess)] = now
	return sess, true
}

func (s *Server) validSession(r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	tok, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || tok == "" {
		return false
	}
	s.authMu.Lock()
	defer s.authMu.Unlock()
	_, found := s.sessions[hashToken(tok)]
	return found
}

// checkInstanceSecret authenticates a second launch of the program.
func (s *Server) checkInstanceSecret(got string) bool {
	want := s.cfg.InstanceSecret
	return want != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// --- Unlock rate limiting ---------------------------------------------------

// unlockWait returns how long until another unlock attempt is allowed.
func (s *Server) unlockWait() time.Duration {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	return max(0, s.unlockAllowed.Sub(s.now()))
}

// unlockFailed doubles the delay after each wrong master password.
func (s *Server) unlockFailed() {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	s.unlockFails++
	delay := s.cfg.UnlockBaseDelay << min(s.unlockFails-1, 10)
	s.unlockAllowed = s.now().Add(min(delay, unlockMaxDelay))
}

func (s *Server) unlockSucceeded() {
	s.authMu.Lock()
	s.unlockFails, s.unlockAllowed = 0, time.Time{}
	s.authMu.Unlock()
}

// --- HTTP plumbing ----------------------------------------------------------

// noDirListing answers 404 for folder paths (other than "/") instead of
// listing their files.
func noDirListing(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Handler returns the complete HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)
	return s.guard(mux)
}

// guard applies the checks and headers every response gets.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		styleSrc := "'self'"
		switch r.URL.Path {
		case "/terminal.html", "/", "/index.html":
			// xterm.js creates <style> elements for colours and cell sizes,
			// and terminals show on the dashboard page as well as their own.
			// Allowing inline *styles* (never scripts) on these pages only is
			// the smallest exception that lets it render. Terminal output is
			// drawn as text, and the dashboard never inserts HTML, so neither
			// can inject markup or styles either way.
			styleSrc = "'self' 'unsafe-inline'"
		}
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src "+styleSrc+"; img-src 'self' data:; "+
			"font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")

		// DNS-rebinding protection: the Host must be our own address.
		s.addrMu.RLock()
		hostOK := s.hosts[r.Host]
		s.addrMu.RUnlock()
		if !hostOK {
			writeError(w, http.StatusForbidden, "bad_host", "requests must be addressed to 127.0.0.1 or localhost")
			return
		}
		// Cross-site protection, in depth (the bearer token already makes
		// forged requests useless): only same-origin requests are allowed.
		if origin := r.Header.Get("Origin"); origin != "" {
			if !strings.HasPrefix(origin, "http://") || !s.hosts[strings.TrimPrefix(origin, "http://")] {
				writeError(w, http.StatusForbidden, "bad_origin", "cross-origin requests are not allowed")
				return
			}
		} else if r.Method != http.MethodGet && r.Method != http.MethodHead && r.URL.Path != "/api/instance/launch" {
			// Browsers always send Origin on POST/PUT/DELETE. The one
			// exception is the program's own second launch, which is not a
			// browser and authenticates with the instance secret instead.
			writeError(w, http.StatusForbidden, "bad_origin", "missing Origin header")
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
			writeError(w, http.StatusForbidden, "bad_origin", "cross-site requests are not allowed")
			return
		}
		if r.Method == http.MethodOptions {
			writeError(w, http.StatusMethodNotAllowed, "method", "not allowed") // no CORS, ever
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// authed wraps an API handler that needs a session. (Activity for auto-lock
// is recorded by the vault itself on every View/Update.)
func (s *Server) authed(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.validSession(r) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "not signed in: start TunnelTab again to open the dashboard")
			return
		}
		fn(w, r)
	}
}

// --- Vault access -----------------------------------------------------------

func (s *Server) vaultOptions() vault.Options {
	o := s.cfg.VaultOptions
	o.AutoLock = time.Duration(s.settings.AutoLockMinutes) * time.Minute
	return o
}

// currentVault returns the vault or nil if none has been created.
func (s *Server) currentVault() *vault.Vault {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.vault
}

func (s *Server) attachVault(v *vault.Vault) {
	v.OnLock(s.onVaultLocked)
	s.mu.Lock()
	s.vault = v
	s.mu.Unlock()
}

// RunAutoLock checks for inactivity until stop is closed.
func (s *Server) RunAutoLock(stop <-chan struct{}) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if v := s.currentVault(); v != nil {
				v.LockIfIdle()
			}
		}
	}
}

func (s *Server) onVaultLocked() {
	s.log.Info("vault locked")
	s.mu.Lock()
	closeTunnels := s.settings.CloseTunnelsOnLock
	s.mu.Unlock()
	if closeTunnels {
		s.mgr.StopAll()
	}
	// Hide terminals: pages are disconnected so nothing can be seen or typed,
	// but the shells (and whatever runs in them) keep going until unlock.
	s.hideTerminals()
	s.pendingMu.Lock()
	s.pendingKeys = map[string]pendingKey{}
	s.pendingMu.Unlock()
	s.events.publish(vaultEvent{Type: "vault", State: "locked"})
}

// target gives the SSH engine a server's details from the vault.
func (s *Server) target(serverID string) (sshx.Target, error) {
	v := s.currentVault()
	if v == nil {
		return sshx.Target{}, sshx.ErrPaused
	}
	var t sshx.Target
	err := v.View(func(d *model.Data) error {
		srv, ok := d.Server(serverID)
		if !ok {
			return fmt.Errorf("server %s no longer exists", serverID)
		}
		t = sshx.Target{Server: srv, KnownHosts: d.HostKeysFor(model.HostKeyAddress(srv.Host, srv.Port))}
		return nil
	})
	if errors.Is(err, vault.ErrLocked) {
		return sshx.Target{}, sshx.ErrPaused
	}
	return t, err
}
