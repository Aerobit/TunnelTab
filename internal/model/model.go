package model

// CurrentVersion is the schema version of Data. Bump it (and add a migration)
// whenever the stored shape changes incompatibly.
const CurrentVersion = 1

// Data is everything stored in the vault.
type Data struct {
	Version  int       `json:"version"`
	Projects []Project `json:"projects"`
	Servers  []Server  `json:"servers"`
	Services []Service `json:"services"`
}

// Project groups related servers.
type Project struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Order       int    `json:"order"`
}

// Server is an SSH host that belongs to a project.
type Server struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	Name      string `json:"name"`
	Host      string `json:"host"` // hostname or IP address
	Port      int    `json:"port"` // SSH port
	Username  string `json:"username"`
	Auth      Auth   `json:"auth"`
	Order     int    `json:"order"`
}

// AuthType selects how TunnelTab logs in to a server.
type AuthType string

const (
	// AuthAgent uses the running ssh-agent (Windows OpenSSH agent, Pageant
	// or SSH_AUTH_SOCK on Linux). Nothing secret is stored.
	AuthAgent AuthType = "agent"
	// AuthKeyFile reads a private key file from disk (KeyPath). An optional
	// Passphrase for the key is stored in the vault.
	AuthKeyFile AuthType = "keyFile"
	// AuthKeyVault uses a private key stored inside the vault (PrivateKey),
	// with an optional Passphrase.
	AuthKeyVault AuthType = "keyVault"
	// AuthPassword logs in with a password stored in the vault.
	AuthPassword AuthType = "password"
)

// Auth holds a server's login method. Fields tagged secret:"true" must never
// leave the vault except to the SSH engine; Public() clears them and a test
// enforces that every such field is covered.
type Auth struct {
	Type       AuthType `json:"type"`
	KeyPath    string   `json:"keyPath,omitempty"`                  // AuthKeyFile; relative paths are inside the portable folder
	PrivateKey string   `json:"privateKey,omitempty" secret:"true"` // AuthKeyVault, PEM/OpenSSH format
	Passphrase string   `json:"passphrase,omitempty" secret:"true"` // AuthKeyFile / AuthKeyVault, optional
	Password   string   `json:"password,omitempty" secret:"true"`   // AuthPassword
}

// Protocol is the scheme used to open a service in the browser.
type Protocol string

const (
	HTTP  Protocol = "http"
	HTTPS Protocol = "https"
)

// DefaultRemoteHost is where a service is reached from the server's side
// when no other host is given.
const DefaultRemoteHost = "127.0.0.1"

// Service is a web UI (or any TCP port) on a server, reached through an SSH
// port forward from 127.0.0.1:LocalPort to RemoteHost:RemotePort.
type Service struct {
	ID         string   `json:"id"`
	ServerID   string   `json:"serverId"`
	Label      string   `json:"label"`
	RemoteHost string   `json:"remoteHost"`          // as seen from the server; default 127.0.0.1
	RemotePort int      `json:"remotePort"`          // port on RemoteHost
	LocalPort  int      `json:"localPort"`           // port on this PC's 127.0.0.1; 0 = pick a free one
	Protocol   Protocol `json:"protocol"`            // http or https
	Path       string   `json:"path,omitempty"`      // e.g. /admin, appended when opening
	AutoStart  bool     `json:"autoStart,omitempty"` // start the forward after unlocking
	Order      int      `json:"order"`
}

// New returns an empty Data at the current schema version.
func New() *Data {
	return &Data{
		Version:  CurrentVersion,
		Projects: []Project{},
		Servers:  []Server{},
		Services: []Service{},
	}
}

// Clone returns a deep copy of d. All fields are values, so copying the
// slices is enough.
func (d *Data) Clone() *Data {
	return &Data{
		Version:  d.Version,
		Projects: append([]Project{}, d.Projects...),
		Servers:  append([]Server{}, d.Servers...),
		Services: append([]Service{}, d.Services...),
	}
}
