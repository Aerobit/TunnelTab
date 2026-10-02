package sshx

import (
	"net"
	"strconv"
)

// Through connects to the server (or reuses its open connection), keeps the
// connection while fn runs, and gives fn a dial function that opens TCP
// connections from the server — the same way a tunnel reaches a service.
// It returns the same connection errors as TestConnection, so it can drive
// the host-key confirmation.
//
// It is used only for checking that services answer (internal/webcheck),
// after a click: never call it in the background.
func (m *Manager) Through(serverID string, fn func(dial func(host string, port int) (net.Conn, error))) error {
	sc, err := m.acquire(serverID)
	if err != nil {
		return err
	}
	defer m.release(sc)
	fn(func(host string, port int) (net.Conn, error) {
		client := sc.currentClient()
		if client == nil {
			return nil, ErrNotConnected
		}
		return client.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	})
	return nil
}
