package model

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"golang.org/x/crypto/ssh"
)

func newPubKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestHostKeyAddress(t *testing.T) {
	cases := map[string]string{
		HostKeyAddress("example.com", 22):   "example.com",
		HostKeyAddress("example.com", 2222): "[example.com]:2222",
		HostKeyAddress("10.0.0.1", 22):      "10.0.0.1",
		HostKeyAddress("::1", 2222):         "[::1]:2222",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestSetHostKeyReplacesAndValidates(t *testing.T) {
	d := New()
	k1, k2 := newPubKey(t), newPubKey(t)
	if _, err := d.SetHostKey("a", k1); err != nil {
		t.Fatal(err)
	}
	d.SetHostKey("b", k1)
	kh, err := d.SetHostKey("a", k2)
	if err != nil {
		t.Fatal(err)
	}
	if got := d.HostKeysFor("a"); len(got) != 1 || got[0].Key != kh.Key {
		t.Fatalf("SetHostKey did not replace the old key: %+v", got)
	}
	if len(d.HostKeysFor("b")) != 1 {
		t.Fatal("another host's key was removed")
	}
	if kh.AddedAt == "" {
		t.Error("AddedAt not set")
	}
	mustValidate(t, d)

	d.ForgetHostKey("a")
	if len(d.HostKeysFor("a")) != 0 || len(d.KnownHosts) != 1 {
		t.Fatal("ForgetHostKey removed the wrong entries")
	}

	d.KnownHosts = append(d.KnownHosts, KnownHost{Host: "c", Key: "not a key"})
	if err := d.Validate(); err == nil {
		t.Fatal("invalid key accepted")
	}
}
