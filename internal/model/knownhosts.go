package model

import (
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// KnownHost is a server host key the user has confirmed. Known hosts are kept
// inside the vault (not in a plain-text known_hosts file) so the data folder
// doesn't reveal which servers are used.
type KnownHost struct {
	// Host is the normalised address: "example.com" for port 22,
	// "[example.com]:2222" otherwise (the OpenSSH known_hosts convention).
	Host string `json:"host"`
	// Key is the public key in authorized_keys format ("ssh-ed25519 AAAA…").
	Key string `json:"key"`
	// AddedAt is when the user confirmed the key (RFC 3339, UTC).
	AddedAt string `json:"addedAt"`
}

// HostKeyAddress returns the normalised known-hosts address for host:port.
func HostKeyAddress(host string, port int) string {
	return knownhosts.Normalize(net.JoinHostPort(host, strconv.Itoa(port)))
}

// Validate checks the address and that the key parses.
func (k *KnownHost) Validate() error {
	if k.Host == "" || len(k.Host) > MaxHostLen+10 {
		return invalid("knownHost.host", "is invalid")
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k.Key)); err != nil {
		return invalid("knownHost.key", "is not a valid public key")
	}
	return nil
}

// HostKeysFor returns the confirmed keys for an address.
func (d *Data) HostKeysFor(address string) []KnownHost {
	var out []KnownHost
	for _, k := range d.KnownHosts {
		if k.Host == address {
			out = append(out, k)
		}
	}
	return out
}

// SetHostKey records key as the only confirmed key for address, replacing
// any previous keys. Call it only after the user has confirmed the key.
func (d *Data) SetHostKey(address string, key ssh.PublicKey) (KnownHost, error) {
	k := KnownHost{
		Host:    address,
		Key:     strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))),
		AddedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := k.Validate(); err != nil {
		return KnownHost{}, err
	}
	d.ForgetHostKey(address)
	d.KnownHosts = append(d.KnownHosts, k)
	return k, nil
}

// ForgetHostKey removes all confirmed keys for address.
func (d *Data) ForgetHostKey(address string) {
	kept := d.KnownHosts[:0]
	for _, k := range d.KnownHosts {
		if k.Host != address {
			kept = append(kept, k)
		}
	}
	d.KnownHosts = kept
}
