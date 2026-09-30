//go:build windows

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// aclSIDs returns the account of every entry in a path's access list, and
// whether the list is protected from inheriting its parent's entries.
//
// Entries are compared by SID rather than by their text (SDDL) form,
// because SDDL abbreviates well-known accounts (e.g. "LA" for the built-in
// Administrator that CI runs as).
func aclSIDs(t *testing.T, path string) (sids []*windows.SID, protected bool) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < int(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, uint32(i), &ace); err != nil {
			t.Fatal(err)
		}
		sids = append(sids, (*windows.SID)(unsafe.Pointer(&ace.SidStart)))
	}
	return sids, control&windows.SE_DACL_PROTECTED != 0
}

func TestRestrictToOwnerWindows(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	os.Mkdir(dir, 0o700)
	existing := filepath.Join(dir, "existing.txt")
	os.WriteFile(existing, []byte("x"), 0o600)

	if err := RestrictToOwner(dir); err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(dir, "created-after.txt")
	os.WriteFile(created, []byte("x"), 0o600)

	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal(err)
	}

	if _, protected := aclSIDs(t, dir); !protected {
		t.Error("the folder still inherits permissions from its parent")
	}
	for _, p := range []string{dir, existing, created} {
		sids, _ := aclSIDs(t, p)
		hasUser := false
		for _, sid := range sids {
			switch {
			case sid.Equals(user.User.Sid):
				hasUser = true
			case sid.Equals(system):
			default:
				t.Errorf("%s: grants access to another account: %s", filepath.Base(p), sid)
			}
		}
		if !hasUser {
			t.Errorf("%s: the current user has no access (entries: %s)", filepath.Base(p), strings.TrimSpace(sidList(sids)))
		}
	}
}

func sidList(sids []*windows.SID) string {
	var b strings.Builder
	for _, s := range sids {
		b.WriteString(s.String() + " ")
	}
	return b.String()
}
