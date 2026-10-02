package sshx

import (
	"bytes"
	"net"

	"golang.org/x/crypto/ssh"

	"github.com/Aerobit/TunnelTab/internal/model"
)

// hostKeyCallback verifies the server's key against the confirmed keys for
// its address. Unknown keys return *UnknownHostKeyError; mismatches return
// *HostKeyChangedError. There is no "accept anyway" path.
func hostKeyCallback(address string, known []model.KnownHost) ssh.HostKeyCallback {
	var keys []ssh.PublicKey
	for _, k := range known {
		if pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k.Key)); err == nil {
			keys = append(keys, pk)
		}
	}
	return func(_ string, _ net.Addr, presented ssh.PublicKey) error {
		for _, k := range keys {
			if bytes.Equal(k.Marshal(), presented.Marshal()) {
				return nil
			}
		}
		if len(keys) == 0 {
			return &UnknownHostKeyError{Address: address, Key: presented, Fingerprint: ssh.FingerprintSHA256(presented)}
		}
		return &HostKeyChangedError{Address: address, Key: presented, Fingerprint: ssh.FingerprintSHA256(presented), Known: fingerprints(keys)}
	}
}

// hostKeyAlgorithms returns the signature algorithms matching the confirmed
// key types, so the server is asked for the key we know rather than another
// type it may also have. Nil (library defaults) when nothing is confirmed.
func hostKeyAlgorithms(known []model.KnownHost) []string {
	var algos []string
	seen := map[string]bool{}
	add := func(a ...string) {
		for _, x := range a {
			if !seen[x] {
				seen[x] = true
				algos = append(algos, x)
			}
		}
	}
	for _, k := range known {
		pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k.Key))
		if err != nil {
			continue
		}
		switch pk.Type() {
		case ssh.KeyAlgoRSA:
			add(ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256)
		default:
			add(pk.Type())
		}
	}
	return algos
}

// KnownFingerprints returns the fingerprints of the confirmed keys, in the
// form HostKeyChangedError.Known uses ("ssh-ed25519 SHA256:…").
func KnownFingerprints(known []model.KnownHost) []string {
	var keys []ssh.PublicKey
	for _, k := range known {
		if pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k.Key)); err == nil {
			keys = append(keys, pk)
		}
	}
	return fingerprints(keys)
}

func fingerprints(keys []ssh.PublicKey) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = k.Type() + " " + ssh.FingerprintSHA256(k)
	}
	return out
}
