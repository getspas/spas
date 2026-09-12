//go:build windows

package app

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func denyWorkspaceCreation(t *testing.T, root string) {
	t.Helper()
	securityInformation := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION)
	original, err := windows.GetNamedSecurityInfo(root, windows.SE_FILE_OBJECT, securityInformation)
	if err != nil || original == nil {
		t.Skipf("cannot capture workspace ACL: %v", err)
	}
	originalDACL, _, err := original.DACL()
	if err != nil {
		t.Skipf("cannot read workspace ACL DACL: %v", err)
	}
	t.Cleanup(func() {
		if err := windows.SetNamedSecurityInfo(
			root,
			windows.SE_FILE_OBJECT,
			windows.DACL_SECURITY_INFORMATION,
			nil,
			nil,
			originalDACL,
			nil,
		); err != nil {
			t.Errorf("restore workspace ACL: %v", err)
		}
	})

	world, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Skipf("cannot create Everyone SID: %v", err)
	}
	deniedDACL, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		// FILE_WRITE_DATA and FILE_APPEND_DATA are the Windows directory
		// rights named FILE_ADD_FILE and FILE_ADD_SUBDIRECTORY. Keep the
		// denial limited to creating children in this fixture root.
		AccessPermissions: windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA,
		AccessMode:        windows.DENY_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(world),
		},
	}}, originalDACL)
	if err != nil {
		t.Skipf("cannot construct workspace denial ACL: %v", err)
	}
	if err := windows.SetNamedSecurityInfo(root, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, deniedDACL, nil); err != nil {
		t.Skipf("cannot apply workspace denial ACL: %v", err)
	}

	file, err := os.CreateTemp(root, ".spas-case-Probe-test-*")
	if err == nil {
		name := file.Name()
		_ = file.Close()
		_ = os.Remove(name)
		t.Skip("filesystem or test account bypasses Windows directory ACL denial")
	}
	if !errors.Is(err, os.ErrPermission) && !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("os.CreateTemp(%q) error = %v, want access denied", root, err)
	}
}
