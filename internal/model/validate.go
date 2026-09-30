package model

import (
	"fmt"
	"net"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits on sizes and counts. They keep the vault small and stop a buggy or
// hostile client from growing it without bound.
const (
	MaxNameLen        = 100
	MaxDescriptionLen = 1000
	MaxHostLen        = 253
	MaxUsernameLen    = 64
	MaxKeyPathLen     = 4096
	MaxPrivateKeyLen  = 64 * 1024
	MaxSecretLen      = 1024
	MaxPathLen        = 1024

	MaxProjects   = 500
	MaxServers    = 1000
	MaxServices   = 2000
	MaxKnownHosts = 2000
)

// ValidationError explains which field is invalid and why. Messages are
// shown to the user and never include secret values.
type ValidationError struct {
	Field string
	Msg   string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Msg }

func invalid(field, format string, args ...any) error {
	return &ValidationError{Field: field, Msg: fmt.Sprintf(format, args...)}
}

// checkText requires valid UTF-8 without control characters, with a length
// (in characters) between min and max.
func checkText(field, s string, min, max int) error {
	if !utf8.ValidString(s) {
		return invalid(field, "must be valid text")
	}
	n := utf8.RuneCountInString(s)
	if n < min {
		if min == 1 {
			return invalid(field, "is required")
		}
		return invalid(field, "must be at least %d characters", min)
	}
	if n > max {
		return invalid(field, "must be at most %d characters", max)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return invalid(field, "must not contain control characters")
		}
	}
	return nil
}

func checkPort(field string, port int, allowZero bool) error {
	if allowZero && port == 0 {
		return nil
	}
	if port < 1 || port > 65535 {
		return invalid(field, "must be between 1 and 65535")
	}
	return nil
}

var hostLabel = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// validHost accepts an IPv4/IPv6 address or a DNS hostname.
func validHost(h string) bool {
	if h == "" || len(h) > MaxHostLen {
		return false
	}
	if net.ParseIP(h) != nil {
		return true
	}
	for _, label := range strings.Split(h, ".") {
		if !hostLabel.MatchString(label) {
			return false
		}
	}
	return true
}

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._@-]*$`)

// Validate checks a project's fields.
func (p *Project) Validate() error {
	if err := checkText("name", p.Name, 1, MaxNameLen); err != nil {
		return err
	}
	return checkText("description", p.Description, 0, MaxDescriptionLen)
}

// Validate checks a server's fields, including its login method.
func (s *Server) Validate() error {
	if err := checkText("name", s.Name, 1, MaxNameLen); err != nil {
		return err
	}
	if !validHost(s.Host) {
		return invalid("host", "must be a hostname or IP address")
	}
	if err := checkPort("port", s.Port, false); err != nil {
		return err
	}
	if len(s.Username) > MaxUsernameLen || !usernamePattern.MatchString(s.Username) {
		return invalid("username", "must be 1–%d characters: letters, digits, . _ @ - (not starting with . @ or -)", MaxUsernameLen)
	}
	return s.Auth.Validate()
}

// Validate checks that the fields required by the login method are present
// and within limits.
func (a *Auth) Validate() error {
	switch a.Type {
	case AuthAgent:
	case AuthKeyFile:
		if err := checkText("keyPath", a.KeyPath, 1, MaxKeyPathLen); err != nil {
			return err
		}
	case AuthKeyVault:
		if a.PrivateKey == "" {
			return invalid("privateKey", "is required")
		}
		if len(a.PrivateKey) > MaxPrivateKeyLen {
			return invalid("privateKey", "is too large")
		}
		if !strings.Contains(a.PrivateKey, "PRIVATE KEY-----") {
			return invalid("privateKey", "must be a private key in PEM/OpenSSH format (starts with -----BEGIN …PRIVATE KEY-----)")
		}
	case AuthPassword:
		if a.Password == "" {
			return invalid("password", "is required")
		}
	default:
		return invalid("auth.type", "must be one of agent, keyFile, keyVault, password")
	}
	if len(a.Passphrase) > MaxSecretLen {
		return invalid("passphrase", "must be at most %d bytes", MaxSecretLen)
	}
	if len(a.Password) > MaxSecretLen {
		return invalid("password", "must be at most %d bytes", MaxSecretLen)
	}
	return nil
}

// Validate checks a service's fields.
func (s *Service) Validate() error {
	if err := checkText("label", s.Label, 1, MaxNameLen); err != nil {
		return err
	}
	if !validHost(s.RemoteHost) {
		return invalid("remoteHost", "must be a hostname or IP address")
	}
	if err := checkPort("remotePort", s.RemotePort, false); err != nil {
		return err
	}
	if err := checkPort("localPort", s.LocalPort, true); err != nil {
		return err
	}
	if s.Protocol != HTTP && s.Protocol != HTTPS {
		return invalid("protocol", "must be http or https")
	}
	if s.Path != "" {
		if len(s.Path) > MaxPathLen || s.Path[0] != '/' {
			return invalid("path", "must start with / and be at most %d characters", MaxPathLen)
		}
		for i := 0; i < len(s.Path); i++ {
			if c := s.Path[i]; c <= ' ' || c >= 0x7f || c == '\\' {
				return invalid("path", "must not contain spaces, backslashes or non-ASCII characters")
			}
		}
	}
	return nil
}

// Validate checks the whole data set: every item, ID format and uniqueness,
// references between items, unique fixed local ports, and size limits.
// The vault runs it before every save.
func (d *Data) Validate() error {
	if d.Version != CurrentVersion {
		return invalid("version", "unsupported data version %d", d.Version)
	}
	if len(d.Projects) > MaxProjects {
		return invalid("projects", "too many (max %d)", MaxProjects)
	}
	if len(d.Servers) > MaxServers {
		return invalid("servers", "too many (max %d)", MaxServers)
	}
	if len(d.Services) > MaxServices {
		return invalid("services", "too many (max %d)", MaxServices)
	}
	if len(d.KnownHosts) > MaxKnownHosts {
		return invalid("knownHosts", "too many (max %d)", MaxKnownHosts)
	}

	ids := map[string]bool{}
	checkID := func(kind, id string) error {
		if !IsValidID(id) {
			return invalid(kind+".id", "is not a valid ID")
		}
		if ids[id] {
			return invalid(kind+".id", "duplicate ID %s", id)
		}
		ids[id] = true
		return nil
	}

	projects := map[string]bool{}
	for i := range d.Projects {
		p := &d.Projects[i]
		if err := checkID("project", p.ID); err != nil {
			return err
		}
		if err := p.Validate(); err != nil {
			return fmt.Errorf("project %q: %w", p.Name, err)
		}
		projects[p.ID] = true
	}

	servers := map[string]bool{}
	for i := range d.Servers {
		s := &d.Servers[i]
		if err := checkID("server", s.ID); err != nil {
			return err
		}
		if !projects[s.ProjectID] {
			return invalid("server.projectId", "server %q belongs to a project that doesn't exist", s.Name)
		}
		if err := s.Validate(); err != nil {
			return fmt.Errorf("server %q: %w", s.Name, err)
		}
		servers[s.ID] = true
	}

	for i := range d.KnownHosts {
		if err := d.KnownHosts[i].Validate(); err != nil {
			return err
		}
	}

	localPorts := map[int]string{}
	for i := range d.Services {
		s := &d.Services[i]
		if err := checkID("service", s.ID); err != nil {
			return err
		}
		if !servers[s.ServerID] {
			return invalid("service.serverId", "service %q belongs to a server that doesn't exist", s.Label)
		}
		if err := s.Validate(); err != nil {
			return fmt.Errorf("service %q: %w", s.Label, err)
		}
		if s.LocalPort != 0 {
			if other, taken := localPorts[s.LocalPort]; taken {
				return invalid("localPort", "port %d is already used by service %q", s.LocalPort, other)
			}
			localPorts[s.LocalPort] = s.Label
		}
	}
	return nil
}
