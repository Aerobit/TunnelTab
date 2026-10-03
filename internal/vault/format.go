package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"fmt"

	"golang.org/x/crypto/argon2"
)

// File format
//
// vault.enc is a small JSON document:
//
//	{
//	  "format":     "tunneltab-vault",
//	  "version":    1,
//	  "kdf":        {"name": "argon2id", "time": 4, "memoryKiB": 262144, "threads": 4, "salt": "<base64>"},
//	  "cipher":     "aes-256-gcm",
//	  "nonce":      "<base64, 12 bytes>",
//	  "ciphertext": "<base64>"
//	}
//
// The key is Argon2id(masterPassword, salt, params) → 32 bytes. The plaintext
// is the JSON of model.Data. The header fields (everything except nonce and
// ciphertext) are authenticated as GCM additional data, so changing any KDF
// parameter or the salt makes decryption fail instead of silently deriving a
// different key.

const (
	formatName    = "tunneltab-vault"
	formatVersion = 1
	kdfName       = "argon2id"
	cipherName    = "aes-256-gcm"

	keyLen   = 32
	saltLen  = 16
	nonceLen = 12
)

// maxFileSize bounds how much is read from disk before parsing, and so how
// large a vault may be saved (a variable for tests).
var maxFileSize = 64 << 20

// Params are the Argon2id cost parameters. They are stored in the vault
// header, so they can be raised in later versions without breaking existing
// vaults.
type Params struct {
	Time      uint32 `json:"time"`      // passes over memory
	MemoryKiB uint32 `json:"memoryKiB"` // memory in KiB
	Threads   uint8  `json:"threads"`   // parallelism
}

// DefaultParams take roughly 0.5–1 s and 256 MiB of memory on a typical PC,
// which makes offline password guessing slow and expensive.
var DefaultParams = Params{Time: 4, MemoryKiB: 256 * 1024, Threads: 4}

// Bounds accepted when reading a vault. They stop a tampered header from
// making the app allocate huge amounts of memory or hang, and reject values
// too weak to be deliberate.
const (
	minTime, maxTime       = 1, 20
	minMemoryKiB           = 8 * 1024
	maxMemoryKiB           = 1024 * 1024
	minThreads, maxThreads = 1, 16
)

func (p Params) validate() error {
	if p.Time < minTime || p.Time > maxTime ||
		p.MemoryKiB < minMemoryKiB || p.MemoryKiB > maxMemoryKiB ||
		p.Threads < minThreads || p.Threads > maxThreads {
		return fmt.Errorf("%w: key-derivation parameters out of range", ErrCorrupt)
	}
	return nil
}

type kdfHeader struct {
	Name string `json:"name"`
	Params
	Salt []byte `json:"salt"`
}

// header is the authenticated, unencrypted part of the file.
type header struct {
	Format  string    `json:"format"`
	Version int       `json:"version"`
	KDF     kdfHeader `json:"kdf"`
	Cipher  string    `json:"cipher"`
}

type file struct {
	header
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

func newHeader(p Params) (header, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return header{}, err
	}
	return header{
		Format:  formatName,
		Version: formatVersion,
		KDF:     kdfHeader{Name: kdfName, Params: p, Salt: salt},
		Cipher:  cipherName,
	}, nil
}

func (h header) validate() error {
	if h.Format != formatName {
		return fmt.Errorf("%w: not a TunnelTab vault", ErrCorrupt)
	}
	if h.Version > formatVersion {
		return ErrUnsupported
	}
	if h.Version != formatVersion || h.KDF.Name != kdfName || h.Cipher != cipherName {
		return fmt.Errorf("%w: unknown format details", ErrCorrupt)
	}
	if len(h.KDF.Salt) < saltLen || len(h.KDF.Salt) > 64 {
		return fmt.Errorf("%w: bad salt", ErrCorrupt)
	}
	return h.KDF.Params.validate()
}

// aad returns the bytes authenticated alongside the ciphertext.
// json.Marshal of a struct is deterministic (fields in declaration order).
func (h header) aad() []byte {
	b, err := json.Marshal(h)
	if err != nil {
		panic(err) // cannot happen: header has only plain fields
	}
	return b
}

// deriveKey runs Argon2id. The caller must wipe the result when done.
func deriveKey(password []byte, h header) []byte {
	p := h.KDF.Params
	return argon2.IDKey(password, h.KDF.Salt, p.Time, p.MemoryKiB, p.Threads, keyLen)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// seal encrypts plaintext with a fresh random nonce and returns the file bytes.
func seal(key []byte, h header, plaintext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	f := file{header: h, Nonce: nonce, Ciphertext: gcm.Seal(nil, nonce, plaintext, h.aad())}
	out, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// parse decodes and checks the file structure (not the password).
func parse(raw []byte) (file, error) {
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		return file{}, fmt.Errorf("%w: not valid JSON", ErrCorrupt)
	}
	if err := f.header.validate(); err != nil {
		return file{}, err
	}
	if len(f.Nonce) != nonceLen || len(f.Ciphertext) == 0 {
		return file{}, fmt.Errorf("%w: bad nonce or ciphertext", ErrCorrupt)
	}
	return f, nil
}

// open decrypts f with key. Any failure means the key is wrong or the file
// was modified; the two are indistinguishable by design.
func open(key []byte, f file) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	plaintext, err := gcm.Open(nil, f.Nonce, f.Ciphertext, f.header.aad())
	if err != nil {
		return nil, ErrWrongPassword
	}
	return plaintext, nil
}

func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
