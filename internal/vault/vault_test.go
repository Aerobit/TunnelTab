package vault

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/Aerobit/TunnelTab/internal/model"
)

// fastOpts uses the cheapest allowed Argon2id costs so tests run quickly.
var fastOpts = Options{Params: Params{Time: 1, MemoryKiB: minMemoryKiB, Threads: 1}}

const pw = "correct horse battery"

type paths struct{ vault, backup string }

func newPaths(t *testing.T) paths {
	dir := t.TempDir()
	return paths{filepath.Join(dir, "vault.enc"), filepath.Join(dir, "vault.enc.bak")}
}

func create(t *testing.T) (*Vault, paths) {
	t.Helper()
	p := newPaths(t)
	v, err := Create(p.vault, p.backup, []byte(pw), fastOpts)
	if err != nil {
		t.Fatal(err)
	}
	return v, p
}

// addSample stores a project, a password server and a service.
func addSample(t *testing.T, v *Vault, password string) {
	t.Helper()
	err := v.Update(func(d *model.Data) error {
		p, err := d.AddProject(model.Project{Name: "Prod"})
		if err != nil {
			return err
		}
		s, err := d.AddServer(model.Server{ProjectID: p.ID, Name: "web", Host: "vps.example.com", Port: 22,
			Username: "root", Auth: model.Auth{Type: model.AuthPassword, Password: password}})
		if err != nil {
			return err
		}
		_, err = d.AddService(model.Service{ServerID: s.ID, Label: "n8n", RemotePort: 5678})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func counts(t *testing.T, v *Vault) (projects, servers, services int) {
	t.Helper()
	if err := v.View(func(d *model.Data) error {
		projects, servers, services = len(d.Projects), len(d.Servers), len(d.Services)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return
}

// --- Create / unlock --------------------------------------------------------

func TestCreateAndReopen(t *testing.T) {
	v, p := create(t)
	if !v.Unlocked() {
		t.Fatal("new vault should be unlocked")
	}
	addSample(t, v, "hunter2")

	v2, err := Open(p.vault, p.backup, fastOpts)
	if err != nil {
		t.Fatal(err)
	}
	if v2.Unlocked() {
		t.Fatal("opened vault should start locked")
	}
	if err := v2.View(func(*model.Data) error { return nil }); !errors.Is(err, ErrLocked) {
		t.Fatalf("View on locked vault: got %v, want ErrLocked", err)
	}
	if err := v2.Unlock([]byte(pw)); err != nil {
		t.Fatal(err)
	}
	if pr, sv, se := counts(t, v2); pr != 1 || sv != 1 || se != 1 {
		t.Fatalf("got %d/%d/%d items, want 1/1/1", pr, sv, se)
	}
	v2.View(func(d *model.Data) error {
		if d.Servers[0].Auth.Password != "hunter2" {
			t.Error("secret did not survive the round trip")
		}
		return nil
	})
}

func TestCreateRefusesExistingVault(t *testing.T) {
	_, p := create(t)
	if _, err := Create(p.vault, p.backup, []byte(pw), fastOpts); !errors.Is(err, ErrExists) {
		t.Fatalf("got %v, want ErrExists", err)
	}
}

func TestCreateRejectsWeakPassword(t *testing.T) {
	p := newPaths(t)
	for _, bad := range []string{"", "short", "1234567", string([]byte{0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8})} {
		if _, err := Create(p.vault, p.backup, []byte(bad), fastOpts); !errors.Is(err, ErrWeakPassword) {
			t.Errorf("password %q: got %v, want ErrWeakPassword", bad, err)
		}
	}
	if Exists(p.vault) {
		t.Fatal("vault file created despite the error")
	}
}

func TestOpenMissingVault(t *testing.T) {
	p := newPaths(t)
	if _, err := Open(p.vault, p.backup, fastOpts); !errors.Is(err, ErrNoVault) {
		t.Fatalf("got %v, want ErrNoVault", err)
	}
}

func TestWrongPassword(t *testing.T) {
	v, p := create(t)
	addSample(t, v, "hunter2")
	v2, _ := Open(p.vault, p.backup, fastOpts)
	for _, bad := range []string{"", "wrong password", pw + " "} {
		if err := v2.Unlock([]byte(bad)); !errors.Is(err, ErrWrongPassword) {
			t.Errorf("password %q: got %v, want ErrWrongPassword", bad, err)
		}
	}
	if v2.Unlocked() {
		t.Fatal("vault unlocked with a wrong password")
	}
}

// --- File contents and tampering --------------------------------------------

func TestFileContainsNoPlaintext(t *testing.T) {
	v, p := create(t)
	secret := "PlaintextCanary-9f8e7d"
	addSample(t, v, secret)
	if err := v.Update(func(d *model.Data) error {
		_, err := d.AddProject(model.Project{Name: "Project-Canary", Description: "Description-Canary"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p.vault)
	if err != nil {
		t.Fatal(err)
	}
	// Every needle contains '-', '.' or ' ', which never occur in base64,
	// so a match can only mean plaintext was written (not a chance match
	// inside the base64 ciphertext).
	for _, needle := range []string{secret, "vps.example.com", "Project-Canary", "Description-Canary", pw} {
		if bytes.Contains(raw, []byte(needle)) {
			t.Errorf("vault file contains %q in plain text", needle)
		}
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(p.vault)
		if info.Mode().Perm() != 0o600 {
			t.Errorf("vault permissions = %o, want 600", info.Mode().Perm())
		}
	}
}

// tamper rewrites one field of the vault file and returns the Unlock error.
func tamper(t *testing.T, mutate func(f map[string]any)) error {
	t.Helper()
	v, p := create(t)
	addSample(t, v, "hunter2")
	raw, _ := os.ReadFile(p.vault)
	var f map[string]any
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	mutate(f)
	out, _ := json.Marshal(f)
	os.WriteFile(p.vault, out, 0o600)

	v2 := newVault(p.vault, p.backup, fastOpts)
	return v2.Unlock([]byte(pw))
}

func flipByte(b64 string) string {
	var b []byte
	json.Unmarshal([]byte(`"`+b64+`"`), &b)
	b[len(b)/2] ^= 0x01
	out, _ := json.Marshal(b)
	return string(out[1 : len(out)-1])
}

func TestTamperingIsDetected(t *testing.T) {
	cases := map[string]struct {
		mutate func(f map[string]any)
		want   error
	}{
		"ciphertext bit flip": {func(f map[string]any) { f["ciphertext"] = flipByte(f["ciphertext"].(string)) }, ErrWrongPassword},
		"nonce bit flip":      {func(f map[string]any) { f["nonce"] = flipByte(f["nonce"].(string)) }, ErrWrongPassword},
		"salt changed": {func(f map[string]any) {
			f["kdf"].(map[string]any)["salt"] = flipByte(f["kdf"].(map[string]any)["salt"].(string))
		}, ErrWrongPassword},
		// Header fields are authenticated: a valid but different time cost
		// must not decrypt.
		"time cost changed": {func(f map[string]any) { f["kdf"].(map[string]any)["time"] = 2.0 }, ErrWrongPassword},
		"huge memory (DoS)": {func(f map[string]any) { f["kdf"].(map[string]any)["memoryKiB"] = 1 << 30 }, ErrCorrupt},
		"too many passes":   {func(f map[string]any) { f["kdf"].(map[string]any)["time"] = 1000.0 }, ErrCorrupt},
		"zero threads":      {func(f map[string]any) { f["kdf"].(map[string]any)["threads"] = 0.0 }, ErrCorrupt},
		"short salt":        {func(f map[string]any) { f["kdf"].(map[string]any)["salt"] = "AAAA" }, ErrCorrupt},
		"other kdf":         {func(f map[string]any) { f["kdf"].(map[string]any)["name"] = "pbkdf2" }, ErrCorrupt},
		"other cipher":      {func(f map[string]any) { f["cipher"] = "aes-128-cbc" }, ErrCorrupt},
		"not a vault":       {func(f map[string]any) { f["format"] = "something-else" }, ErrCorrupt},
		"newer version":     {func(f map[string]any) { f["version"] = 2.0 }, ErrUnsupported},
		"missing nonce":     {func(f map[string]any) { delete(f, "nonce") }, ErrCorrupt},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if err := tamper(t, c.mutate); !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
		})
	}
}

func TestGarbageFiles(t *testing.T) {
	p := newPaths(t)
	for name, content := range map[string][]byte{
		"empty":     {},
		"not json":  []byte("hello"),
		"truncated": []byte(`{"format":"tunneltab-vault","version":1,`),
	} {
		os.WriteFile(p.vault, content, 0o600)
		if _, err := Open(p.vault, p.backup, fastOpts); !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: got %v, want ErrCorrupt", name, err)
		}
	}
}

// --- Updates ----------------------------------------------------------------

func TestFailedUpdateChangesNothing(t *testing.T) {
	v, p := create(t)
	addSample(t, v, "hunter2")
	before, _ := os.ReadFile(p.vault)

	sentinel := errors.New("stop")
	if err := v.Update(func(d *model.Data) error {
		d.Projects = nil
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("got %v", err)
	}
	// fn succeeds but leaves invalid data (a dangling reference).
	if err := v.Update(func(d *model.Data) error {
		d.Projects = d.Projects[:0]
		return nil
	}); err == nil {
		t.Fatal("invalid data was saved")
	}

	after, _ := os.ReadFile(p.vault)
	if !bytes.Equal(before, after) {
		t.Error("vault file changed after failed updates")
	}
	if pr, sv, se := counts(t, v); pr != 1 || sv != 1 || se != 1 {
		t.Errorf("in-memory data changed: %d/%d/%d", pr, sv, se)
	}
}

func TestViewCannotModify(t *testing.T) {
	v, _ := create(t)
	addSample(t, v, "hunter2")
	v.View(func(d *model.Data) error {
		d.Projects[0].Name = "hacked"
		return nil
	})
	v.View(func(d *model.Data) error {
		if d.Projects[0].Name == "hacked" {
			t.Error("View modified the stored data")
		}
		return nil
	})
}

func TestBackupKeepsPreviousVersion(t *testing.T) {
	v, p := create(t)
	addSample(t, v, "hunter2") // second write: backup = empty vault
	addSample(t, v, "hunter3") // third write: backup = one sample

	b := newVault(p.backup, p.backup+".unused", fastOpts)
	if err := b.Unlock([]byte(pw)); err != nil {
		t.Fatalf("backup does not unlock: %v", err)
	}
	if pr, _, _ := counts(t, b); pr != 1 {
		t.Fatalf("backup has %d projects, want 1 (the previous version)", pr)
	}
	if pr, _, _ := counts(t, v); pr != 2 {
		t.Fatalf("current vault has %d projects, want 2", pr)
	}
}

func TestConcurrentUpdates(t *testing.T) {
	v, p := create(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := v.Update(func(d *model.Data) error {
				_, err := d.AddProject(model.Project{Name: "p"})
				return err
			}); err != nil {
				t.Error(err)
			}
			v.View(func(*model.Data) error { return nil })
		}()
	}
	wg.Wait()
	v2, _ := Open(p.vault, p.backup, fastOpts)
	v2.Unlock([]byte(pw))
	if pr, _, _ := counts(t, v2); pr != 20 {
		t.Fatalf("got %d projects on disk, want 20", pr)
	}
}

// --- Lock, password change, auto-lock ---------------------------------------

func TestLockWipesKey(t *testing.T) {
	v, _ := create(t)
	key := v.key
	called := 0
	v.OnLock(func() { called++ })
	v.Lock()
	if v.Unlocked() || v.key != nil || v.data != nil {
		t.Fatal("vault still holds key or data after Lock")
	}
	if !bytes.Equal(key, make([]byte, keyLen)) {
		t.Fatal("key bytes were not wiped")
	}
	v.Lock() // no-op
	if called != 1 {
		t.Fatalf("OnLock ran %d times, want 1", called)
	}
	if err := v.Update(func(*model.Data) error { return nil }); !errors.Is(err, ErrLocked) {
		t.Fatalf("Update after Lock: got %v", err)
	}
	if err := v.Unlock([]byte(pw)); err != nil {
		t.Fatal(err)
	}
}

func TestChangePassword(t *testing.T) {
	v, p := create(t)
	addSample(t, v, "hunter2")
	newPw := []byte("a much better passphrase")

	if err := v.ChangePassword([]byte("wrong"), newPw); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("wrong old password: got %v", err)
	}
	if err := v.ChangePassword([]byte(pw), []byte("short")); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("weak new password: got %v", err)
	}
	if err := v.ChangePassword([]byte(pw), newPw); err != nil {
		t.Fatal(err)
	}

	// Checked before any further save: the backup must not open with the
	// old password.
	b := newVault(p.backup, p.backup+".unused", fastOpts)
	if err := b.Unlock([]byte(pw)); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("backup still opens with the old password: %v", err)
	}
	if err := b.Unlock(newPw); err != nil {
		t.Fatalf("backup does not open with the new password: %v", err)
	}
	addSample(t, v, "hunter3") // still usable after the change

	v2, _ := Open(p.vault, p.backup, fastOpts)
	if err := v2.Unlock([]byte(pw)); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("old password still works: %v", err)
	}
	if err := v2.Unlock(newPw); err != nil {
		t.Fatal(err)
	}
	if pr, _, _ := counts(t, v2); pr != 2 {
		t.Fatalf("got %d projects, want 2", pr)
	}

	v.Lock()
	if err := v.ChangePassword(newPw, []byte("another long one")); !errors.Is(err, ErrLocked) {
		t.Fatalf("ChangePassword while locked: got %v", err)
	}
}

func TestAutoLock(t *testing.T) {
	v, _ := create(t)
	now := time.Now()
	v.now = func() time.Time { return now }
	v.SetAutoLock(15 * time.Minute)
	v.Touch()

	now = now.Add(14 * time.Minute)
	if v.LockIfIdle() {
		t.Fatal("locked before the timeout")
	}
	v.View(func(*model.Data) error { return nil }) // activity resets the timer
	now = now.Add(14 * time.Minute)
	if v.LockIfIdle() {
		t.Fatal("activity did not postpone auto-lock")
	}
	now = now.Add(time.Minute)
	if !v.LockIfIdle() || v.Unlocked() {
		t.Fatal("did not lock after the timeout")
	}

	v.Unlock([]byte(pw))
	now = now.Add(14 * time.Minute)
	if err := v.Peek(func(*model.Data) error { return nil }); err != nil { // background read
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if !v.LockIfIdle() {
		t.Fatal("Peek postponed auto-lock")
	}
	if err := v.Peek(func(*model.Data) error { return nil }); !errors.Is(err, ErrLocked) {
		t.Fatalf("Peek while locked: got %v", err)
	}

	v.Unlock([]byte(pw))
	v.SetAutoLock(0)
	now = now.Add(1000 * time.Hour)
	if v.LockIfIdle() {
		t.Fatal("locked although auto-lock is disabled")
	}
}

func TestRunAutoLock(t *testing.T) {
	v, _ := create(t)
	v.SetAutoLock(time.Nanosecond)
	locked := make(chan struct{})
	v.OnLock(func() { close(locked) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go v.RunAutoLock(ctx, time.Millisecond)
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		t.Fatal("RunAutoLock never locked the vault")
	}
}

func TestDefaultParamsAreValid(t *testing.T) {
	if err := DefaultParams.validate(); err != nil {
		t.Fatal(err)
	}
	if err := (Params{Time: 1, MemoryKiB: 1024, Threads: 1}).validate(); err == nil {
		t.Fatal("parameters below the minimum were accepted")
	}
}

// Unlocking again (a second tab, say) must not bring back the data the file
// held when that unlock read it: it may be older than what is in memory.
func TestUnlockWhenUnlockedKeepsData(t *testing.T) {
	v, p := create(t)
	// Another Vault on the same file writes a version this one never saw;
	// it stands in for the file read by an unlock that started earlier.
	other, err := Open(p.vault, p.backup, fastOpts)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Unlock([]byte(pw)); err != nil {
		t.Fatal(err)
	}
	addSample(t, v, "hunter3")
	addSample(t, v, "hunter4")
	addSample(t, other, "hunter2") // the file now has 1 project, memory 2

	if err := v.Unlock([]byte(pw)); err != nil {
		t.Fatal(err)
	}
	if pr, _, _ := counts(t, v); pr != 2 {
		t.Fatalf("got %d projects after unlocking again, want the 2 in memory", pr)
	}
	if err := v.Unlock([]byte("wrong password!")); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("wrong password on an unlocked vault: got %v", err)
	}
	if !v.Unlocked() {
		t.Fatal("a wrong password locked the vault")
	}
}
