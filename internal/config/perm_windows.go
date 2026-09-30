//go:build windows

package config

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// RestrictToOwner replaces the data folder's permissions with a protected
// access list that grants full control to the current user and to SYSTEM
// only, inherited by everything inside. Without this, a portable folder on a
// shared drive would inherit permissions that may let other Windows users
// read it (including instance.json, which can request a dashboard login).
//
// It fails on file systems without permissions (FAT32/exFAT USB sticks);
// the caller logs that and continues, since the vault is encrypted anyway.
func RestrictToOwner(dir string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("find current user: %w", err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return fmt.Errorf("find SYSTEM account: %w", err)
	}
	entry := func(sid *windows.SID, kind windows.TRUSTEE_TYPE) windows.EXPLICIT_ACCESS {
		return windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.SET_ACCESS,
			Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  kind,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		}
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		entry(user.User.Sid, windows.TRUSTEE_IS_USER),
		entry(system, windows.TRUSTEE_IS_WELL_KNOWN_GROUP),
	}, nil)
	if err != nil {
		return fmt.Errorf("build access list: %w", err)
	}
	// PROTECTED: stop inheriting from the parent folder. Windows applies the
	// new inheritable entries to existing files and folders inside.
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}
