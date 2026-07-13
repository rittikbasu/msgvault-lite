//go:build unix

package fileutil

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestReadPrivateFileRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private.fifo")
	require.NoError(t, unix.Mkfifo(path, 0o600))

	done := make(chan error, 1)
	go func() {
		_, err := ReadPrivateFile(path)
		done <- err
	}()

	select {
	case err := <-done:
		require.Error(t, err)
		assert.ErrorContains(t, err, "not a regular file")
	case <-time.After(250 * time.Millisecond):
		assert.Fail(t, "ReadPrivateFile blocked while opening a FIFO")
	}
}
