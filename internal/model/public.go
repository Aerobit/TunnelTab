package model

// PublicData is the view of Data that may be sent to the dashboard: it has
// the same shape but every secret is replaced by a "has…" flag.
type PublicData struct {
	Projects []Project      `json:"projects"`
	Servers  []PublicServer `json:"servers"`
	Services []Service      `json:"services"`
	// KnownHosts are public keys, not secrets; shown so the user can review them.
	KnownHosts []KnownHost `json:"knownHosts"`
}

// PublicServer is a Server without secrets.
type PublicServer struct {
	ID        string     `json:"id"`
	ProjectID string     `json:"projectId"`
	Name      string     `json:"name"`
	Host      string     `json:"host"`
	Port      int        `json:"port"`
	Username  string     `json:"username"`
	Auth      PublicAuth `json:"auth"`
	Order     int        `json:"order"`
	Notes     string     `json:"notes,omitempty"`
	Health    bool       `json:"healthEnabled,omitempty"`
}

// PublicAuth describes a login method without revealing any secret.
type PublicAuth struct {
	Type          AuthType `json:"type"`
	KeyPath       string   `json:"keyPath,omitempty"`
	HasPrivateKey bool     `json:"hasPrivateKey"`
	HasPassphrase bool     `json:"hasPassphrase"`
	HasPassword   bool     `json:"hasPassword"`
}

// Public returns the auth details that are safe to show.
func (a Auth) Public() PublicAuth {
	return PublicAuth{
		Type:          a.Type,
		KeyPath:       a.KeyPath,
		HasPrivateKey: a.PrivateKey != "",
		HasPassphrase: a.Passphrase != "",
		HasPassword:   a.Password != "",
	}
}

// Public returns the server details that are safe to show.
func (s Server) Public() PublicServer {
	return PublicServer{
		ID:        s.ID,
		ProjectID: s.ProjectID,
		Name:      s.Name,
		Host:      s.Host,
		Port:      s.Port,
		Username:  s.Username,
		Auth:      s.Auth.Public(),
		Order:     s.Order,
		Notes:     s.Notes,
		Health:    s.HealthEnabled,
	}
}

// Public returns a copy of d that is safe to send to the dashboard.
func (d *Data) Public() *PublicData {
	p := &PublicData{
		Projects:   append([]Project{}, d.Projects...),
		Servers:    make([]PublicServer, len(d.Servers)),
		Services:   append([]Service{}, d.Services...),
		KnownHosts: append([]KnownHost{}, d.KnownHosts...),
	}
	for i, s := range d.Servers {
		p.Servers[i] = s.Public()
	}
	return p
}
