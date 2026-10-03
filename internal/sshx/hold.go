package sshx

// A hold keeps a server connected with nothing else using it: the dashboard's
// Connect button on the Health card. It is in memory only, one per server,
// and is dropped by Unhold, StopServer, StopAll, Close and when the server
// can't be reconnected.

// Hold connects to the server (or reuses its open connection) and keeps the
// connection open until Unhold. Holding a held server does nothing. It
// returns the same errors as TestConnection, so it can drive the host-key
// confirmation. Call it only when the user asks to connect.
func (m *Manager) Hold(serverID string) error {
	m.mu.Lock()
	_, held := m.holds[serverID]
	stops := m.holdStops(serverID)
	m.mu.Unlock()
	if held {
		return nil
	}
	sc, err := m.acquire(serverID)
	if err != nil {
		return err
	}
	m.mu.Lock()
	if _, held := m.holds[serverID]; held || m.closed || m.holdStops(serverID) != stops {
		// Another Hold won the race, the server was stopped (or the Manager
		// closed) meanwhile.
		cancelled := !held && !m.closed
		m.mu.Unlock()
		m.release(sc)
		switch {
		case m.isClosed():
			return ErrClosed
		case cancelled:
			return ErrConnectCancelled
		}
		return nil
	}
	m.holds[serverID] = sc
	sc.mu.Lock()
	sc.held = true
	st := sc.statusLocked()
	sc.mu.Unlock()
	m.mu.Unlock()
	m.emit(st.event())
	return nil
}

// holdStops counts what drops a server's hold: StopServer and StopAll.
// Call with m.mu held.
func (m *Manager) holdStops(serverID string) uint64 {
	return m.serverStops[serverID] + m.allStops
}

// Unhold drops the server's hold, if any. The connection closes unless a
// forward or terminal still uses it.
func (m *Manager) Unhold(serverID string) {
	m.mu.Lock()
	sc := m.holds[serverID]
	delete(m.holds, serverID)
	if sc != nil {
		sc.mu.Lock()
		sc.held = false
		sc.mu.Unlock()
	}
	m.mu.Unlock()
	if sc == nil {
		return
	}
	m.release(sc)
	sc.mu.Lock()
	done, st := sc.done, sc.statusLocked()
	sc.mu.Unlock()
	if !done { // still used by something else: say it's no longer held
		m.emit(st.event())
	}
}

// UnholdAll drops every hold.
func (m *Manager) UnholdAll() {
	for _, id := range m.Held() {
		m.Unhold(id)
	}
}

// Held lists the IDs of the held servers.
func (m *Manager) Held() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.holds))
	for id := range m.holds {
		ids = append(ids, id)
	}
	return ids
}

// IsHeld reports whether the server is held.
func (m *Manager) IsHeld(serverID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.holds[serverID]
	return ok
}

func (m *Manager) isClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}
