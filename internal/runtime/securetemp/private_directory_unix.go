//go:build !windows

package securetemp

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func validatePrivateDirectory(path string, expected os.FileInfo) error {
	file, err := os.Open(path)
	if err != nil {
		return fmtPrivateDirectoryError(err)
	}
	defer file.Close()

	opened, err := file.Stat()
	if err != nil || !opened.IsDir() || !os.SameFile(expected, opened) {
		return errors.New("private directory changed during validation")
	}
	stat, ok := opened.Sys().(*syscall.Stat_t)
	if !ok || stat == nil || uint64(stat.Uid) != uint64(os.Geteuid()) || opened.Mode().Perm() != 0o700 {
		return errors.New("private directory is not owned and accessible only by the current user")
	}
	current, err := os.Lstat(path)
	if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, current) {
		return errors.New("private directory changed during validation")
	}
	return validatePrivateDirectoryAncestors(path, current)
}

func validatePrivateDirectoryAncestors(path string, child os.FileInfo) error {
	euid := uint64(os.Geteuid())
	for current := filepath.Dir(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("private directory ancestor is unsafe")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		childStat, childOK := child.Sys().(*syscall.Stat_t)
		if !ok || stat == nil || !childOK || childStat == nil {
			return errors.New("private directory ancestor ownership is unavailable")
		}
		owner := uint64(stat.Uid)
		if owner != euid && owner != 0 {
			return errors.New("private directory ancestor has an untrusted owner")
		}
		if info.Mode().Perm()&0o022 != 0 {
			childOwner := uint64(childStat.Uid)
			if info.Mode()&os.ModeSticky == 0 || (childOwner != euid && childOwner != 0) {
				return errors.New("private directory ancestor grants untrusted rename authority")
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
		child = info
	}
}
