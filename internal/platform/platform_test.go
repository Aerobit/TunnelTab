package platform

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInstanceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.json")
	if _, ok, err := ReadInstance(path); ok || err != nil {
		t.Fatalf("missing file: ok=%v err=%v", ok, err)
	}
	inst, err := NewInstance(47811)
	if err != nil {
		t.Fatal(err)
	}
	if len(inst.Secret) != 64 {
		t.Fatalf("secret too short: %d", len(inst.Secret))
	}
	if err := WriteInstance(path, inst); err != nil {
		t.Fatal(err)
	}
	got, ok, err := ReadInstance(path)
	if err != nil || !ok || got != inst {
		t.Fatalf("got %+v ok=%v err=%v", got, ok, err)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0o600 {
			t.Errorf("permissions %o, want 600", info.Mode().Perm())
		}
	}
	RemoveInstance(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("instance file not removed")
	}
}

func TestRemoveInstanceKeepsOtherProcessFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.json")
	WriteInstance(path, Instance{PID: os.Getpid() + 1, Port: 1, Secret: "x"})
	RemoveInstance(path)
	if _, err := os.Stat(path); err != nil {
		t.Fatal("removed another process's instance file")
	}
}

func TestReadInstanceDamaged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.json")
	os.WriteFile(path, []byte("{"), 0o600)
	if _, ok, err := ReadInstance(path); ok || err == nil {
		t.Fatal("damaged file accepted")
	}
}

func TestCheckDashboardURL(t *testing.T) {
	for _, ok := range []string{
		"http://127.0.0.1:8123/",
		"http://127.0.0.1:8123/#login=abc",
		"http://127.0.0.1:1/launch?token=x",
	} {
		if err := checkDashboardURL(ok); err != nil {
			t.Errorf("checkDashboardURL(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{
		"",
		`C:\Windows\System32\calc.exe`,
		"calc.exe",
		"file:///C:/Windows/System32/calc.exe",
		"https://127.0.0.1:8123/",
		"http://example.com/",
		"http://127.0.0.1/",
		"http://127.0.0.1:0/",
		"http://127.0.0.1:99999/",
		"http://127.0.0.1:8123@example.com/",
		"http://user@127.0.0.1:8123/",
		"http://localhost:8123/",
		"http://[::1]:8123/",
		"http://127.0.0.1.example.com:8123/",
		"ms-msdt:/id PCWDiagnostic",
	} {
		if err := checkDashboardURL(bad); err == nil {
			t.Errorf("checkDashboardURL(%q) = nil, want an error", bad)
		}
	}
}
