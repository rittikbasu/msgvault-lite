package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/config"
)

func directWriteTestConfig(dataDir string) *config.Config {
	return &config.Config{
		HomeDir: dataDir,
		Data:    config.DataConfig{DataDir: dataDir},
	}
}

func withStoreResolverConfig(t *testing.T, c *config.Config) {
	t.Helper()
	oldCfg := cfg
	cfg = c
	t.Cleanup(func() {
		cfg = oldCfg
	})
}

func TestAcquireDirectSQLiteWriteLockHoldsThenReleases(t *testing.T) {
	dataDir := t.TempDir()
	cfg := directWriteTestConfig(dataDir)

	release, err := acquireDirectSQLiteWriteLock(cfg)
	require.NoError(t, err, "acquire on a free archive")
	require.NotNil(t, release, "release func")

	blocked, err := tryAcquireWriteOwnerLock(dataDir)
	assert.Nil(t, blocked, "second owner")
	require.ErrorAs(t, err, &writeOwnerLockHeldError{}, "second acquisition blocked")

	release()

	reacquired, err := tryAcquireWriteOwnerLock(dataDir)
	require.NoError(t, err, "reacquire after release")
	require.NoError(t, reacquired.Close(), "close reacquired lock")
}

func TestOpenWritableStoreAndInitOwnsArchiveUntilCleanup(t *testing.T) {
	dataDir := t.TempDir()
	withStoreResolverConfig(t, directWriteTestConfig(dataDir))

	st, cleanup, err := openWritableStoreAndInit()
	require.NoError(t, err, "open writable store")
	require.NotNil(t, st, "store")
	require.NotNil(t, cleanup, "cleanup")
	cleaned := false
	t.Cleanup(func() {
		if !cleaned {
			cleanup()
		}
	})

	blocked, err := tryAcquireWriteOwnerLock(dataDir)
	assert.Nil(t, blocked, "second owner while store is open")
	require.ErrorAs(t, err, &writeOwnerLockHeldError{}, "store helper holds write-owner lock")

	cleanup()
	cleaned = true

	reacquired, err := tryAcquireWriteOwnerLock(dataDir)
	require.NoError(t, err, "cleanup releases write-owner lock")
	require.NoError(t, reacquired.Close(), "close reacquired lock")
}

func TestAcquireDirectSQLiteWriteLockActionableErrorWhenOwned(t *testing.T) {
	dataDir := t.TempDir()
	cfg := directWriteTestConfig(dataDir)

	owner, err := tryAcquireWriteOwnerLock(dataDir)
	require.NoError(t, err, "pre-acquire owner")
	t.Cleanup(func() { _ = owner.Close() })

	release, err := acquireDirectSQLiteWriteLock(cfg)
	assert.Nil(t, release, "no release when blocked")
	require.Error(t, err, "acquire on an owned archive")
	assert.Contains(t, err.Error(), "owned", "names the ownership condition")
	assert.Contains(t, err.Error(), "wait", "points at the remedy")
}
