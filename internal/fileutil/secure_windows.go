//go:build windows

package fileutil

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// ReadPrivateFile reads a regular file while rejecting a final-path symbolic
// link and a path swap between inspection and open. Windows has no POSIX
// owner/mode bits; write helpers apply owner-only DACLs separately.
func ReadPrivateFile(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refuse to read symbolic link: %s", path)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
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

	return io.ReadAll(f)
}

// SyncDir is a no-op on Windows, where directory flushing requires
// platform-specific handles and has no portable Go equivalent.
func SyncDir(_ string) error {
	return nil
}

// isOwnerOnly returns true if the permission mode grants nothing to group or other.
func isOwnerOnly(perm os.FileMode) bool {
	return perm&0077 == 0
}

// restrictToCurrentUser sets a DACL on path that grants GENERIC_ALL only to
// the current user and blocks inherited ACEs. For directories, the DACL
// includes CONTAINER_INHERIT_ACE | OBJECT_INHERIT_ACE so that child files
// and subdirectories automatically inherit the restriction. Errors are
// returned to the caller; the file was already created with the requested
// Unix mode, so callers may treat DACL failures as non-fatal warnings.
func restrictToCurrentUser(path string) error {
	token := windows.GetCurrentProcessToken()

	user, err := token.GetTokenUser()
	if err != nil {
		return fmt.Errorf("fileutil: get current user SID for %s: %w", path, err)
	}

	trustee := windows.TrusteeValueFromSID(user.User.Sid)

	// For directories, enable inheritance so children get the same restriction.
	// For files, NO_INHERITANCE is correct (files don't have children).
	var inherit uint32 = windows.NO_INHERITANCE
	info, statErr := os.Stat(path)
	if statErr == nil && info.IsDir() {
		inherit = windows.CONTAINER_INHERIT_ACE | windows.OBJECT_INHERIT_ACE
	}

	ea := []windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.SET_ACCESS,
			Inheritance:       inherit,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_USER,
				TrusteeValue: trustee,
			},
		},
	}

	acl, err := windows.ACLFromEntries(ea, nil)
	if err != nil {
		return fmt.Errorf("fileutil: build ACL for %s: %w", path, err)
	}

	secInfo := windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION
	err = windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.SECURITY_INFORMATION(secInfo),
		nil, // owner SID (unchanged)
		nil, // group SID (unchanged)
		acl, // DACL
		nil, // SACL (unchanged)
	)
	if err != nil {
		return fmt.Errorf("fileutil: set DACL on %s: %w", path, err)
	}
	return nil
}

// SecureWriteFile writes data to the named file, creating it if necessary.
// For owner-only modes, a DACL restricting access to the current user is applied.
// DACL failures are logged as warnings but do not fail the write.
func SecureWriteFile(path string, data []byte, perm os.FileMode) error {
	if err := os.WriteFile(path, data, perm); err != nil {
		return err
	}
	if isOwnerOnly(perm) {
		if err := restrictToCurrentUser(path); err != nil {
			slog.Warn("fileutil: best-effort DACL failed", "path", path, "err", err)
		}
	}
	return nil
}

// SecureMkdirAll creates a directory path and all parents that do not yet exist.
// For owner-only modes, a DACL restricting access to the current user is applied
// to the leaf directory and every intermediate directory that was created.
func SecureMkdirAll(path string, perm os.FileMode) error {
	// Determine which directories already exist before creating.
	var toSecure []string
	if isOwnerOnly(perm) {
		p := filepath.Clean(path)
		for p != "" && p != "." && p != string(filepath.Separator) {
			if _, err := os.Stat(p); err == nil {
				break // already exists, stop climbing
			}
			toSecure = append(toSecure, p)
			parent := filepath.Dir(p)
			if parent == p {
				break
			}
			p = parent
		}
	}

	if err := os.MkdirAll(path, perm); err != nil {
		return err
	}

	// Secure all newly created directories (leaf-first order, but order doesn't matter).
	for _, dir := range toSecure {
		if err := restrictToCurrentUser(dir); err != nil {
			slog.Warn("fileutil: best-effort DACL failed", "path", dir, "err", err)
		}
	}
	return nil
}

// SecureChmod changes the mode of the named file.
// For owner-only modes, a DACL restricting access to the current user is applied.
// DACL failures are logged as warnings but do not fail the chmod.
func SecureChmod(path string, perm os.FileMode) error {
	if err := os.Chmod(path, perm); err != nil {
		return err
	}
	if isOwnerOnly(perm) {
		if err := restrictToCurrentUser(path); err != nil {
			slog.Warn("fileutil: best-effort DACL failed", "path", path, "err", err)
		}
	}
	return nil
}

// SecureOpenFile opens the named file with specified flag and permissions.
// For owner-only modes when O_CREATE is set, a DACL restricting access to
// the current user is applied — regardless of whether the file already existed.
// This is intentional: all callers write sensitive data (email content,
// attachments) that should be owner-only.
//
// Note: on Windows there is a small TOCTOU window between file creation and
// DACL application because SetNamedSecurityInfo operates by path after the
// file is already open. The window is very brief and exploitation would
// require local access, so this is acceptable for the threat model.
// DACL failures are logged as warnings but do not fail the open.
func SecureOpenFile(path string, flag int, perm os.FileMode) (*os.File, error) {
	f, err := os.OpenFile(path, flag, perm)
	if err != nil {
		return nil, err
	}
	if isOwnerOnly(perm) && (flag&os.O_CREATE != 0) {
		if err := restrictToCurrentUser(path); err != nil {
			slog.Warn("fileutil: best-effort DACL failed", "path", path, "err", err)
		}
	}
	return f, nil
}
