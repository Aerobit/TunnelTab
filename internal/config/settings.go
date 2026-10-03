package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/Aerobit/TunnelTab/internal/atomicfile"
)

// DefaultPort is the dashboard's default port on 127.0.0.1. A fixed default
// lets the browser keep its session cookie and bookmarks between runs.
const DefaultPort = 47811

// Settings are the user's non-secret preferences, stored in settings.json.
// Anything secret belongs in the vault instead.
type Settings struct {
	// Port is the dashboard port on 127.0.0.1 (1024–65535).
	Port int `json:"port"`
	// AutoLockMinutes locks the vault after this many minutes without
	// activity (1–1440), or never when 0.
	AutoLockMinutes int `json:"autoLockMinutes"`
	// CloseTunnelsOnLock stops all tunnels and terminals when the vault locks.
	CloseTunnelsOnLock bool `json:"closeTunnelsOnLock"`
	// CloseAction is what closing the last dashboard tab does: CloseKeepRunning
	// or CloseQuit (the default; "" means the same).
	CloseAction string `json:"closeAction,omitempty"`
}

// Settings.CloseAction values.
const (
	CloseKeepRunning = "tray"
	CloseQuit        = "quit"
)

// DefaultSettings returns the settings used on first run.
func DefaultSettings() Settings {
	return Settings{
		Port:               DefaultPort,
		AutoLockMinutes:    15,
		CloseTunnelsOnLock: false,
		CloseAction:        CloseQuit,
	}
}

// Validate reports whether all settings are within their allowed ranges.
func (s Settings) Validate() error {
	if s.Port < 1024 || s.Port > 65535 {
		return fmt.Errorf("port must be between 1024 and 65535, got %d", s.Port)
	}
	if s.AutoLockMinutes < 0 || s.AutoLockMinutes > 1440 {
		return fmt.Errorf("auto-lock must be between 0 (never) and 1440 minutes, got %d", s.AutoLockMinutes)
	}
	if s.CloseAction != "" && s.CloseAction != CloseKeepRunning && s.CloseAction != CloseQuit {
		return fmt.Errorf("closeAction must be \"\", %q or %q, got %q", CloseKeepRunning, CloseQuit, s.CloseAction)
	}
	return nil
}

// LoadSettings reads settings.json. A missing file yields the defaults, and
// fields missing from the file keep their default values.
func LoadSettings(path string) (Settings, error) {
	s := DefaultSettings()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, fmt.Errorf("read settings: %w", err)
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return DefaultSettings(), fmt.Errorf("settings.json is not valid JSON: %w", err)
	}
	if err := s.Validate(); err != nil {
		return DefaultSettings(), fmt.Errorf("settings.json: %w", err)
	}
	return s, nil
}

// SaveSettings validates s and writes it to path atomically.
func SaveSettings(path string, s Settings) error {
	if err := s.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(path, append(data, '\n'), 0o600)
}
