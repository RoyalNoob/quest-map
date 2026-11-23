//go:build windows

package server

import (
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestRunDirIsOwnerOnly(t *testing.T) {
	root := t.TempDir()
	l, err := AcquireLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	sd, err := windows.GetNamedSecurityInfo(filepath.Join(root, ".run"), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Error(".run inherits its parent's access list")
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	// Windows may split one inheritable entry into two (this folder, and inherit-only); all must be the user's.
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			t.Fatal(err)
		}
		if sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)); !sid.Equals(user.User.Sid) {
			t.Errorf("access entry %d is for %s, not the current user", i, sid)
		}
	}
}
