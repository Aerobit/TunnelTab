package sshx

import (
	"io"
	"sort"
	"sync"
	"time"
)

// Traffic through each service's tunnel, counted on this PC as the bytes
// pass: today's totals and per-minute totals for the last hour. Kept in
// memory only; it starts from zero when TunnelTab starts.

// TrafficStatus is one service's traffic.
type TrafficStatus struct {
	ServiceID string  `json:"serviceId"`
	TodayIn   int64   `json:"todayIn"`  // bytes from the server (to the browser) today
	TodayOut  int64   `json:"todayOut"` // bytes to the server today
	LastHour  []int64 `json:"lastHour"` // bytes per minute, both ways, 60 values, oldest first
}

type trafficMeter struct {
	mu        sync.Mutex
	now       func() time.Time
	byService map[string]*serviceTraffic
}

type serviceTraffic struct {
	day     string // local date the today totals are for
	in, out int64
	buckets [60]int64 // bytes in each minute; slot = unix minute % 60
	minutes [60]int64 // the unix minute each slot holds
}

func newTrafficMeter() *trafficMeter {
	return &trafficMeter{now: time.Now, byService: map[string]*serviceTraffic{}}
}

// add counts n bytes for a service; fromServer is the direction.
func (t *trafficMeter) add(serviceID string, n int, fromServer bool) {
	if n <= 0 {
		return
	}
	now := t.now()
	day := now.Format("2006-01-02")
	minute := now.Unix() / 60
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.byService[serviceID]
	if s == nil {
		s = &serviceTraffic{}
		t.byService[serviceID] = s
	}
	if s.day != day {
		s.day, s.in, s.out = day, 0, 0
	}
	if fromServer {
		s.in += int64(n)
	} else {
		s.out += int64(n)
	}
	slot := minute % 60
	if s.minutes[slot] != minute {
		s.minutes[slot], s.buckets[slot] = minute, 0
	}
	s.buckets[slot] += int64(n)
}

// list reports every service that had traffic, sorted by service ID.
func (t *trafficMeter) list() []TrafficStatus {
	now := t.now()
	day := now.Format("2006-01-02")
	minute := now.Unix() / 60
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]TrafficStatus, 0, len(t.byService))
	for id, s := range t.byService {
		st := TrafficStatus{ServiceID: id, LastHour: make([]int64, 60)}
		if s.day == day {
			st.TodayIn, st.TodayOut = s.in, s.out
		}
		for i := range 60 {
			m := minute - 59 + int64(i)
			if slot := m % 60; s.minutes[slot] == m {
				st.LastHour[i] = s.buckets[slot]
			}
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ServiceID < out[j].ServiceID })
	return out
}

// Traffic reports the traffic of every service that has had any.
func (m *Manager) Traffic() []TrafficStatus { return m.traffic.list() }

// countingWriter counts what is written through it.
type countingWriter struct {
	w     io.Writer
	count func(n int)
}

func (c countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.count(n)
	return n, err
}
