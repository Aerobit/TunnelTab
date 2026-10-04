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

// A Start that read old settings must not return the tunnel started since
// for the new ones (with a URL built from the old settings).
func TestStaleStartDoesNotReturnNewerTunnel(t *testing.T) {
	h := ready(t)
	_, serverID, svcID := sshSetup(t, h)
	confirmHostKey(t, h, svcID)

	read, release := make(chan struct{}), make(chan struct{})
	first := true
	testHookStarting = func(id string) {
		if id == svcID && first { // only the old Start waits
			first = false
			close(read)
			<-release
		}
	}
	t.Cleanup(func() { testHookStarting = nil })
	answered := make(chan int, 1)
	go func() {
		status, _ := h.call("POST", "/api/services/"+svcID+"/start", nil)
		answered <- status
	}()
	waitFor(t, read, "the old Start to read the service")
	changeService(t, h, "edit", serverID, svcID)
	h.mustCall("POST", "/api/services/"+svcID+"/start", nil, 200) // the new settings
	close(release)

	select {
	case status := <-answered:
		if status == 200 {
			t.Fatal("the old Start answered 200 with the tunnel of the new settings")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the old Start never answered")
	}
	if _, ok := h.srv.mgr.Forward(svcID); !ok {
		t.Fatal("the tunnel for the new settings was stopped")
	}
}

// Deleting a server or project removes its services: a Start paused before
// it reserved its service must not then make a tunnel nobody can see or
// stop, even when other work (here a check) keeps the connection open.
func TestDeletingParentCancelsStart(t *testing.T) {
	for _, parent := range []string{"server", "project"} {
		t.Run(parent, func(t *testing.T) {
			h := ready(t)
			_, serverID, svcID := sshSetup(t, h)
			confirmHostKey(t, h, svcID)

			// A check held open by a slow app keeps the connection in use.
			entered, appRelease := make(chan struct{}, 4), make(chan struct{})
			app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				entered <- struct{}{}
				<-appRelease
			}))
			defer app.Close()
			defer close(appRelease) // before app.Close, which waits for the handler
			slow := id(h.mustCall("POST", "/api/services", map[string]any{"serverId": serverID, "label": "slow", "remotePort": port(t, app)}, 201))
			checked := make(chan struct{})
			go func() {
				h.call("POST", "/api/services/"+slow+"/check", nil)
				close(checked)
			}()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("the check never reached the app")
			}

			read, release := make(chan struct{}), make(chan struct{})
			testHookStarting = func(id string) {
				if id == svcID {
					close(read)
					<-release
				}
			}
			t.Cleanup(func() { testHookStarting = nil })
			answered := make(chan int, 1)
			go func() {
				status, _ := h.call("POST", "/api/services/"+svcID+"/start", nil)
				answered <- status
			}()
			waitFor(t, read, "Start to read the service")
			if parent == "server" {
				h.mustCall("DELETE", "/api/servers/"+serverID, nil, 204)
			} else {
				projects := h.mustCall("GET", "/api/data", nil, 200)["data"].(map[string]any)["projects"].([]any)
				h.mustCall("DELETE", "/api/projects/"+id(projects[0].(map[string]any)), nil, 204)
			}
			close(release)

			select {
			case status := <-answered:
				if status == 200 {
					t.Fatalf("Start answered 200 after its %s was deleted", parent)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("Start never answered")
			}
			if st, ok := h.srv.mgr.Forward(svcID); ok {
				t.Fatalf("a tunnel was made for a service deleted with its %s: %+v", parent, st)
			}
		})
	}
}
