package server

import (
	"fmt"
	"testing"
)

func TestActivityLog(t *testing.T) {
	var a activityLog
	add := func(kind, id, state string) bool {
		return a.add(activityEntry{Kind: kind, ID: id, ServerID: "s1", State: state})
	}
	if add("server", "s1", "connecting") {
		t.Error("recorded a state that isn't worth showing")
	}
	if !add("server", "s1", "connected") || add("server", "s1", "connected") {
		t.Error("a repeated state should be recorded once")
	}
	if !add("server", "s1", "reconnecting") || !add("server", "s1", "connected") {
		t.Error("state changes not recorded")
	}
	if !add("forward", "svc", "active") || add("forward", "svc", "active") || !add("forward", "svc", "stopped") {
		t.Error("forward states")
	}
	if !add("terminal", "t1", "opened") || !add("terminal", "t1", "ended") {
		t.Error("terminal states")
	}
	if add("unknown", "x", "connected") {
		t.Error("recorded an unknown kind")
	}
	if got := len(a.list()); got != 7 {
		t.Fatalf("%d entries, want 7", got)
	}

	// Only the newest activityMax entries are kept.
	for i := 0; i < activityMax+50; i++ {
		add("terminal", fmt.Sprint("t", i), "opened")
	}
	list := a.list()
	if len(list) != activityMax || list[len(list)-1].ID != fmt.Sprint("t", activityMax+49) {
		t.Fatalf("%d entries, last %v", len(list), list[len(list)-1])
	}
}
