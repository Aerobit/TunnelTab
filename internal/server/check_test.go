package server

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func port(t *testing.T, s *httptest.Server) int {
	t.Helper()
	return s.Listener.Addr().(*net.TCPAddr).Port
}

// closedPort returns a port nothing listens on.
func closedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return p
}

func TestServiceCheck(t *testing.T) {
	h := ready(t)
	sshSrv, serverID, serviceID := sshSetup(t, h) // service: an http backend, path /admin
	check := func(id string) map[string]any {
		return h.mustCall("POST", "/api/services/"+id+"/check", nil, 200)["check"].(map[string]any)
	}

	// Like Start, a check may need the fingerprint confirmed first.
	m := h.mustCall("POST", "/api/services/"+serviceID+"/check", nil, http.StatusConflict)
	if m["error"] != "unknown_host_key" || sshSrv.Logins() != 0 {
		t.Fatalf("got %v", m)
	}
	h.mustCall("POST", "/api/hostkeys/confirm", map[string]any{"token": m["token"]}, 200)

	// Check: connects, answers over http, lets the connection go.
	if c := check(serviceID); c["state"] != "responding" || c["protocol"] != "http" || c["status"] != float64(200) {
		t.Fatalf("check: %v", c)
	}
	if len(h.srv.mgr.Servers()) != 0 {
		t.Fatal("connection kept after a check")
	}
	if _, ok := h.mustCall("GET", "/api/data", nil, 200)["checks"].(map[string]any)[serviceID]; !ok {
		t.Fatal("check not in /api/data")
	}

	// Editing the service drops the result (its port may have changed).
	closed := closedPort(t)
	h.mustCall("PUT", "/api/services/"+serviceID, map[string]any{"serverId": serverID, "label": "web", "remotePort": closed}, 200)
	if _, ok := h.mustCall("GET", "/api/data", nil, 200)["checks"].(map[string]any)[serviceID]; ok {
		t.Fatal("check kept after editing the service")
	}
	if c := check(serviceID); c["state"] != "no_answer" {
		t.Fatalf("closed port: %v", c)
	}

	// An https app (self-signed) is recognised as such.
	tlsApp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer tlsApp.Close()
	svc := h.mustCall("POST", "/api/services", map[string]any{"serverId": serverID, "label": "tls", "remotePort": port(t, tlsApp)}, 201)
	if c := check(id(svc)); c["state"] != "responding" || c["protocol"] != "https" {
		t.Fatalf("https app: %v", c)
	}

	// Start checks once the tunnel is up.
	dashboard := h.srv.events.subscribe()
	defer h.srv.events.unsubscribe(dashboard)
	h.mustCall("DELETE", "/api/services/"+id(svc), nil, 204)
	svc = h.mustCall("POST", "/api/services", map[string]any{"serverId": serverID, "label": "tls2", "remotePort": port(t, tlsApp)}, 201)
	h.mustCall("POST", "/api/services/"+id(svc)+"/start", nil, 200)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := h.srv.checkReadings()[id(svc)]; ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no check after Start")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Deleting drops it.
	h.mustCall("POST", "/api/services/"+id(svc)+"/stop", nil, 204)
	h.mustCall("DELETE", "/api/services/"+id(svc), nil, 204)
	if _, ok := h.srv.checkReadings()[id(svc)]; ok {
		t.Fatal("check kept after deleting the service")
	}
	h.mustCall("POST", "/api/services/nope/check", nil, 404)
}

func TestDiscoverChecksCandidates(t *testing.T) {
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer web.Close()
	tlsApp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer tlsApp.Close()
	closed := closedPort(t)
	out := fmt.Sprintf(`@@ss
State  Recv-Q Send-Q Local Address:Port  Peer Address:Port Process
LISTEN 0      4096         127.0.0.1:%d        0.0.0.0:*
LISTEN 0      4096         127.0.0.1:%d        0.0.0.0:*
LISTEN 0      4096         127.0.0.1:%d        0.0.0.0:*
LISTEN 0      4096           0.0.0.0:22          0.0.0.0:*
@@netstat
@@docker
{"name":"grafana","image":"grafana/grafana","ports":"127.0.0.1:%d->3000/tcp"}
@@end
`, port(t, web), port(t, tlsApp), closed, port(t, tlsApp))
	h, _, serverID := discoverSetup(t, out)
	path := "/api/servers/" + serverID + "/discover"
	m := h.mustCall("POST", path, nil, http.StatusConflict)
	h.mustCall("POST", "/api/hostkeys/confirm", map[string]any{"token": m["token"]}, 200)

	dashboard := h.srv.events.subscribe()
	defer h.srv.events.unsubscribe(dashboard)
	m = h.mustCall("POST", path, nil, 200)

	// Progress went to the dashboards: connecting, scanning, then each check.
	var steps []string
	for len(dashboard) > 0 {
		var ev struct {
			Type, ServerID, Step string
			Done, Total          int
		}
		json.Unmarshal(<-dashboard, &ev)
		if ev.Type == "discover" && ev.ServerID == serverID {
			steps = append(steps, fmt.Sprintf("%s %d/%d", ev.Step, ev.Done, ev.Total))
		}
	}
	// 3 candidates may be web pages (http, https, closed); ssh isn't checked.
	if want := []string{"connecting 0/0", "scanning 0/0", "checking 0/3", "checking 1/3", "checking 2/3", "checking 3/3"}; fmt.Sprint(steps) != fmt.Sprint(want) {
		t.Errorf("progress %v, want %v", steps, want)
	}

	checks := map[int]any{}
	for _, c := range m["candidates"].([]any) {
		c := c.(map[string]any)
		checks[int(c["port"].(float64))] = c["check"]
	}
	state := func(p int) (string, string) {
		c, _ := checks[p].(map[string]any)
		s, _ := c["state"].(string)
		proto, _ := c["protocol"].(string)
		return s, proto
	}
	if s, p := state(port(t, web)); s != "responding" || p != "http" {
		t.Errorf("http app: %s %s", s, p)
	}
	if s, p := state(port(t, tlsApp)); s != "responding" || p != "https" {
		t.Errorf("https app (Grafana, usually http): %s %s", s, p)
	}
	if s, _ := state(closed); s != "no_answer" {
		t.Errorf("closed port: %s", s)
	}
	if checks[22] != nil {
		t.Errorf("ssh was checked: %v", checks[22])
	}
	if list := h.srv.mgr.Servers(); len(list) != 0 {
		t.Errorf("connection kept: %+v", list)
	}
}
