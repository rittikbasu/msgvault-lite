package cmd

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/config"
)

func TestResolveBackupRepoPrecedence(t *testing.T) {
	withStoreResolverConfig(t, &config.Config{
		Backup: config.BackupConfig{Repo: "/configured/repo"},
	})

	repo, err := resolveBackupRepo("/flag/repo")
	require.NoError(t, err)
	assert.Equal(t, "/flag/repo", repo)

	repo, err = resolveBackupRepo("")
	require.NoError(t, err)
	assert.Equal(t, "/configured/repo", repo)
}

func TestResolveBackupRepoRequiresConfiguration(t *testing.T) {
	withStoreResolverConfig(t, &config.Config{})

	repo, err := resolveBackupRepo("")
	assert.Empty(t, repo)
	require.EqualError(t, err, "backup: no repository configured; pass --repo or set [backup] repo in config.toml")
}

func TestDirectBackupFreezerOwnsArchiveUntilEnd(t *testing.T) {
	dataDir := t.TempDir()
	withStoreResolverConfig(t, directWriteTestConfig(dataDir))

	freezer, closeFreezer, err := newBackupFreezer()
	require.NoError(t, err)
	t.Cleanup(closeFreezer)

	require.NoError(t, freezer.Begin(context.Background()))

	blocked, err := tryAcquireWriteOwnerLock(dataDir)
	assert.Nil(t, blocked)
	require.ErrorAs(t, err, &writeOwnerLockHeldError{})

	require.NoError(t, freezer.End(context.Background()))

	reacquired, err := tryAcquireWriteOwnerLock(dataDir)
	require.NoError(t, err)
	require.NoError(t, reacquired.Close())
}

func TestDirectBackupFreezerRejectsInvalidTransitions(t *testing.T) {
	withStoreResolverConfig(t, directWriteTestConfig(t.TempDir()))
	freezer, closeFreezer, err := newBackupFreezer()
	require.NoError(t, err)
	t.Cleanup(closeFreezer)

	require.Error(t, freezer.End(context.Background()))
	require.NoError(t, freezer.Begin(context.Background()))
	require.Error(t, freezer.Begin(context.Background()))
	require.NoError(t, freezer.End(context.Background()))
}

func TestRestoreIntoLiveArchiveUsesWriterLock(t *testing.T) {
	dataDir := t.TempDir()
	withStoreResolverConfig(t, directWriteTestConfig(dataDir))

	owner, err := tryAcquireWriteOwnerLock(dataDir)
	require.NoError(t, err)

	release, err := acquireRestoreTargetLock(dataDir)
	assert.Nil(t, release)
	require.EqualError(t, err, "the msgvault archive is owned by another msgvault process; wait for that operation to finish, then retry")
	require.NoError(t, owner.Close())

	release, err = acquireRestoreTargetLock(dataDir)
	require.NoError(t, err)
	release()
}

func TestRestoreOutsideLiveArchiveNeedsNoWriterLock(t *testing.T) {
	dataDir := t.TempDir()
	withStoreResolverConfig(t, directWriteTestConfig(dataDir))

	owner, err := tryAcquireWriteOwnerLock(dataDir)
	require.NoError(t, err)
	defer func() { require.NoError(t, owner.Close()) }()

	release, err := acquireRestoreTargetLock(t.TempDir())
	require.NoError(t, err)
	release()
}
