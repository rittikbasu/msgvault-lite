//go:build unix

// Package fileutil provides cross-platform secure file helpers.
//
// On non-Windows targets, write helpers are thin wrappers over os.* while
// ReadPrivateFile validates ownership, permissions, and final-path identity.
// On Windows, owner-only modes additionally set a DACL restricting access to
// the current user.
package fileutil

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// ReadPrivateFile reads a regular file owned by the current user with no
// group or world permissions. It rejects a final-path symbolic link and
// verifies the opened file is the same inode that was inspected.
func ReadPrivateFile(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refuse to read symbolic link: %s", path)
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("private file is not a regular file: %s", path)
	}

	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open private file: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	defer func() { _ = f.Close() }()

	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, after) {
		return nil, fmt.Errorf("private file changed while opening: %s", path)
	}
	if !after.Mode().IsRegular() {
		return nil, fmt.Errorf("private file is not a regular file: %s", path)
	}
	if after.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf(
			"private file permissions for %s are too open (%04o); use chmod 600 %s",
			path, after.Mode().Perm(), path,
		)
	}
	stat, ok := after.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("inspect private file ownership: %s", path)
	}
	if int64(stat.Uid) != int64(os.Geteuid()) {
		return nil, fmt.Errorf("private file is not owned by the current user: %s", path)
	}

	return io.ReadAll(f)
}

// SyncDir flushes directory metadata so a preceding rename is durable.
func SyncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := dir.Sync(); err != nil {
		_ = dir.Close()
		return err
	}
	return dir.Close()
}

// ReplaceFile atomically replaces target with temp and flushes the containing
// directory so the rename survives a crash after success is reported.
func ReplaceFile(temp, target string) error {
	if err := os.Rename(temp, target); err != nil {
		return err
	}
	return SyncDir(filepath.Dir(target))
}

// SecureWriteFile writes data to the named file, creating it if necessary.
func SecureWriteFile(path string, data []byte, perm os.FileMode) error {
	// codeql[go/path-injection] -- callers provide local user-owned paths;
	// this helper preserves os.WriteFile semantics on non-Windows platforms.
	return os.WriteFile(path, data, perm)
}

// SecureMkdirAll creates a directory path and all parents that do not yet exist.
func SecureMkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

// SecureChmod changes the mode of the named file.
func SecureChmod(path string, perm os.FileMode) error {
	return os.Chmod(path, perm)
}

// SecureOpenFile opens the named file with specified flag and permissions.
func SecureOpenFile(path string, flag int, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(path, flag, perm)
}
