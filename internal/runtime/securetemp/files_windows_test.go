//go:build windows

package securetemp

import (
	"testing"
	"unsafe"

	"github.com/google/uuid"
	"golang.org/x/sys/windows"
)

const windowsFileAllAccess windows.ACCESS_MASK = 0x001f01ff

func TestWindowsProtectedFilesUseCurrentUserOnlyDACL(t *testing.T) {
	manager, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files, err := manager.Create(uuid.NewString(), 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := files.Cleanup(); err != nil {
			t.Errorf("cleanup protected files: %v", err)
		}
	})
	if err := files.WriteEnv([]byte("RIG_TEST=value\n")); err != nil {
		t.Fatal(err)
	}
	if err := files.WriteCompose([]byte(`{"services":{"app":{"image":"example.invalid/test"}}}`)); err != nil {
		t.Fatal(err)
	}

	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatalf("read current token user: %v", err)
	}
	if user == nil || user.User.Sid == nil {
		t.Fatal("current token user SID is unavailable")
	}
	attributes, err := currentUserSecurityAttributes()
	if err != nil {
		t.Fatalf("build current-user security attributes: %v", err)
	}
	assertWindowsExplicitCurrentUserOwner(t, "creation security attributes", attributes.SecurityDescriptor, user.User.Sid)

	for name, path := range map[string]string{
		"operation-directory": files.Directory,
		"environment-file":    files.EnvPath,
		"compose-file":        files.ComposePath,
	} {
		t.Run(name, func(t *testing.T) {
			assertWindowsCurrentUserOnlyDACL(t, path, user.User.Sid)
		})
	}
}

func assertWindowsExplicitCurrentUserOwner(t *testing.T, name string, descriptor *windows.SECURITY_DESCRIPTOR, currentUser *windows.SID) {
	t.Helper()
	if descriptor == nil || !descriptor.IsValid() {
		t.Fatalf("security descriptor for %s is invalid", name)
	}
	owner, defaulted, err := descriptor.Owner()
	if err != nil {
		t.Fatalf("read owner for %s: %v", name, err)
	}
	if owner == nil || defaulted || !owner.Equals(currentUser) {
		t.Fatalf("owner for %s is not the explicit current user", name)
	}
}

func assertWindowsCurrentUserOnlyDACL(t *testing.T, path string, currentUser *windows.SID) {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatalf("read security descriptor for %q: %v", path, err)
	}
	if descriptor == nil || !descriptor.IsValid() {
		t.Fatalf("security descriptor for %q is invalid", path)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatalf("read security descriptor control for %q: %v", path, err)
	}
	if control&windows.SE_DACL_PRESENT == 0 {
		t.Fatalf("DACL for %q is absent", path)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("DACL for %q permits inherited access", path)
	}

	assertWindowsExplicitCurrentUserOwner(t, path, descriptor, currentUser)

	dacl, defaulted, err := descriptor.DACL()
	if err != nil {
		t.Fatalf("read DACL for %q: %v", path, err)
	}
	if dacl == nil || defaulted {
		t.Fatalf("DACL for %q is nil or defaulted", path)
	}
	if dacl.AceCount != 1 {
		t.Fatalf("DACL for %q has %d ACEs, want exactly 1", path, dacl.AceCount)
	}

	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		t.Fatalf("read DACL ACE for %q: %v", path, err)
	}
	if ace == nil {
		t.Fatalf("DACL ACE for %q is nil", path)
	}
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
		t.Fatalf("DACL ACE for %q has type %d, want ACCESS_ALLOWED", path, ace.Header.AceType)
	}
	if ace.Header.AceFlags != 0 {
		t.Fatalf("DACL ACE for %q has inheritance flags %#x, want 0", path, ace.Header.AceFlags)
	}
	if ace.Mask != windowsFileAllAccess {
		t.Fatalf("DACL ACE for %q has mask %#x, want FILE_ALL_ACCESS %#x", path, ace.Mask, windowsFileAllAccess)
	}
	trustee := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if trustee == nil || !trustee.Equals(currentUser) {
		t.Fatalf("DACL ACE for %q does not grant only the current user", path)
	}
}
