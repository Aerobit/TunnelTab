package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Aerobit/TunnelTab/internal/discover"
	"github.com/Aerobit/TunnelTab/internal/model"
	"github.com/Aerobit/TunnelTab/internal/sshx/sshtest"
)

// What discover.Command prints on a small Docker host.
const discoverOutput = `@@ss
State  Recv-Q Send-Q Local Address:Port  Peer Address:Port Process
LISTEN 0      4096         127.0.0.1:5678        0.0.0.0:*
LISTEN 0      4096           0.0.0.0:3000        0.0.0.0:*
LISTEN 0      4096           0.0.0.0:22          0.0.0.0:*
@@netstat
@@docker
{"name":"n8n","image":"n8nio/n8n","ports":"127.0.0.1:5678->5678/tcp"}
{"name":"grafana","image":"grafana/grafana","ports":"0.0.0.0:3000->3000/tcp"}
@@end
`

// discoverSetup returns a logged-in harness with one server (host key not
// yet confirmed) whose fake SSH server answers discover.Command with out.
func discoverSetup(t *testing.T, out string) (*harness, *sshtest.Server, string) {
	h := ready(t)
	sshSrv := sshtest.Start(t, sshtest.Options{User: "tester", Password: sshPassword,
		Exec: map[string]string{discover.Command: out}})
	p := h.mustCall("POST", "/api/projects", map[string]string{"name": "P"}, 201)
	srv := h.mustCall("POST", "/api/servers", map[string]any{
		"projectId": id(p), "name": "s", "host": sshSrv.Host, "port": sshSrv.Port, "username": "tester",
		"auth": map[string]string{"type": "password", "password": sshPassword},
	}, 201)
	return h, sshSrv, id(srv)
}

func TestDiscoverServices(t *testing.T) {
	h, sshSrv, serverID := discoverSetup(t, discoverOutput)
	path := "/api/servers/" + serverID + "/discover"

	// An unknown host key is confirmed first, like any connection.
	m := h.mustCall("POST", path, nil, http.StatusConflict)
	if m["error"] != "unknown_host_key" || sshSrv.Logins() != 0 {
		t.Fatalf("got %v, %d logins", m, sshSrv.Logins())
	}
	h.mustCall("POST", "/api/hostkeys/confirm", map[string]any{"token": m["token"]}, 200)

	// Grafana is already a service (as "localhost"): marked, not offered again.
	h.mustCall("POST", "/api/services", map[string]any{"serverId": serverID, "label": "Graphs", "remoteHost": "localhost", "remotePort": 3000}, 201)

	m = h.mustCall("POST", path, nil, 200)
	if m["docker"] != "ok" {
		t.Errorf("docker %v", m["docker"])
	}
	found := map[float64]map[string]any{}
	for _, c := range m["candidates"].([]any) {
		c := c.(map[string]any)
		found[c["port"].(float64)] = c
	}
	if c := found[5678]; c["name"] != "n8n" || c["kind"] != "web" || c["added"] != nil || c["host"] != "127.0.0.1" {
		t.Errorf("n8n: %v", c)
	}
	if c := found[3000]; c["name"] != "Grafana" || c["added"] != true {
		t.Errorf("grafana: %v", c)
	}
	if c := found[22]; c["kind"] != "other" {
		t.Errorf("ssh: %v", c)
	}

	// The scan connected for itself and let the connection go; nothing was saved.
	if list := h.srv.mgr.Servers(); len(list) != 0 {
		t.Errorf("connection kept: %+v", list)
	}
	var services int
	h.srv.currentVault().View(func(d *model.Data) error { services = len(d.Services); return nil })
	if services != 1 {
		t.Errorf("%d services after a scan, want 1", services)
	}

	// It shows in Recent activity, once per scan.
	var scans int
	for _, e := range h.mustCall("GET", "/api/data", nil, 200)["activity"].([]any) {
		if e := e.(map[string]any); e["kind"] == "discover" && e["state"] == "searched" && e["serverId"] == serverID {
			scans++
		}
	}
	if scans != 1 {
		t.Errorf("%d scans in the activity log, want 1", scans)
	}
	h.mustCall("POST", path, nil, 200)
	scans = 0
	for _, e := range h.mustCall("GET", "/api/data", nil, 200)["activity"].([]any) {
		if e := e.(map[string]any); e["kind"] == "discover" {
			scans++
		}
	}
	if scans != 2 {
		t.Errorf("%d scans in the activity log, want 2", scans)
	}

	h.mustCall("POST", "/api/servers/not-a-server/discover", nil, 404)
}

func TestDiscoverNoPortTools(t *testing.T) {
	h, sshSrv, serverID := discoverSetup(t, "@@ss\n@@netstat\n@@docker\nsh: docker: not found\n@@end\n")
	h.srv.currentVault().Update(func(d *model.Data) error {
		_, err := d.SetHostKey(model.HostKeyAddress(sshSrv.Host, sshSrv.Port), sshSrv.HostKey.PublicKey())
		return err
	})
	m := h.mustCall("POST", "/api/servers/"+serverID+"/discover", nil, http.StatusUnprocessableEntity)
	if m["error"] != "discover_failed" {
		t.Errorf("got %v", m)
	}
}

func TestAddServices(t *testing.T) {
	h, _, serverID := discoverSetup(t, discoverOutput)
	path := "/api/servers/" + serverID + "/services"
	services := func() []model.Service {
		var list []model.Service
		h.srv.currentVault().View(func(d *model.Data) error { list = append(list, d.Services...); return nil })
		return list
	}

	// All or nothing: one invalid service adds none, and says which.
	m := h.mustCall("POST", path, map[string]any{"services": []map[string]any{
		{"label": "n8n", "remoteHost": "127.0.0.1", "remotePort": 5678, "protocol": "http"},
		{"label": "", "remoteHost": "127.0.0.1", "remotePort": 3000, "protocol": "http"},
	}}, 400)
	if m["field"] != "services.1.label" || len(services()) != 0 {
		t.Fatalf("got %v, %d services", m, len(services()))
	}

	status, raw := h.request("POST", path, map[string]any{"services": []map[string]any{
		{"label": "n8n", "remoteHost": "127.0.0.1", "remotePort": 5678, "protocol": "http"},
		{"label": "Portainer", "remoteHost": "127.0.0.1", "remotePort": 9443, "protocol": "https", "serverId": "ignored"},
	}}, map[string]string{"Origin": h.base, "Authorization": "Bearer " + h.session, "Content-Type": "application/json"})
	if status != 201 {
		t.Fatalf("status %d: %s", status, raw)
	}
	var added []model.Service
	if err := json.Unmarshal(raw, &added); err != nil || len(added) != 2 {
		t.Fatalf("%s %v", raw, err)
	}
	list := services()
	if len(list) != 2 || list[1].ServerID != serverID || list[1].Protocol != model.HTTPS || list[0].Order >= list[1].Order {
		t.Fatalf("%+v", list)
	}

	h.mustCall("POST", path, map[string]any{"services": []any{}}, 400)
	h.mustCall("POST", "/api/servers/not-a-server/services", map[string]any{"services": []map[string]any{
		{"label": "x", "remoteHost": "127.0.0.1", "remotePort": 1, "protocol": "http"},
	}}, 404)
}

func TestSameHost(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"localhost", "127.0.0.1", true},
		{"::1", "127.0.0.1", true},
		{"LOCALHOST", "::1", true},
		{"10.0.0.5", "10.0.0.5", true},
		{"127.0.0.2", "127.0.0.1", false},
		{"127.0.0.53", "localhost", false},
		{"10.0.0.5", "127.0.0.1", false},
	} {
		if got := sameHost(tc.a, tc.b); got != tc.want {
			t.Errorf("sameHost(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
