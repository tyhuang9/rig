//go:build windows

package securetemp

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestValidatePrivateDirectoryRequiresCreatorDACLOnWindows(t *testing.T) {
	root := t.TempDir()
	private := filepath.Join(root, "private")
	if err := secureMkdir(private); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePrivateDirectory(private); err != nil {
		t.Fatalf("creator-protected directory rejected: %v", err)
	}

	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		t.Fatal("current user SID is unavailable")
	}
	sid := user.User.Sid.String()
	for _, test := range []struct {
		name string
		sddl string
	}{
		{name: "additional identity", sddl: "O:" + sid + "D:P(A;;FA;;;" + sid + ")(A;;FR;;;WD)"},
		{name: "inheritable entry", sddl: "O:" + sid + "D:P(A;CI;FA;;;" + sid + ")"},
		{name: "different identity", sddl: "O:" + sid + "D:P(A;;FA;;;WD)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(root, test.name)
			createWindowsDirectoryWithDescriptor(t, path, test.sddl)
			if err := ValidatePrivateDirectory(path); err == nil {
				t.Fatal("directory with non-creator DACL was accepted")
			}
		})
	}

	t.Run("unprotected", func(t *testing.T) {
		path := filepath.Join(root, "unprotected")
		if err := secureMkdir(path); err != nil {
			t.Fatal(err)
		}
		descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		dacl, _, err := descriptor.DACL()
		if err != nil || dacl == nil {
			t.Fatalf("read test DACL: %v", err)
		}
		if err := windows.SetNamedSecurityInfo(
			path,
			windows.SE_FILE_OBJECT,
			windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION,
			nil,
			nil,
			dacl,
			nil,
		); err != nil {
			t.Fatal(err)
		}
		if err := ValidatePrivateDirectory(path); err == nil {
			t.Fatal("directory with inheritable DACL was accepted")
		}
	})
}

func TestValidatePrivateDirectoryRejectsWindowsReparsePoint(t *testing.T) {
	target := filepath.Join(t.TempDir(), "target")
	if err := secureMkdir(target); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "working")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink privilege unavailable: %v", err)
	}
	if err := ValidatePrivateDirectory(link); err == nil {
		t.Fatal("reparse-point directory was accepted")
	}
}

func TestPrivateDirectoryGuardPinsPrivateLeafBeneathPermissiveWindowsAncestor(t *testing.T) {
	root, err := os.MkdirTemp(".", ".private-directory-guard-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove guard fixture: %v", err)
		}
	})
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		t.Fatal("current user SID is unavailable")
	}
	ancestor := filepath.Join(root, "permissive")
	createWindowsDirectoryWithDescriptor(t, ancestor, "O:"+user.User.Sid.String()+"D:P(A;;FA;;;WD)")
	working := filepath.Join(ancestor, "working")
	if err := secureMkdir(working); err != nil {
		t.Fatal(err)
	}
	guard, err := AcquirePrivateDirectoryGuard(working)
	if err != nil {
		t.Fatalf("acquire private-directory guard: %v", err)
	}
	renamed := filepath.Join(root, "renamed")
	if err := os.Rename(ancestor, renamed); err == nil {
		_ = guard.Close()
		t.Fatal("guard permitted ancestor rename")
	}
	if err := os.Remove(working); err == nil {
		_ = guard.Close()
		t.Fatal("guard permitted working-directory deletion")
	}
	rootRenamed := root + "-renamed"
	if err := os.Rename(root, rootRenamed); err == nil {
		_ = guard.Close()
		root = rootRenamed
		t.Fatal("guard permitted higher-ancestor rename")
	}
	if err := guard.Close(); err != nil {
		t.Fatalf("close private-directory guard: %v", err)
	}
	if err := guard.Close(); err != nil {
		t.Fatalf("close private-directory guard twice: %v", err)
	}
	if err := os.Rename(ancestor, renamed); err != nil {
		t.Fatalf("ancestor remained pinned after guard close: %v", err)
	}
}

func createWindowsDirectoryWithDescriptor(t *testing.T, path, sddl string) {
	t.Helper()
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatalf("parse test security descriptor: %v", err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	attributes := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	if err := windows.CreateDirectory(name, attributes); err != nil {
		t.Fatalf("create test directory: %v", err)
	}
}
