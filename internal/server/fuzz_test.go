package server

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Aerobit/TunnelTab/internal/config"
	"github.com/Aerobit/TunnelTab/internal/model"
)

// FuzzAPIBodies sends arbitrary request bodies to every endpoint that takes
// one, on an unlocked vault. The server must never panic or answer 500:
// bad input has to be rejected as bad input. Endpoints that connect to a
// server (start, test, terminals) get non-existent IDs, so nothing dials out.
func FuzzAPIBodies(f *testing.F) {
	for _, seed := range []string{
		`{}`, `null`, `[]`, `"x"`, `{"name":"x"}`, `{"name":"` + strings.Repeat("a", 300) + `"}`,
		`{"projectId":"x","name":"s","host":"h","port":22,"username":"u","auth":{"type":"password","password":"p"}}`,
		`{"serverId":"x","label":"l","remotePort":80,"localPort":-1}`,
		`{"port":1,"autoLockMinutes":-5}`, `{"token":"x","replace":true}`, `{"old":"","new":""}`,
		`{"serverId":"x","cols":99999,"rows":-1}`, "\xff\xfe", `{"name":"x"}{"name":"y"}`,
	} {
		for i := range fuzzEndpoints {
			f.Add(uint8(i), []byte(seed))
		}
	}

	dir := f.TempDir()
	config.EnsureDataDir(dir)
	s, err := New(Config{Paths: config.PathsFor(dir), BaseDir: dir, Settings: config.DefaultSettings(), VaultOptions: fastVault})
	if err != nil {
		f.Fatal(err)
	}
	defer s.Close()
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	s.SetAddr(ts.Listener.Addr())
	sess, _ := s.redeemLaunchToken(launchTokenOf(s.LaunchURL()))
	handler := s.Handler()
	post := func(method, path string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Host = strings.TrimPrefix(ts.URL, "http://")
		req.Header.Set("Origin", ts.URL)
		req.Header.Set("Authorization", "Bearer "+sess)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := post("POST", "/api/vault/create", []byte(`{"password":"correct horse battery"}`)); rec.Code != 200 {
		f.Fatalf("create vault: %d %s", rec.Code, rec.Body)
	}
	var projectID string
	s.currentVault().Update(func(d *model.Data) error {
		p, err := d.AddProject(model.Project{Name: "P"})
		projectID = p.ID
		return err
	})

	f.Fuzz(func(t *testing.T, which uint8, body []byte) {
		ep := fuzzEndpoints[int(which)%len(fuzzEndpoints)]
		path := strings.ReplaceAll(ep.path, "{project}", projectID)
		rec := post(ep.method, path, body)
		if rec.Code >= 500 {
			t.Fatalf("%s %s with %q: %d %s", ep.method, path, body, rec.Code, rec.Body)
		}
		// Don't let the fuzzer fill the vault up to its limits.
		s.currentVault().Update(func(d *model.Data) error {
			if len(d.Projects) > 50 {
				d.Projects, d.Servers, d.Services = d.Projects[:1], nil, nil
			}
			return nil
		})
		// Keep the settings valid for the next iteration.
		config.SaveSettings(s.cfg.Paths.Settings, config.DefaultSettings())
	})
}

var fuzzEndpoints = []struct{ method, path string }{
	{"POST", "/api/projects"},
	{"PUT", "/api/projects/{project}"},
	{"POST", "/api/servers"},
	{"PUT", "/api/servers/00000000-0000-4000-8000-000000000000"},
	{"POST", "/api/servers/00000000-0000-4000-8000-000000000000/move"},
	{"POST", "/api/services"},
	{"PUT", "/api/services/00000000-0000-4000-8000-000000000000"},
	{"PUT", "/api/settings"},
	{"POST", "/api/hostkeys/confirm"},
	{"POST", "/api/hostkeys/forget"},
	{"POST", "/api/terminals"},
	{"POST", "/api/session"},
	{"PUT", "/api/projects/order"},
	{"PUT", "/api/projects/{project}/servers/order"},
	{"PUT", "/api/servers/00000000-0000-4000-8000-000000000000/services/order"},
}

func launchTokenOf(u string) string {
	_, tok, _ := strings.Cut(u, "?launch=")
	return tok
}
