package update

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ReleasePublicKey is the Ed25519 public key (base64) whose private half
// signs every release's SHA256SUMS.txt in the release workflow. "Update now"
// installs only downloads that match a checksum file signed with it.
//
// To change it, see docs/DEVELOPMENT.md → Release signing key. Builds with a
// different key can't install releases signed with the old one. It is a var
// only so the browser test can build with a test key (-ldflags -X).
var ReleasePublicKey = "Oohw9X83lvojAGUEtCjwJwIj3dzVRttWSOfm1uk2wCU="

// signContext is put in front of the checksum file before signing, so a
// TunnelTab release signature can't be mistaken for any other signature.
const signContext = "TunnelTab release checksums v1\n"

// ErrNoReleaseKey means this build has no release public key.
var ErrNoReleaseKey = errors.New("this build has no release signing key")

// ErrBadSignature means the checksum file wasn't signed with the release key.
var ErrBadSignature = errors.New("the download is not signed by TunnelTab")

// PublicKey decodes ReleasePublicKey.
func PublicKey() (ed25519.PublicKey, error) {
	if ReleasePublicKey == "" {
		return nil, ErrNoReleaseKey
	}
	return DecodePublicKey(ReleasePublicKey)
}

// DecodePublicKey decodes a base64 Ed25519 public key.
func DecodePublicKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("invalid release public key")
	}
	return ed25519.PublicKey(b), nil
}

// DecodePrivateKey decodes a base64 Ed25519 private key seed (32 bytes).
func DecodePrivateKey(s string) (ed25519.PrivateKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.SeedSize {
		return nil, errors.New("invalid release signing key")
	}
	return ed25519.NewKeyFromSeed(b), nil
}

// EncodeKeys returns the base64 forms of a key pair: the private seed (the
// secret) and the public key (built into the app).
func EncodeKeys(priv ed25519.PrivateKey) (private, public string) {
	enc := base64.StdEncoding.EncodeToString
	return enc(priv.Seed()), enc(priv.Public().(ed25519.PublicKey))
}

// SignSums signs the contents of SHA256SUMS.txt and returns the contents of
// SHA256SUMS.txt.sig (one base64 line).
func SignSums(priv ed25519.PrivateKey, sums []byte) []byte {
	sig := ed25519.Sign(priv, append([]byte(signContext), sums...))
	return []byte(base64.StdEncoding.EncodeToString(sig) + "\n")
}

// VerifySums checks that sig (the contents of SHA256SUMS.txt.sig) is a
// signature of sums made with the private half of pub.
func VerifySums(pub ed25519.PublicKey, sums, sig []byte) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(raw) != ed25519.SignatureSize {
		return ErrBadSignature
	}
	if !ed25519.Verify(pub, append([]byte(signContext), sums...), raw) {
		return ErrBadSignature
	}
	return nil
}

// ParseSums reads a SHA256SUMS.txt ("<hex>  <name>" per line) into a map from
// file name to lower-case hex SHA-256.
func ParseSums(sums []byte) (map[string]string, error) {
	out := map[string]string{}
	for _, line := range strings.Split(string(sums), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		// "<hex>  <name>" (text mode) or "<hex> *<name>" (binary mode).
		sum, name, ok := strings.Cut(line, " ")
		if ok && (strings.HasPrefix(name, " ") || strings.HasPrefix(name, "*")) {
			name = name[1:]
		} else {
			ok = false
		}
		if !ok || len(sum) != 64 || name == "" || strings.ContainsAny(name, "/\\") {
			return nil, fmt.Errorf("bad checksum line %q", line)
		}
		if _, err := hex.DecodeString(sum); err != nil {
			return nil, fmt.Errorf("bad checksum line %q", line)
		}
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("file %q listed twice", name)
		}
		out[name] = strings.ToLower(sum)
	}
	if len(out) == 0 {
		return nil, errors.New("empty checksum file")
	}
	return out, nil
}
