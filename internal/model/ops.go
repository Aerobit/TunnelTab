package model

import (
	"errors"
	"strings"
)

// ErrNotFound is returned when an ID doesn't match any item.
var ErrNotFound = errors.New("not found")

// Each operation validates its input before changing d, so a failed call
// leaves d unchanged. IDs and Order are assigned here; values for them in the
// input are ignored (except the ID used to find the item being updated).

// --- Projects ---------------------------------------------------------------

// AddProject adds a project and returns it with its new ID.
func (d *Data) AddProject(in Project) (Project, error) {
	if len(d.Projects) >= MaxProjects {
		return Project{}, invalid("projects", "too many (max %d)", MaxProjects)
	}
	p := Project{
		ID:          NewID(),
		Name:        strings.TrimSpace(in.Name),
		Description: strings.TrimSpace(in.Description),
	}
	if err := p.Validate(); err != nil {
		return Project{}, err
	}
	for _, x := range d.Projects {
		p.Order = max(p.Order, x.Order+1)
	}
	d.Projects = append(d.Projects, p)
	return p, nil
}

// UpdateProject changes a project's name and description.
func (d *Data) UpdateProject(in Project) (Project, error) {
	i := d.projectIndex(in.ID)
	if i < 0 {
		return Project{}, ErrNotFound
	}
	p := d.Projects[i]
	p.Name = strings.TrimSpace(in.Name)
	p.Description = strings.TrimSpace(in.Description)
	if err := p.Validate(); err != nil {
		return Project{}, err
	}
	d.Projects[i] = p
	return p, nil
}

// DeleteProject removes a project together with its servers and their
// services. It returns the IDs of the removed servers so their connections
// can be closed.
func (d *Data) DeleteProject(id string) (removedServers []string, err error) {
	i := d.projectIndex(id)
	if i < 0 {
		return nil, ErrNotFound
	}
	for _, s := range d.Servers {
		if s.ProjectID == id {
			removedServers = append(removedServers, s.ID)
		}
	}
	for _, sid := range removedServers {
		d.removeServer(sid)
	}
	d.Projects = append(d.Projects[:i], d.Projects[i+1:]...)
	return removedServers, nil
}

// --- Servers ----------------------------------------------------------------

// AddServer adds a server to an existing project.
func (d *Data) AddServer(in Server) (Server, error) {
	if len(d.Servers) >= MaxServers {
		return Server{}, invalid("servers", "too many (max %d)", MaxServers)
	}
	if d.projectIndex(in.ProjectID) < 0 {
		return Server{}, invalid("projectId", "project not found")
	}
	s := normalizeServer(in)
	s.ID = NewID()
	s.ProjectID = in.ProjectID
	if err := s.Validate(); err != nil {
		return Server{}, err
	}
	s.Order = d.nextServerOrder(s.ProjectID)
	d.Servers = append(d.Servers, s)
	return s, nil
}

// UpdateServer changes a server's connection details and login method. Its
// project is unchanged (see MoveServer).
//
// Stored secrets are kept when the login method stays the same and the
// corresponding field in the input is empty, so the dashboard never needs to
// send existing secrets back. Changing the login method discards the old
// method's secrets. To remove an optional key passphrase, use
// ClearPassphrase.
func (d *Data) UpdateServer(in Server) (Server, error) {
	i := d.serverIndex(in.ID)
	if i < 0 {
		return Server{}, ErrNotFound
	}
	old := d.Servers[i]
	s := normalizeServer(in)
	s.ID, s.ProjectID, s.Order, s.Notes, s.HealthEnabled = old.ID, old.ProjectID, old.Order, old.Notes, old.HealthEnabled
	if s.Auth.Type == old.Auth.Type {
		keep := func(newVal *string, oldVal string) {
			if *newVal == "" {
				*newVal = oldVal
			}
		}
		keep(&s.Auth.PrivateKey, old.Auth.PrivateKey)
		keep(&s.Auth.Passphrase, old.Auth.Passphrase)
		keep(&s.Auth.Password, old.Auth.Password)
		s.Auth = normalizeAuth(s.Auth)
	}
	if err := s.Validate(); err != nil {
		return Server{}, err
	}
	d.Servers[i] = s
	return s, nil
}

// ClearPassphrase removes a stored key passphrase from a server.
func (d *Data) ClearPassphrase(serverID string) error {
	i := d.serverIndex(serverID)
	if i < 0 {
		return ErrNotFound
	}
	d.Servers[i].Auth.Passphrase = ""
	return nil
}

// MoveServer moves a server (with its services) to another project.
func (d *Data) MoveServer(serverID, projectID string) error {
	i := d.serverIndex(serverID)
	if i < 0 {
		return ErrNotFound
	}
	if d.projectIndex(projectID) < 0 {
		return invalid("projectId", "project not found")
	}
	if d.Servers[i].ProjectID == projectID {
		return nil
	}
	d.Servers[i].Order = d.nextServerOrder(projectID)
	d.Servers[i].ProjectID = projectID
	return nil
}

// DeleteServer removes a server and its services.
func (d *Data) DeleteServer(id string) error {
	if d.serverIndex(id) < 0 {
		return ErrNotFound
	}
	d.removeServer(id)
	return nil
}

// --- Services ---------------------------------------------------------------

// AddService adds a service to an existing server. An empty RemoteHost
// becomes 127.0.0.1 and an empty Protocol becomes http.
func (d *Data) AddService(in Service) (Service, error) {
	if len(d.Services) >= MaxServices {
		return Service{}, invalid("services", "too many (max %d)", MaxServices)
	}
	if d.serverIndex(in.ServerID) < 0 {
		return Service{}, invalid("serverId", "server not found")
	}
	s := normalizeService(in)
	s.ID = NewID()
	s.ServerID = in.ServerID
	if err := s.Validate(); err != nil {
		return Service{}, err
	}
	if err := d.checkLocalPortFree(s.LocalPort, ""); err != nil {
		return Service{}, err
	}
	for _, x := range d.Services {
		if x.ServerID == s.ServerID {
			s.Order = max(s.Order, x.Order+1)
		}
	}
	d.Services = append(d.Services, s)
	return s, nil
}

// UpdateService changes a service's details. Its server is unchanged.
func (d *Data) UpdateService(in Service) (Service, error) {
	i := d.serviceIndex(in.ID)
	if i < 0 {
		return Service{}, ErrNotFound
	}
	old := d.Services[i]
	s := normalizeService(in)
	s.ID, s.ServerID, s.Order = old.ID, old.ServerID, old.Order
	if err := s.Validate(); err != nil {
		return Service{}, err
	}
	if err := d.checkLocalPortFree(s.LocalPort, s.ID); err != nil {
		return Service{}, err
	}
	d.Services[i] = s
	return s, nil
}

// DeleteService removes a service.
func (d *Data) DeleteService(id string) error {
	i := d.serviceIndex(id)
	if i < 0 {
		return ErrNotFound
	}
	d.Services = append(d.Services[:i], d.Services[i+1:]...)
	return nil
}

// --- Lookups ----------------------------------------------------------------

// Project returns the project with the given ID.
func (d *Data) Project(id string) (Project, bool) {
	if i := d.projectIndex(id); i >= 0 {
		return d.Projects[i], true
	}
	return Project{}, false
}

// Server returns the server with the given ID, including its secrets. Only
// the SSH engine should need the secrets; use Public for anything else.
func (d *Data) Server(id string) (Server, bool) {
	if i := d.serverIndex(id); i >= 0 {
		return d.Servers[i], true
	}
	return Server{}, false
}

// Service returns the service with the given ID.
func (d *Data) Service(id string) (Service, bool) {
	if i := d.serviceIndex(id); i >= 0 {
		return d.Services[i], true
	}
	return Service{}, false
}

// --- Helpers ----------------------------------------------------------------

func (d *Data) projectIndex(id string) int {
	for i := range d.Projects {
		if d.Projects[i].ID == id {
			return i
		}
	}
	return -1
}

func (d *Data) serverIndex(id string) int {
	for i := range d.Servers {
		if d.Servers[i].ID == id {
			return i
		}
	}
	return -1
}

func (d *Data) serviceIndex(id string) int {
	for i := range d.Services {
		if d.Services[i].ID == id {
			return i
		}
	}
	return -1
}

func (d *Data) nextServerOrder(projectID string) int {
	next := 0
	for _, x := range d.Servers {
		if x.ProjectID == projectID {
			next = max(next, x.Order+1)
		}
	}
	return next
}

// removeServer deletes a server and its services without checks.
func (d *Data) removeServer(id string) {
	services := d.Services[:0]
	for _, s := range d.Services {
		if s.ServerID != id {
			services = append(services, s)
		}
	}
	d.Services = services
	servers := d.Servers[:0]
	for _, s := range d.Servers {
		if s.ID != id {
			servers = append(servers, s)
		}
	}
	d.Servers = servers
}

func (d *Data) checkLocalPortFree(port int, exceptServiceID string) error {
	if port == 0 {
		return nil
	}
	for _, x := range d.Services {
		if x.LocalPort == port && x.ID != exceptServiceID {
			return invalid("localPort", "port %d is already used by service %q", port, x.Label)
		}
	}
	return nil
}

func normalizeServer(in Server) Server {
	return Server{
		Name:     strings.TrimSpace(in.Name),
		Host:     strings.TrimSpace(in.Host),
		Port:     in.Port,
		Username: strings.TrimSpace(in.Username),
		Auth:     normalizeAuth(in.Auth),
	}
}

// normalizeAuth keeps only the fields used by the login method, so switching
// method never leaves an old secret behind.
func normalizeAuth(a Auth) Auth {
	switch a.Type {
	case AuthAgent:
		return Auth{Type: a.Type}
	case AuthKeyFile:
		return Auth{Type: a.Type, KeyPath: strings.TrimSpace(a.KeyPath), Passphrase: a.Passphrase}
	case AuthKeyVault:
		key := strings.TrimSpace(a.PrivateKey)
		if key != "" {
			key += "\n" // key parsers expect the trailing newline
		}
		return Auth{Type: a.Type, PrivateKey: key, Passphrase: a.Passphrase}
	case AuthPassword:
		return Auth{Type: a.Type, Password: a.Password}
	default:
		return Auth{Type: a.Type}
	}
}

func normalizeService(in Service) Service {
	s := Service{
		Label:      strings.TrimSpace(in.Label),
		RemoteHost: strings.TrimSpace(in.RemoteHost),
		RemotePort: in.RemotePort,
		LocalPort:  in.LocalPort,
		Protocol:   in.Protocol,
		Path:       strings.TrimSpace(in.Path),
		AutoStart:  in.AutoStart,
	}
	if s.RemoteHost == "" {
		s.RemoteHost = DefaultRemoteHost
	}
	if s.Protocol == "" {
		s.Protocol = HTTP
	}
	return s
}

// SetServerNotes replaces a server's notes. Windows line endings become
// plain ones, and trailing blank space is dropped.
func (d *Data) SetServerNotes(serverID, notes string) error {
	i := d.serverIndex(serverID)
	if i < 0 {
		return ErrNotFound
	}
	notes = strings.TrimRight(strings.ReplaceAll(notes, "\r\n", "\n"), " \t\n")
	if err := checkNotes(notes); err != nil {
		return err
	}
	d.Servers[i].Notes = notes
	return nil
}

// SetServerHealth turns the opt-in server health check on or off.
func (d *Data) SetServerHealth(serverID string, on bool) error {
	i := d.serverIndex(serverID)
	if i < 0 {
		return ErrNotFound
	}
	d.Servers[i].HealthEnabled = on
	return nil
}
