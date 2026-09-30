package platform

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/Aerobit/TunnelTab/internal/atomicfile"
)

// Instance describes the running TunnelTab, written to data/instance.json so
// that launching the program a second time opens the existing dashboard
// instead of starting another copy.
//
// Secret authenticates the second launch to the running instance. It lives
// in the owner-only data folder, so other users on the PC can't read it and
// ask the running app for a login link.
type Instance struct {
	PID    int    `json:"pid"`
	Port   int    `json:"port"`
	Secret string `json:"secret"`
}

// NewInstance returns an Instance for this process with a fresh secret.
func NewInstance(port int) (Instance, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return Instance{}, err
	}
	return Instance{PID: os.Getpid(), Port: port, Secret: hex.EncodeToString(b)}, nil
}

// WriteInstance saves inst to path (owner-only permissions).
func WriteInstance(path string, inst Instance) error {
	data, err := json.Marshal(inst)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(path, data, 0o600)
}

// ReadInstance loads the instance file. ok is false if there is none.
func ReadInstance(path string) (inst Instance, ok bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Instance{}, false, nil
	}
	if err != nil {
		return Instance{}, false, err
	}
	if err := json.Unmarshal(data, &inst); err != nil || inst.Port == 0 || inst.Secret == "" {
		return Instance{}, false, fmt.Errorf("instance file is damaged")
	}
	return inst, true, nil
}

// RemoveInstance deletes the instance file if it still belongs to this
// process.
func RemoveInstance(path string) {
	if inst, ok, _ := ReadInstance(path); ok && inst.PID == os.Getpid() {
		os.Remove(path)
	}
}
