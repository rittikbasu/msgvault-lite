//go:build windows

package fileutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestReadPrivateFileRejectsBroadDACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private.json")
	require.NoError(t, SecureWriteFile(path, []byte("private"), 0o600))

	currentUser, err := currentUserSID()
	require.NoError(t, err)
	world, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	require.NoError(t, err)

	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(currentUser),
			},
		},
		{
			AccessPermissions: windows.GENERIC_READ,
			AccessMode:        windows.GRANT_ACCESS,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(world),
			},
		},
	}, nil)
	require.NoError(t, err)
	secInfo := windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION
	require.NoError(t, windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.SECURITY_INFORMATION(secInfo),
		nil,
		nil,
		acl,
		nil,
	))

	_, err = ReadPrivateFile(path)
	require.Error(t, err)
	assert.ErrorContains(t, err, "multiple principals")

	// Restore a private DACL so Windows can clean up the temporary directory.
	require.NoError(t, restrictToCurrentUser(path))
	assert.NoError(t, os.Remove(path))
}
