package model

import (
	"crypto/rand"
	"fmt"
	"regexp"
)

// NewID returns a random RFC 4122 version 4 UUID. IDs are always generated
// here; IDs supplied by the browser are only used to look items up.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand failed: " + err.Error()) // never happens on supported OSes
	}
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

var idPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// IsValidID reports whether s has the format produced by NewID.
func IsValidID(s string) bool {
	return idPattern.MatchString(s)
}
