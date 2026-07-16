//go:build windows

package fileutil

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ReadPrivateFile reads a regular file owned by the current user and protected
// by a current-user-only DACL. It rejects a final-path symbolic link and a path
// swap between inspection and open.
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
	if err := validatePrivateHandle(f, path); err != nil {
		return nil, err
	}

	return io.ReadAll(f)
}

func validatePrivateHandle(f *os.File, path string) error {
	sd, err := windows.GetSecurityInfo(
		windows.Handle(f.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("inspect private file security for %s: %w", path, err)
	}
	if sd == nil {
		return fmt.Errorf("private file has no security descriptor: %s", path)
	}

	currentUser, err := currentUserSID()
	if err != nil {
		return fmt.Errorf("inspect current user for %s: %w", path, err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("inspect private file owner for %s: %w", path, err)
	}
	if owner == nil || !owner.Equals(currentUser) {
		return fmt.Errorf("private file is not owned by the current user: %s", path)
	}

	control, _, err := sd.Control()
	if err != nil {
		return fmt.Errorf("inspect private file DACL control for %s: %w", path, err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("private file DACL inherits access: %s", path)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return fmt.Errorf("private file has no restrictive DACL: %s", path)
	}
	if dacl.AceCount != 1 {
		return fmt.Errorf("private file DACL grants access to multiple principals: %s", path)
	}

	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		return fmt.Errorf("inspect private file DACL entry for %s: %w", path, err)
	}
	if ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
		return fmt.Errorf("private file DACL does not grant current-user access: %s", path)
	}
	aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if !aceSID.Equals(currentUser) {
		return fmt.Errorf("private file DACL grants access to another principal: %s", path)
	}
	return nil
}

// SyncDir is retained for callers that only need a portable best effort.
// Durable replacement on Windows uses ReplaceFile instead.
func SyncDir(_ string) error {
	return nil
}

// ReplaceFile atomically replaces target with temp and asks Windows to flush
// the move to disk before reporting success.
func ReplaceFile(temp, target string) error {
	from, err := windows.UTF16PtrFromString(temp)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(
		from,
		to,
		windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH,
	)
}

func isOwnerOnly(perm os.FileMode) bool {
	return perm&0o077 == 0
}

func currentUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid, nil
}

// restrictToCurrentUser sets a protected DACL that grants GENERIC_ALL only to
// the current user. Directories propagate the restriction to children.
func restrictToCurrentUser(path string) error {
	userSID, err := currentUserSID()
	if err != nil {
		return fmt.Errorf("fileutil: get current user SID for %s: %w", path, err)
	}

	inherit := uint32(windows.NO_INHERITANCE)
	if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
		inherit = windows.CONTAINER_INHERIT_ACE | windows.OBJECT_INHERIT_ACE
	}

	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.SET_ACCESS,
		Inheritance:       inherit,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(userSID),
		},
	}}, nil)
	if err != nil {
		return fmt.Errorf("fileutil: build ACL for %s: %w", path, err)
	}

	secInfo := windows.OWNER_SECURITY_INFORMATION |
		windows.DACL_SECURITY_INFORMATION |
		windows.PROTECTED_DACL_SECURITY_INFORMATION
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.SECURITY_INFORMATION(secInfo),
		userSID,
		nil,
		acl,
		nil,
	); err != nil {
		return fmt.Errorf("fileutil: set DACL on %s: %w", path, err)
	}
	return nil
}

// SecureWriteFile writes data to the named file, creating it if necessary.
// Owner-only DACL failures are fatal.
func SecureWriteFile(path string, data []byte, perm os.FileMode) error {
	if !isOwnerOnly(perm) {
		return os.WriteFile(path, data, perm)
	}

	// Do not truncate or write sensitive bytes until the DACL is in place.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, perm)
	if err != nil {
		return err
	}
	if err := restrictToCurrentUser(path); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Truncate(0); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// SecureMkdirAll creates a directory path and all missing parents.
// Owner-only DACL failures are fatal.
func SecureMkdirAll(path string, perm os.FileMode) error {
	var toSecure []string
	if isOwnerOnly(perm) {
		for p := filepath.Clean(path); p != "" && p != "." && p != string(filepath.Separator); p = filepath.Dir(p) {
			if _, err := os.Stat(p); err == nil {
				break
			}
			toSecure = append(toSecure, p)
			if filepath.Dir(p) == p {
				break
			}
		}
	}

	if err := os.MkdirAll(path, perm); err != nil {
		return err
	}
	for _, dir := range toSecure {
		if err := restrictToCurrentUser(dir); err != nil {
			return err
		}
	}
	return nil
}

// SecureChmod changes the mode and applies a current-user-only DACL for
// owner-only modes. DACL failures are fatal.
func SecureChmod(path string, perm os.FileMode) error {
	if err := os.Chmod(path, perm); err != nil {
		return err
	}
	if isOwnerOnly(perm) {
		return restrictToCurrentUser(path)
	}
	return nil
}

// SecureOpenFile opens the named file and applies a current-user-only DACL
// when creating with an owner-only mode. DACL failures close the file and fail.
func SecureOpenFile(path string, flag int, perm os.FileMode) (*os.File, error) {
	ownerOnlyCreate := isOwnerOnly(perm) && flag&os.O_CREATE != 0
	openFlag := flag
	if ownerOnlyCreate {
		openFlag &^= os.O_TRUNC
	}
	f, err := os.OpenFile(path, openFlag, perm)
	if err != nil {
		return nil, err
	}
	if ownerOnlyCreate {
		if err := restrictToCurrentUser(path); err != nil {
			_ = f.Close()
			return nil, err
		}
		if flag&os.O_TRUNC != 0 {
			if err := f.Truncate(0); err != nil {
				_ = f.Close()
				return nil, err
			}
		}
	}
	return f, nil
}
