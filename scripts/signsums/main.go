// Command signsums manages the release signing key and signs a release's
// SHA256SUMS.txt, so that "Update now" can tell a real TunnelTab release from
// a tampered one. The signature format lives in internal/update.
//
// Usage:
//
//	go run ./scripts/signsums genkey <private-key-file>
//	    Creates a new key pair. Writes the private key to the file (which
//	    must not exist yet) and prints the public key for
//	    internal/update.ReleasePublicKey.
//
//	go run ./scripts/signsums sign <SHA256SUMS.txt>
//	    Signs the file with the private key in $TUNNELTAB_SIGNING_KEY and
//	    writes <SHA256SUMS.txt>.sig next to it.
//
//	go run ./scripts/signsums verify <SHA256SUMS.txt> [public-key]
//	    Checks <SHA256SUMS.txt>.sig against the public key (default: the one
//	    built into TunnelTab).
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"os"

	"github.com/Aerobit/TunnelTab/internal/update"
)

const keyEnv = "TUNNELTAB_SIGNING_KEY"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "signsums:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	switch {
	case len(args) == 2 && args[0] == "genkey":
		return genkey(args[1])
	case len(args) == 2 && args[0] == "sign":
		return sign(args[1])
	case (len(args) == 2 || len(args) == 3) && args[0] == "verify":
		pub := ""
		if len(args) == 3 {
			pub = args[2]
		}
		return verify(args[1], pub)
	}
	fmt.Fprintln(os.Stderr, "usage: signsums genkey <private-key-file> | sign <SHA256SUMS.txt> | verify <SHA256SUMS.txt> [public-key]")
	os.Exit(2)
	return nil
}

func genkey(path string) error {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	privS, pubS := update.EncodeKeys(priv)
	// O_EXCL: never overwrite an existing key.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(privS + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Printf("Private key written to %s — keep it secret and keep a backup.\n", path)
	fmt.Printf("Public key (for internal/update.ReleasePublicKey):\n%s\n", pubS)
	return nil
}

func sign(sumsPath string) error {
	keyS := os.Getenv(keyEnv)
	if keyS == "" {
		return fmt.Errorf("$%s is not set", keyEnv)
	}
	priv, err := update.DecodePrivateKey(keyS)
	if err != nil {
		return err
	}
	sums, err := os.ReadFile(sumsPath)
	if err != nil {
		return err
	}
	if _, err := update.ParseSums(sums); err != nil {
		return fmt.Errorf("%s: %w", sumsPath, err)
	}
	if err := os.WriteFile(sumsPath+".sig", update.SignSums(priv, sums), 0o644); err != nil {
		return err
	}
	fmt.Printf("Signed %s (public key %s)\n", sumsPath, publicOf(priv))
	return nil
}

func verify(sumsPath, pubS string) error {
	var pub ed25519.PublicKey
	var err error
	if pubS == "" {
		pub, err = update.PublicKey()
		if errors.Is(err, update.ErrNoReleaseKey) {
			return errors.New("internal/update.ReleasePublicKey is empty; set it first (docs/DEVELOPMENT.md → Release signing key)")
		}
	} else {
		pub, err = update.DecodePublicKey(pubS)
	}
	if err != nil {
		return err
	}
	sums, err := os.ReadFile(sumsPath)
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(sumsPath + ".sig")
	if err != nil {
		return err
	}
	if err := update.VerifySums(pub, sums, sig); err != nil {
		return fmt.Errorf("%s: %w (does the signing key match ReleasePublicKey?)", sumsPath, err)
	}
	fmt.Printf("%s.sig is valid\n", sumsPath)
	return nil
}

func publicOf(priv ed25519.PrivateKey) string {
	_, pub := update.EncodeKeys(priv)
	return pub
}
