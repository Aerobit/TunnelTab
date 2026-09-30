package model

import (
	"errors"
	"sort"
	"testing"
)

// orderOf returns the IDs of items sorted by Order.
func orderOf[T any](items []T, id func(T) string, order func(T) int, keep func(T) bool) []string {
	var sel []T
	for _, it := range items {
		if keep(it) {
			sel = append(sel, it)
		}
	}
	sort.SliceStable(sel, func(i, j int) bool { return order(sel[i]) < order(sel[j]) })
	out := make([]string, len(sel))
	for i, it := range sel {
		out[i] = id(it)
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestReorderProjects(t *testing.T) {
	d := New()
	a, _ := d.AddProject(Project{Name: "A"})
	b, _ := d.AddProject(Project{Name: "B"})
	c, _ := d.AddProject(Project{Name: "C"})
	if err := d.ReorderProjects([]string{c.ID, a.ID, b.ID}); err != nil {
		t.Fatal(err)
	}
	got := orderOf(d.Projects, func(p Project) string { return p.ID }, func(p Project) int { return p.Order }, func(Project) bool { return true })
	if !eq(got, []string{c.ID, a.ID, b.ID}) {
		t.Fatalf("order %v", got)
	}
	mustValidate(t, d)
	for name, ids := range map[string][]string{
		"missing one": {c.ID, a.ID},
		"duplicate":   {c.ID, a.ID, a.ID},
		"unknown":     {c.ID, a.ID, b.ID, NewID()},
	} {
		before := d.Clone()
		if err := d.ReorderProjects(ids); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if !eq(orderOf(d.Projects, func(p Project) string { return p.ID }, func(p Project) int { return p.Order }, func(Project) bool { return true }),
			orderOf(before.Projects, func(p Project) string { return p.ID }, func(p Project) int { return p.Order }, func(Project) bool { return true })) {
			t.Errorf("%s: order changed on error", name)
		}
	}
}

func TestReorderServersAndMoveBetweenProjects(t *testing.T) {
	d, p, s1, _ := fixture(t)
	s2, _ := d.AddServer(Server{ProjectID: p.ID, Name: "b", Host: "h", Port: 22, Username: "u", Auth: Auth{Type: AuthAgent}})
	p2, _ := d.AddProject(Project{Name: "Other"})
	s3, _ := d.AddServer(Server{ProjectID: p2.ID, Name: "c", Host: "h", Port: 22, Username: "u", Auth: Auth{Type: AuthAgent}})
	serversIn := func(pid string) []string {
		return orderOf(d.Servers, func(s Server) string { return s.ID }, func(s Server) int { return s.Order }, func(s Server) bool { return s.ProjectID == pid })
	}

	if err := d.ReorderServers(p.ID, []string{s2.ID, s1.ID}); err != nil {
		t.Fatal(err)
	}
	if got := serversIn(p.ID); !eq(got, []string{s2.ID, s1.ID}) {
		t.Fatalf("order %v", got)
	}
	// Drop s3 (from the other project) between s2 and s1: moved and placed.
	if err := d.ReorderServers(p.ID, []string{s2.ID, s3.ID, s1.ID}); err != nil {
		t.Fatal(err)
	}
	if got := serversIn(p.ID); !eq(got, []string{s2.ID, s3.ID, s1.ID}) {
		t.Fatalf("after move: %v", got)
	}
	if len(serversIn(p2.ID)) != 0 {
		t.Fatal("server still in the old project")
	}
	mustValidate(t, d)

	if err := d.ReorderServers(p.ID, []string{s2.ID}); err == nil {
		t.Error("leaving out a server of the project was accepted")
	}
	if err := d.ReorderServers(NewID(), nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown project: %v", err)
	}
}

func TestReorderServices(t *testing.T) {
	d, _, s, svc1 := fixture(t)
	svc2, _ := d.AddService(Service{ServerID: s.ID, Label: "b", RemotePort: 81})
	svc3, _ := d.AddService(Service{ServerID: s.ID, Label: "c", RemotePort: 82})
	if err := d.ReorderServices(s.ID, []string{svc3.ID, svc1.ID, svc2.ID}); err != nil {
		t.Fatal(err)
	}
	got := orderOf(d.Services, func(x Service) string { return x.ID }, func(x Service) int { return x.Order }, func(x Service) bool { return x.ServerID == s.ID })
	if !eq(got, []string{svc3.ID, svc1.ID, svc2.ID}) {
		t.Fatalf("order %v", got)
	}
	// Services can't be moved to another server this way.
	d2, _, s2, _ := fixture(t)
	other, _ := d2.AddServer(Server{ProjectID: s2.ProjectID, Name: "x", Host: "h", Port: 22, Username: "u", Auth: Auth{Type: AuthAgent}})
	if err := d2.ReorderServices(other.ID, []string{d2.Services[0].ID}); err == nil {
		t.Error("moved a service to another server")
	}
	if err := d.ReorderServices(NewID(), nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown server: %v", err)
	}
}
