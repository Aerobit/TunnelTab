package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Aerobit/TunnelTab/internal/health"
	"github.com/Aerobit/TunnelTab/internal/sshx/sshtest"
)

// confirmHostKey answers the fingerprint question for the service's server,
// without connecting.
func confirmHostKey(t *testing.T, h *harness, serviceID string) {
	t.Helper()
	m := h.mustCall("POST", "/api/services/"+serviceID+"/check", nil, http.StatusConflict)
	h.mustCall("POST", "/api/hostkeys/confirm", map[string]any{"token": m["token"]}, 200)
}

// changeService edits (to another port) or deletes the service.
func changeService(t *testing.T, h *harness, change, serverID, serviceID string) {
	t.Helper()
	if change == "edit" {
		h.mustCall("PUT", "/api/services/"+serviceID, map[string]any{"serverId": serverID, "label": "b", "remotePort": closedPort(t), "autoStart": true}, 200)
	} else {
		h.mustCall("DELETE", "/api/services/"+serviceID, nil, 204)
	}
}

func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for " + what)
	}
}

// Auto-start reads all services, then starts them one after the other. A
// service edited or deleted while an earlier one is still connecting must
// not be started afterwards with the settings read at the beginning.
func TestAutoStartSkipsChangedService(t *testing.T) {
	for _, change := range []string{"edit", "delete"} {
		t.Run(change, func(t *testing.T) {
			h := ready(t)
			app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
			defer app.Close()
			p := h.mustCall("POST", "/api/projects", map[string]string{"name": "P"}, 201)
			addServer := func(name string) (*sshtest.Server, string) {
				srv := sshtest.Start(t, sshtest.Options{User: "tester", Password: sshPassword,
					Exec: map[string]string{health.Command: healthOutput}})
				m := h.mustCall("POST", "/api/servers", map[string]any{
					"projectId": id(p), "name": name, "host": srv.Host, "port": srv.Port, "username": "tester",
					"auth": map[string]string{"type": "password", "password": sshPassword},
				}, 201)
				return srv, id(m)
			}
			slow, slowID := addServer("slow")
			_, otherID := addServer("other")
			// a (on the slow server) comes before b in the auto-start order.
			a := id(h.mustCall("POST", "/api/services", map[string]any{"serverId": slowID, "label": "a", "remotePort": port(t, app), "autoStart": true}, 201))
			b := id(h.mustCall("POST", "/api/services", map[string]any{"serverId": otherID, "label": "b", "remotePort": port(t, app), "autoStart": true}, 201))
			confirmHostKey(t, h, a)
			confirmHostKey(t, h, b)

			resume := slow.PauseHandshakes()
			defer resume()
			startingA := make(chan struct{})
			testHookStarting = func(id string) {
				if id == a {
					close(startingA)
				}
			}
			t.Cleanup(func() { testHookStarting = nil })
			done := make(chan struct{})
			go func() {
				h.srv.autoStart()
				close(done)
			}()
			waitFor(t, startingA, "auto-start to reach the first service")
			changeService(t, h, change, otherID, b) // while a is still connecting
			resume()
			waitFor(t, done, "auto-start to finish")

			if _, ok := h.srv.mgr.Forward(a); !ok {
				t.Fatal("the unchanged service was not started")
			}
			if st, ok := h.srv.mgr.Forward(b); ok {
				t.Fatalf("the service was started with its old settings (%s): %+v", change, st)
			}
		})
	}
}

// The same for Start: an edit or delete between reading the service and
// starting its tunnel (before the start can be cancelled by StopForward).
func TestStartSkipsChangedService(t *testing.T) {
	for _, change := range []string{"edit", "delete"} {
		t.Run(change, func(t *testing.T) {
			h := ready(t)
			_, serverID, svcID := sshSetup(t, h)
			confirmHostKey(t, h, svcID)

			read, release := make(chan struct{}), make(chan struct{})
			testHookStarting = func(id string) {
				if id == svcID {
					close(read)
					<-release
				}
			}
			t.Cleanup(func() { testHookStarting = nil })
			type answer struct {
				status int
				body   map[string]any
			}
			answered := make(chan answer, 1)
			go func() {
				status, body := h.call("POST", "/api/services/"+svcID+"/start", nil)
				answered <- answer{status, body}
			}()
			waitFor(t, read, "Start to read the service")
			changeService(t, h, change, serverID, svcID)
			close(release)

			var got answer
			select {
			case got = <-answered:
			case <-time.After(10 * time.Second):
				t.Fatal("Start never answered")
			}
			if got.status == 200 {
				t.Fatalf("Start answered 200 for a service changed meanwhile (%s): %v", change, got.body)
			}
			if st, ok := h.srv.mgr.Forward(svcID); ok {
				t.Fatalf("the service was started with its old settings (%s): %+v", change, st)
			}
		})
	}
}
