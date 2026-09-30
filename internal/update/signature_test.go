package update

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

const testSums = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef  tunneltab-1.2.3.zip\n"

func testKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func TestSignVerifyRoundTrip(t *testing.T) {
	pub, priv := testKey(t)
	sig := SignSums(priv, []byte(testSums))
	if err := VerifySums(pub, []byte(testSums), sig); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	// Windows line endings in the .sig file are fine.
	crlf := []byte(strings.TrimSpace(string(sig)) + "\r\n")
	if err := VerifySums(pub, []byte(testSums), crlf); err != nil {
		t.Fatalf("signature with CRLF rejected: %v", err)
	}
}

func TestVerifyRejects(t *testing.T) {
	pub, priv := testKey(t)
	otherPub, otherPriv := testKey(t)
	sig := SignSums(priv, []byte(testSums))
	tampered := strings.Replace(testSums, "0123", "1123", 1)

	cases := map[string]struct {
		pub  ed25519.PublicKey
		sums string
		sig  []byte
	}{
		"tampered sums":    {pub, tampered, sig},
		"other key":        {otherPub, testSums, sig},
		"signed by other":  {pub, testSums, SignSums(otherPriv, []byte(testSums))},
		"empty sig":        {pub, testSums, nil},
		"garbage sig":      {pub, testSums, []byte("not base64!\n")},
		"short sig":        {pub, testSums, []byte("AAAA\n")},
		"no context":       {pub, testSums, []byte(b64(ed25519.Sign(priv, []byte(testSums))))},
		"sums with suffix": {pub, testSums + "x", sig},
	}
	for name, c := range cases {
		if err := VerifySums(c.pub, []byte(c.sums), c.sig); !errors.Is(err, ErrBadSignature) {
			t.Errorf("%s: got %v, want ErrBadSignature", name, err)
		}
	}
}

func TestKeyEncoding(t *testing.T) {
	_, priv := testKey(t)
	privS, pubS := EncodeKeys(priv)
	priv2, err := DecodePrivateKey(privS + "\n")
	if err != nil || !priv2.Equal(priv) {
		t.Fatalf("private key round trip failed: %v", err)
	}
	pub, err := DecodePublicKey(pubS)
	if err != nil || !pub.Equal(priv.Public()) {
		t.Fatalf("public key round trip failed: %v", err)
	}
	for _, bad := range []string{"", "!!", "AAAA", pubS + "AAAA"} {
		if _, err := DecodePublicKey(bad); err == nil {
			t.Errorf("DecodePublicKey(%q) accepted", bad)
		}
		if _, err := DecodePrivateKey(bad); err == nil {
			t.Errorf("DecodePrivateKey(%q) accepted", bad)
		}
	}
}

func TestReleasePublicKey(t *testing.T) {
	if ReleasePublicKey == "" {
		if _, err := PublicKey(); !errors.Is(err, ErrNoReleaseKey) {
			t.Fatalf("got %v, want ErrNoReleaseKey", err)
		}
		t.Skip("no release key set yet")
	}
	if _, err := PublicKey(); err != nil {
		t.Fatalf("ReleasePublicKey is invalid: %v", err)
	}
}

func TestParseSums(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	got, err := ParseSums([]byte(sum + "  tunneltab-1.2.3.zip\r\n" + strings.ToUpper(sum) + " *other.txt\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got["tunneltab-1.2.3.zip"] != sum || got["other.txt"] != sum {
		t.Fatalf("got %v", got)
	}
	for _, bad := range []string{
		"",
		sum + " tunneltab.zip\n", // one space
		"xyz  tunneltab.zip\n",   // short
		strings.Repeat("zz", 32) + "  tunneltab.zip\n",        // not hex
		sum + "  ../tunneltab.zip\n",                          // path
		sum + "  a\\tunneltab.zip\n",                          // Windows path
		sum + "  tunneltab.zip\n" + sum + "  tunneltab.zip\n", // duplicate
		sum + "  \n",
	} {
		if _, err := ParseSums([]byte(bad)); err == nil {
			t.Errorf("ParseSums(%q) accepted", bad)
		}
	}
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) + "\n" }
