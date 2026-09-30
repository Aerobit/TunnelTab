//go:build windows

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// sddl returns a path's access list in SDDL form, e.g. "D:P(A;OICI;FA;;;SY)…".
func sddl(t *testing.T, path string) string {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	return sd.String()
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
	me := user.User.Sid.String()
	d := sddl(t, dir)
	if !strings.HasPrefix(d, "D:P") {
		t.Errorf("folder access list is not protected from inheritance: %s", d)
	}
	for _, p := range []string{dir, existing, created} {
		s := sddl(t, p)
		if !strings.Contains(s, me) {
			t.Errorf("%s: current user missing: %s", filepath.Base(p), s)
		}
		// No access for Everyone (WD), Users (BU) or Authenticated Users (AU).
		for _, other := range []string{";;;WD)", ";;;BU)", ";;;AU)"} {
			if strings.Contains(s, other) {
				t.Errorf("%s: grants access to %s: %s", filepath.Base(p), other, s)
			}
		}
	}
}
