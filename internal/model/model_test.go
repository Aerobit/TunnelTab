package model

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const testKey = "-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\n-----END OPENSSH PRIVATE KEY-----"

// fixture returns data with one project, one password server and one service.
func fixture(t *testing.T) (*Data, Project, Server, Service) {
	t.Helper()
	d := New()
	p, err := d.AddProject(Project{Name: "Prod"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := d.AddServer(Server{ProjectID: p.ID, Name: "web", Host: "vps.example.com", Port: 22, Username: "root",
		Auth: Auth{Type: AuthPassword, Password: "hunter2"}})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := d.AddService(Service{ServerID: s.ID, Label: "n8n", RemotePort: 5678, LocalPort: 5678})
	if err != nil {
		t.Fatal(err)
	}
	return d, p, s, svc
}

func mustValidate(t *testing.T, d *Data) {
	t.Helper()
	if err := d.Validate(); err != nil {
		t.Fatalf("data should be valid: %v", err)
	}
}

// --- IDs --------------------------------------------------------------------

func TestNewID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := NewID()
		if !IsValidID(id) {
			t.Fatalf("invalid ID %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate ID %q", id)
		}
		seen[id] = true
	}
	for _, bad := range []string{"", "x", "00000000-0000-0000-0000-000000000000", strings.ToUpper(NewID()), NewID() + " "} {
		if IsValidID(bad) {
			t.Errorf("IsValidID(%q) = true", bad)
		}
	}
}

// --- Field validation -------------------------------------------------------

func TestServerValidation(t *testing.T) {
	good := Server{Name: "web", Host: "vps.example.com", Port: 22, Username: "root", Auth: Auth{Type: AuthAgent}}
	if err := good.Validate(); err != nil {
		t.Fatalf("good server rejected: %v", err)
	}
	cases := map[string]func(s *Server){
		"empty name":        func(s *Server) { s.Name = "" },
		"long name":         func(s *Server) { s.Name = strings.Repeat("a", MaxNameLen+1) },
		"control char name": func(s *Server) { s.Name = "a\nb" },
		"empty host":        func(s *Server) { s.Host = "" },
		"host with space":   func(s *Server) { s.Host = "a b" },
		"host leading dash": func(s *Server) { s.Host = "-oProxyCommand=x" },
		"host with semi":    func(s *Server) { s.Host = "a;b" },
		"host bad label":    func(s *Server) { s.Host = "a..b" },
		"port zero":         func(s *Server) { s.Port = 0 },
		"port too big":      func(s *Server) { s.Port = 70000 },
		"empty user":        func(s *Server) { s.Username = "" },
		"user leading dash": func(s *Server) { s.Username = "-l" },
		"user with space":   func(s *Server) { s.Username = "a b" },
		"user too long":     func(s *Server) { s.Username = strings.Repeat("a", MaxUsernameLen+1) },
		"unknown auth":      func(s *Server) { s.Auth.Type = "magic" },
	}
	for name, mutate := range cases {
		s := good
		mutate(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	for _, host := range []string{"10.0.0.1", "::1", "2001:db8::1", "my-vps", "a.b-c.example.org"} {
		s := good
		s.Host = host
		if err := s.Validate(); err != nil {
			t.Errorf("host %q rejected: %v", host, err)
		}
	}
	for _, user := range []string{"root", "deploy_bot", "first.last", "me@corp", "user-1"} {
		s := good
		s.Username = user
		if err := s.Validate(); err != nil {
			t.Errorf("username %q rejected: %v", user, err)
		}
	}
}

func TestAuthValidation(t *testing.T) {
	valid := []Auth{
		{Type: AuthAgent},
		{Type: AuthKeyFile, KeyPath: "keys/id_ed25519"},
		{Type: AuthKeyFile, KeyPath: `C:\Users\me\.ssh\id_ed25519`, Passphrase: "pp"},
		{Type: AuthKeyVault, PrivateKey: testKey},
		{Type: AuthPassword, Password: "pw"},
	}
	for _, a := range valid {
		if err := a.Validate(); err != nil {
			t.Errorf("%+v rejected: %v", a.Type, err)
		}
	}
	invalidAuth := map[string]Auth{
		"keyFile no path":    {Type: AuthKeyFile},
		"keyVault no key":    {Type: AuthKeyVault},
		"keyVault not a key": {Type: AuthKeyVault, PrivateKey: "hello"},
		"keyVault too big":   {Type: AuthKeyVault, PrivateKey: testKey + strings.Repeat("A", MaxPrivateKeyLen)},
		"password empty":     {Type: AuthPassword},
		"password too long":  {Type: AuthPassword, Password: strings.Repeat("p", MaxSecretLen+1)},
		"passphrase too long": {Type: AuthKeyFile, KeyPath: "k",
			Passphrase: strings.Repeat("p", MaxSecretLen+1)},
		"no type": {},
	}
	for name, a := range invalidAuth {
		if err := a.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestServiceValidation(t *testing.T) {
	good := Service{Label: "n8n", RemoteHost: "127.0.0.1", RemotePort: 5678, Protocol: HTTP}
	if err := good.Validate(); err != nil {
		t.Fatalf("good service rejected: %v", err)
	}
	cases := map[string]func(s *Service){
		"empty label":       func(s *Service) { s.Label = "" },
		"bad remote host":   func(s *Service) { s.RemoteHost = "a b" },
		"remote port zero":  func(s *Service) { s.RemotePort = 0 },
		"local port big":    func(s *Service) { s.LocalPort = 65536 },
		"local port neg":    func(s *Service) { s.LocalPort = -1 },
		"bad protocol":      func(s *Service) { s.Protocol = "javascript" },
		"path no slash":     func(s *Service) { s.Path = "admin" },
		"path with space":   func(s *Service) { s.Path = "/a b" },
		"path backslash":    func(s *Service) { s.Path = `/a\b` },
		"path non-ascii":    func(s *Service) { s.Path = "/café" },
		"path control char": func(s *Service) { s.Path = "/a\x00" },
	}
	for name, mutate := range cases {
		s := good
		mutate(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	s := good
	s.Path, s.Protocol, s.LocalPort, s.RemoteHost = "/admin?x=1#top", HTTPS, 8443, "portainer"
	if err := s.Validate(); err != nil {
		t.Errorf("valid variant rejected: %v", err)
	}
}

func TestValidationErrorsNeverContainSecrets(t *testing.T) {
	secret := "SuperSecretValue123"
	errs := []error{
		(&Auth{Type: AuthKeyVault, PrivateKey: secret}).Validate(),
		(&Auth{Type: AuthPassword, Password: secret + strings.Repeat("x", MaxSecretLen)}).Validate(),
		(&Auth{Type: AuthKeyFile, KeyPath: "k", Passphrase: secret + strings.Repeat("x", MaxSecretLen)}).Validate(),
	}
	for _, err := range errs {
		if err == nil {
			t.Fatal("expected an error")
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error message leaks the secret: %v", err)
		}
	}
}

// --- Whole-data validation --------------------------------------------------

func TestDataValidate(t *testing.T) {
	d, _, _, svc := fixture(t)
	mustValidate(t, d)

	broken := map[string]func(d *Data){
		"bad version":      func(d *Data) { d.Version = 99 },
		"bad id":           func(d *Data) { d.Projects[0].ID = "x" },
		"duplicate id":     func(d *Data) { d.Servers[0].ID = d.Projects[0].ID },
		"dangling server":  func(d *Data) { d.Servers[0].ProjectID = NewID() },
		"dangling service": func(d *Data) { d.Services[0].ServerID = NewID() },
		"invalid server":   func(d *Data) { d.Servers[0].Port = 0 },
		"duplicate lport": func(d *Data) {
			dup := svc
			dup.ID = NewID()
			d.Services = append(d.Services, dup)
		},
	}
	for name, mutate := range broken {
		c := d.Clone()
		mutate(c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// --- Operations -------------------------------------------------------------

func TestAddIgnoresClientIDsAndAssignsOrder(t *testing.T) {
	d := New()
	fake := NewID()
	p1, _ := d.AddProject(Project{ID: fake, Name: "A", Order: 99})
	p2, _ := d.AddProject(Project{Name: "B"})
	if p1.ID == fake || !IsValidID(p1.ID) {
		t.Errorf("client-supplied ID was used: %s", p1.ID)
	}
	if p1.Order != 0 || p2.Order != 1 {
		t.Errorf("orders = %d, %d; want 0, 1", p1.Order, p2.Order)
	}
	mustValidate(t, d)
}

func TestAddTrimsAndDefaults(t *testing.T) {
	d, p, _, _ := fixture(t)
	s, err := d.AddServer(Server{ProjectID: p.ID, Name: "  db  ", Host: " db.example.com ", Port: 2222,
		Username: " admin ", Auth: Auth{Type: AuthAgent, Password: "leftover"}})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "db" || s.Host != "db.example.com" || s.Username != "admin" {
		t.Errorf("fields not trimmed: %+v", s)
	}
	if s.Auth.Password != "" {
		t.Error("agent auth kept an unused password")
	}
	svc, err := d.AddService(Service{ServerID: s.ID, Label: "grafana", RemotePort: 3000})
	if err != nil {
		t.Fatal(err)
	}
	if svc.RemoteHost != DefaultRemoteHost || svc.Protocol != HTTP || svc.LocalPort != 0 {
		t.Errorf("defaults not applied: %+v", svc)
	}
	mustValidate(t, d)
}

func TestAddRequiresExistingParent(t *testing.T) {
	d, _, _, _ := fixture(t)
	if _, err := d.AddServer(Server{ProjectID: NewID(), Name: "x", Host: "h", Port: 22, Username: "u",
		Auth: Auth{Type: AuthAgent}}); err == nil {
		t.Error("server added to a missing project")
	}
	if _, err := d.AddService(Service{ServerID: NewID(), Label: "x", RemotePort: 1}); err == nil {
		t.Error("service added to a missing server")
	}
}

func TestFailedOperationLeavesDataUnchanged(t *testing.T) {
	d, p, s, svc := fixture(t)
	before := d.Clone()

	d.AddProject(Project{Name: ""})
	d.UpdateProject(Project{ID: p.ID, Name: ""})
	d.AddServer(Server{ProjectID: p.ID, Name: "x", Host: "bad host", Port: 22, Username: "u", Auth: Auth{Type: AuthAgent}})
	d.UpdateServer(Server{ID: s.ID, Name: "web", Host: "h", Port: 0, Username: "u", Auth: Auth{Type: AuthAgent}})
	d.AddService(Service{ServerID: s.ID, Label: "dup", RemotePort: 1, LocalPort: svc.LocalPort})
	d.UpdateService(Service{ID: svc.ID, Label: "", RemotePort: 1})

	if !reflect.DeepEqual(before, d) {
		t.Fatal("a failed operation modified the data")
	}
}

func TestNotFound(t *testing.T) {
	d := New()
	id := NewID()
	checks := []error{
		func() error { _, err := d.UpdateProject(Project{ID: id, Name: "x"}); return err }(),
		func() error { _, err := d.DeleteProject(id); return err }(),
		func() error { _, err := d.UpdateServer(Server{ID: id}); return err }(),
		d.DeleteServer(id),
		d.MoveServer(id, id),
		d.ClearPassphrase(id),
		func() error { _, err := d.UpdateService(Service{ID: id}); return err }(),
		d.DeleteService(id),
	}
	for i, err := range checks {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("check %d: got %v, want ErrNotFound", i, err)
		}
	}
}

func TestUpdateServerKeepsSecretsWhenBlank(t *testing.T) {
	d, _, s, _ := fixture(t)
	upd, err := d.UpdateServer(Server{ID: s.ID, Name: "web2", Host: s.Host, Port: 2222, Username: "root",
		Auth: Auth{Type: AuthPassword}}) // password left blank
	if err != nil {
		t.Fatal(err)
	}
	if upd.Auth.Password != "hunter2" || upd.Name != "web2" || upd.Port != 2222 {
		t.Errorf("unexpected result: name=%q port=%d passwordKept=%v", upd.Name, upd.Port, upd.Auth.Password == "hunter2")
	}
	if upd.ProjectID != s.ProjectID || upd.ID != s.ID {
		t.Error("UpdateServer changed ID or project")
	}

	upd, _ = d.UpdateServer(Server{ID: s.ID, Name: "web2", Host: s.Host, Port: 22, Username: "root",
		Auth: Auth{Type: AuthPassword, Password: "new"}})
	if upd.Auth.Password != "new" {
		t.Error("new password not saved")
	}
}

func TestUpdateServerKeepsVaultKeyWhenBlank(t *testing.T) {
	d, p, _, _ := fixture(t)
	s, err := d.AddServer(Server{ProjectID: p.ID, Name: "k", Host: "h", Port: 22, Username: "u",
		Auth: Auth{Type: AuthKeyVault, PrivateKey: testKey, Passphrase: "pp"}})
	if err != nil {
		t.Fatal(err)
	}
	upd, err := d.UpdateServer(Server{ID: s.ID, Name: "k", Host: "h", Port: 22, Username: "u",
		Auth: Auth{Type: AuthKeyVault}})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Auth.PrivateKey != s.Auth.PrivateKey || upd.Auth.Passphrase != "pp" {
		t.Error("vault key or passphrase was not kept")
	}
	if err := d.ClearPassphrase(s.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.Server(s.ID); got.Auth.Passphrase != "" || got.Auth.PrivateKey == "" {
		t.Error("ClearPassphrase did not clear only the passphrase")
	}
}

func TestChangingAuthTypeDropsOldSecrets(t *testing.T) {
	d, _, s, _ := fixture(t)
	upd, err := d.UpdateServer(Server{ID: s.ID, Name: "web", Host: s.Host, Port: 22, Username: "root",
		Auth: Auth{Type: AuthKeyFile, KeyPath: "keys/id"}})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Auth.Password != "" {
		t.Error("old password survived a change to key-file auth")
	}
	// Switching to password without supplying one must fail, not reuse anything.
	if _, err := d.UpdateServer(Server{ID: s.ID, Name: "web", Host: s.Host, Port: 22, Username: "root",
		Auth: Auth{Type: AuthPassword}}); err == nil {
		t.Error("switching to password auth without a password was accepted")
	}
}

func TestDeleteCascades(t *testing.T) {
	d, p, s, _ := fixture(t)
	p2, _ := d.AddProject(Project{Name: "Other"})
	s2, _ := d.AddServer(Server{ProjectID: p2.ID, Name: "x", Host: "h", Port: 22, Username: "u", Auth: Auth{Type: AuthAgent}})
	d.AddService(Service{ServerID: s2.ID, Label: "keep", RemotePort: 80})

	removed, err := d.DeleteProject(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != s.ID {
		t.Errorf("removed servers = %v, want [%s]", removed, s.ID)
	}
	if len(d.Projects) != 1 || len(d.Servers) != 1 || len(d.Services) != 1 || d.Services[0].Label != "keep" {
		t.Fatalf("cascade removed the wrong items: %+v", d)
	}
	mustValidate(t, d)

	if err := d.DeleteServer(s2.ID); err != nil {
		t.Fatal(err)
	}
	if len(d.Servers) != 0 || len(d.Services) != 0 {
		t.Fatal("DeleteServer did not remove the server's services")
	}
	mustValidate(t, d)
}

func TestMoveServer(t *testing.T) {
	d, _, s, _ := fixture(t)
	p2, _ := d.AddProject(Project{Name: "Other"})
	if err := d.MoveServer(s.ID, NewID()); err == nil {
		t.Error("moved to a missing project")
	}
	if err := d.MoveServer(s.ID, p2.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := d.Server(s.ID)
	if got.ProjectID != p2.ID {
		t.Error("server not moved")
	}
	mustValidate(t, d)
}

func TestLocalPortUniqueness(t *testing.T) {
	d, _, s, svc := fixture(t)
	if _, err := d.AddService(Service{ServerID: s.ID, Label: "dup", RemotePort: 1, LocalPort: svc.LocalPort}); err == nil {
		t.Error("duplicate local port accepted")
	}
	// Auto ports (0) may repeat, and a service may keep its own port on update.
	for i := 0; i < 2; i++ {
		if _, err := d.AddService(Service{ServerID: s.ID, Label: "auto", RemotePort: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.UpdateService(Service{ID: svc.ID, Label: "renamed", RemotePort: 5678, LocalPort: svc.LocalPort}); err != nil {
		t.Fatalf("updating a service with its own port failed: %v", err)
	}
	mustValidate(t, d)
}

func TestCloneIsIndependent(t *testing.T) {
	d, _, _, _ := fixture(t)
	c := d.Clone()
	c.Projects[0].Name = "changed"
	c.Servers[0].Auth.Password = "changed"
	c.Services[0].Label = "changed"
	if d.Projects[0].Name == "changed" || d.Servers[0].Auth.Password == "changed" || d.Services[0].Label == "changed" {
		t.Fatal("Clone shares memory with the original")
	}
}

// --- Secrets never leave through Public() -----------------------------------

// TestPublicHidesEverySecret fills every field tagged secret:"true" anywhere
// in Data with a marker and checks that none reach Public()'s JSON. It also
// fails if Auth gains a new string field that isn't classified, so new
// secrets can't be added without being hidden.
func TestPublicHidesEverySecret(t *testing.T) {
	d, p, _, _ := fixture(t)
	d.AddServer(Server{ProjectID: p.ID, Name: "k", Host: "h", Port: 22, Username: "u",
		Auth: Auth{Type: AuthKeyVault, PrivateKey: testKey, Passphrase: "x"}})

	const marker = "LEAK-MARKER"
	secretFields := 0
	var fill func(v reflect.Value)
	fill = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.Pointer:
			fill(v.Elem())
		case reflect.Slice:
			for i := 0; i < v.Len(); i++ {
				fill(v.Index(i))
			}
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				f := v.Type().Field(i)
				if f.Tag.Get("secret") == "true" {
					v.Field(i).SetString(marker + f.Name)
					secretFields++
				} else {
					fill(v.Field(i))
				}
			}
		}
	}
	fill(reflect.ValueOf(d))
	if secretFields == 0 {
		t.Fatal("no secret fields found; the test is broken")
	}

	out, err := json.Marshal(d.Public())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), marker) {
		t.Fatalf("Public() leaks a secret: %s", out)
	}

	known := map[string]bool{"Type": true, "KeyPath": true}
	at := reflect.TypeOf(Auth{})
	for i := 0; i < at.NumField(); i++ {
		f := at.Field(i)
		if f.Tag.Get("secret") != "true" && !known[f.Name] {
			t.Errorf("Auth.%s is neither tagged secret:\"true\" nor listed as a known public field — classify it", f.Name)
		}
	}
}

func TestPublicFlags(t *testing.T) {
	d, _, s, _ := fixture(t)
	pub := d.Public()
	if len(pub.Servers) != 1 || !pub.Servers[0].Auth.HasPassword || pub.Servers[0].ID != s.ID {
		t.Fatalf("unexpected public server: %+v", pub.Servers)
	}
	pub.Projects[0].Name = "changed"
	if d.Projects[0].Name == "changed" {
		t.Fatal("Public shares memory with the data")
	}
}

func TestServerNotes(t *testing.T) {
	d, _, s, _ := fixture(t)
	if err := d.SetServerNotes(s.ID, "Backups at 02:00\r\n\tPortainer: see password manager  \n\n"); err != nil {
		t.Fatal(err)
	}
	got, _ := d.Server(s.ID)
	if got.Notes != "Backups at 02:00\n\tPortainer: see password manager" {
		t.Fatalf("notes %q", got.Notes)
	}
	if d.Public().Servers[0].Notes != got.Notes {
		t.Error("notes missing from the dashboard view")
	}

	// Editing the server's details keeps its notes.
	upd, err := d.UpdateServer(Server{ID: s.ID, Name: "renamed", Host: s.Host, Port: s.Port, Username: s.Username, Auth: Auth{Type: AuthPassword}})
	if err != nil || upd.Notes != got.Notes {
		t.Fatalf("notes after edit %q (%v)", upd.Notes, err)
	}

	for name, bad := range map[string]string{
		"control character": "bell \a",
		"too long":          strings.Repeat("x", MaxNotesLen+1),
		"invalid UTF-8":     "\xff",
	} {
		if err := d.SetServerNotes(s.ID, bad); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := d.SetServerNotes("nope", "x"); err != ErrNotFound {
		t.Errorf("unknown server: %v", err)
	}
	if err := d.SetServerNotes(s.ID, ""); err != nil {
		t.Errorf("clearing notes: %v", err)
	}
}
