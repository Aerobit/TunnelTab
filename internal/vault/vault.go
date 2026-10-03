package vault

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"reflect"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Aerobit/TunnelTab/internal/atomicfile"
	"github.com/Aerobit/TunnelTab/internal/model"
)

// Errors returned by the vault. Messages are shown to the user.
var (
	ErrExists        = errors.New("a vault already exists")
	ErrNoVault       = errors.New("no vault found")
	ErrLocked        = errors.New("vault is locked")
	ErrWrongPassword = errors.New("wrong master password, or the vault file is damaged")
	ErrCorrupt       = errors.New("vault file is damaged")
	ErrUnsupported   = errors.New("vault was created by a newer version of TunnelTab")
	ErrWeakPassword  = fmt.Errorf("master password must be at least %d characters", MinPasswordLen)
	// ErrLockedMeanwhile means the vault was locked while an unlock was
	// under way; that unlock is abandoned rather than undoing the lock.
	ErrLockedMeanwhile = errors.New("TunnelTab was locked while unlocking: please try again")
)

// MinPasswordLen is the minimum master password length, in characters.
const MinPasswordLen = 8

// Vault is the encrypted store for all TunnelTab data. It is safe for
// concurrent use.
//
// While unlocked it holds the derived key and the decrypted data in memory.
// Locking wipes the key and drops the data. Go strings (such as the secrets
// inside model.Data) can't be wiped from memory; they are released for
// garbage collection.
type Vault struct {
	path       string
	backupPath string
	params     Params // for new headers (Create, ChangePassword)

	mu       sync.RWMutex
	hdr      header
	key      []byte      // nil while locked
	data     *model.Data // nil while locked
	lastUsed time.Time
	locks    uint64        // how often Lock locked; see Unlock
	autoLock time.Duration // 0 = never
	onLock   []func()

	now func() time.Time // replaceable in tests
}

// Options configure a Vault. The zero value uses the defaults.
type Options struct {
	// Params are the Argon2id costs for newly written headers
	// (DefaultParams if zero).
	Params Params
	// AutoLock locks the vault after this long without activity (0 = never).
	AutoLock time.Duration
}

func newVault(path, backupPath string, opts Options) *Vault {
	p := opts.Params
	if p == (Params{}) {
		p = DefaultParams
	}
	return &Vault{path: path, backupPath: backupPath, params: p, autoLock: opts.AutoLock, now: time.Now}
}

// Exists reports whether a vault file is present at path.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Create makes a new, empty vault protected by password and returns it
// unlocked. It fails with ErrExists if a vault is already there.
func Create(path, backupPath string, password []byte, opts Options) (*Vault, error) {
	if err := checkNewPassword(password); err != nil {
		return nil, err
	}
	if Exists(path) {
		return nil, ErrExists
	}
	v := newVault(path, backupPath, opts)
	if err := v.params.validate(); err != nil {
		return nil, err
	}
	h, err := newHeader(v.params)
	if err != nil {
		return nil, err
	}
	key := deriveKey(password, h)
	data := model.New()
	if err := v.write(key, h, data); err != nil {
		wipe(key)
		return nil, err
	}
	v.hdr, v.key, v.data, v.lastUsed = h, key, data, v.now()
	return v, nil
}

// Open prepares an existing vault for unlocking. The vault starts locked.
// The file's structure is checked now so problems are reported early.
func Open(path, backupPath string, opts Options) (*Vault, error) {
	if _, err := readFile(path); err != nil {
		return nil, err
	}
	return newVault(path, backupPath, opts), nil
}

// Unlock decrypts the vault with password. It re-reads the file, so it also
// picks up a vault restored from backup while the app was locked.
//
// Unlocking an unlocked vault only checks the password: the data in memory
// is newer than (or the same as) the file read here, since it was read
// before the key derivation, which takes a while. If the vault is locked
// during that time, the unlock fails with ErrLockedMeanwhile: the lock came
// later, and the file read may be older than what was saved before it.
func (v *Vault) Unlock(password []byte) error {
	v.mu.RLock()
	locks := v.locks
	v.mu.RUnlock()
	f, err := readFile(v.path)
	if err != nil {
		return err
	}
	key := deriveKey(password, f.header)
	plaintext, err := open(key, f)
	if err != nil {
		wipe(key)
		return err
	}
	data, err := decodeData(plaintext)
	wipe(plaintext)
	if err != nil {
		wipe(key)
		return err
	}

	if testHookUnlocking != nil {
		testHookUnlocking()
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.key != nil {
		// Already unlocked (another tab, say). Keep the current data; the
		// password must still be the current one (it may have been changed
		// since the file was read).
		same := reflect.DeepEqual(f.header, v.hdr) && subtle.ConstantTimeCompare(key, v.key) == 1
		wipe(key)
		if !same {
			return ErrWrongPassword
		}
		v.lastUsed = v.now()
		return nil
	}
	if v.locks != locks {
		wipe(key)
		return ErrLockedMeanwhile
	}
	v.hdr, v.key, v.data, v.lastUsed = f.header, key, data, v.now()
	return nil
}

// testHookUnlocking, if set (by tests), runs in Unlock between reading the
// file and using it.
var testHookUnlocking func()

// Lock wipes the key and forgets the decrypted data, then runs the OnLock
// callbacks. Locking a locked vault does nothing.
func (v *Vault) Lock() {
	v.mu.Lock()
	if v.key == nil {
		v.mu.Unlock()
		return
	}
	wipe(v.key)
	v.key, v.data = nil, nil
	v.locks++
	callbacks := append([]func(){}, v.onLock...)
	v.mu.Unlock()

	for _, fn := range callbacks {
		fn()
	}
}

// Unlocked reports whether the vault is currently unlocked.
func (v *Vault) Unlocked() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.key != nil
}

// View calls fn with a copy of the data. Changes made by fn are discarded.
func (v *Vault) View(fn func(d *model.Data) error) error {
	v.mu.Lock()
	if v.key == nil {
		v.mu.Unlock()
		return ErrLocked
	}
	d := v.data.Clone()
	v.lastUsed = v.now()
	v.mu.Unlock()
	return fn(d)
}

// Peek is View for background work (health checks, reconnects): it does not
// count as activity, so it never postpones auto-lock.
func (v *Vault) Peek(fn func(d *model.Data) error) error {
	v.mu.RLock()
	if v.key == nil {
		v.mu.RUnlock()
		return ErrLocked
	}
	d := v.data.Clone()
	v.mu.RUnlock()
	return fn(d)
}

// Update calls fn with a copy of the data. If fn returns nil and the result
// is valid, the copy is saved to disk and becomes the current data. If
// anything fails, neither the file nor the in-memory data changes.
func (v *Vault) Update(fn func(d *model.Data) error) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.key == nil {
		return ErrLocked
	}
	d := v.data.Clone()
	if err := fn(d); err != nil {
		return err
	}
	if err := d.Validate(); err != nil {
		return err
	}
	if err := v.write(v.key, v.hdr, d); err != nil {
		return err
	}
	v.data = d
	v.lastUsed = v.now()
	return nil
}

// ChangePassword re-encrypts the vault under newPassword, with a new salt
// and the current default cost parameters. The vault must be unlocked and
// oldPassword must be correct.
func (v *Vault) ChangePassword(oldPassword, newPassword []byte) error {
	if err := checkNewPassword(newPassword); err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.key == nil {
		return ErrLocked
	}
	check := deriveKey(oldPassword, v.hdr)
	ok := subtle.ConstantTimeCompare(check, v.key) == 1
	wipe(check)
	if !ok {
		return ErrWrongPassword
	}

	h, err := newHeader(v.params)
	if err != nil {
		return err
	}
	key := deriveKey(newPassword, h)
	if err := v.write(key, h, v.data); err != nil {
		wipe(key)
		return err
	}
	wipe(v.key)
	v.hdr, v.key, v.lastUsed = h, key, v.now()

	// write() backed up the previous file, which is still encrypted with the
	// old password. Replace the backup so the old password (which may have
	// been changed because it leaked) no longer opens anything.
	current, err := os.ReadFile(v.path)
	if err == nil {
		err = atomicfile.WriteFile(v.backupPath, current, 0o600)
	}
	if err != nil {
		return fmt.Errorf("password changed, but the old backup could not be replaced; delete %s: %w", v.backupPath, err)
	}
	return nil
}

// Touch records user activity, postponing auto-lock.
func (v *Vault) Touch() {
	v.mu.Lock()
	v.lastUsed = v.now()
	v.mu.Unlock()
}

// SetAutoLock changes the inactivity timeout (0 = never).
func (v *Vault) SetAutoLock(d time.Duration) {
	v.mu.Lock()
	v.autoLock = d
	v.mu.Unlock()
}

// OnLock registers fn to run after every lock (manual or automatic), e.g. to
// close tunnels. Callbacks run without the vault's lock held.
func (v *Vault) OnLock(fn func()) {
	v.mu.Lock()
	v.onLock = append(v.onLock, fn)
	v.mu.Unlock()
}

// LockIfIdle locks the vault if auto-lock is enabled and the vault has been
// unused for at least the timeout. It reports whether it locked.
func (v *Vault) LockIfIdle() bool {
	v.mu.RLock()
	idle := v.key != nil && v.autoLock > 0 && v.now().Sub(v.lastUsed) >= v.autoLock
	v.mu.RUnlock()
	if idle {
		v.Lock()
	}
	return idle
}

// RunAutoLock checks for inactivity every interval until ctx is done.
func (v *Vault) RunAutoLock(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			v.LockIfIdle()
		}
	}
}

// write encrypts data and saves it atomically, first copying the current
// file to the backup path. Caller holds v.mu (or owns v exclusively).
func (v *Vault) write(key []byte, h header, data *model.Data) error {
	plaintext, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encode vault: %w", err)
	}
	out, err := seal(key, h, plaintext)
	wipe(plaintext)
	if err != nil {
		return fmt.Errorf("encrypt vault: %w", err)
	}
	if current, err := os.ReadFile(v.path); err == nil {
		if err := atomicfile.WriteFile(v.backupPath, current, 0o600); err != nil {
			return fmt.Errorf("back up vault: %w", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read vault for backup: %w", err)
	}
	if err := atomicfile.WriteFile(v.path, out, 0o600); err != nil {
		return fmt.Errorf("save vault: %w", err)
	}
	return nil
}

func readFile(path string) (file, error) {
	fh, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return file{}, ErrNoVault
	}
	if err != nil {
		return file{}, fmt.Errorf("open vault: %w", err)
	}
	defer fh.Close()
	raw, err := io.ReadAll(io.LimitReader(fh, maxFileSize+1))
	if err != nil {
		return file{}, fmt.Errorf("read vault: %w", err)
	}
	if len(raw) > maxFileSize {
		return file{}, fmt.Errorf("%w: file too large", ErrCorrupt)
	}
	return parse(raw)
}

func decodeData(plaintext []byte) (*model.Data, error) {
	var d model.Data
	if err := json.Unmarshal(plaintext, &d); err != nil {
		return nil, fmt.Errorf("%w: contents are not valid", ErrCorrupt)
	}
	if d.Version > model.CurrentVersion {
		return nil, ErrUnsupported
	}
	// Future schema migrations go here (d.Version < model.CurrentVersion).
	if d.Projects == nil {
		d.Projects = []model.Project{}
	}
	if d.Servers == nil {
		d.Servers = []model.Server{}
	}
	if d.Services == nil {
		d.Services = []model.Service{}
	}
	if d.KnownHosts == nil {
		d.KnownHosts = []model.KnownHost{}
	}
	if err := d.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	return &d, nil
}

func checkNewPassword(p []byte) error {
	if !utf8.Valid(p) || utf8.RuneCount(p) < MinPasswordLen {
		return ErrWeakPassword
	}
	return nil
}
