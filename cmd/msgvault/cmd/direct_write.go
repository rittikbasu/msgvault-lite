package cmd

import (
	"errors"
	"fmt"

	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/store"
)

// acquireDirectSQLiteWriteLock claims the cross-process write-owner lock on
// behalf of a direct CLI writer using the local SQLite archive. On success it
// returns a release func that the caller must defer. When the SQLite archive is
// already owned it returns an actionable error instead of silently contending on
// the database file.
//
// The lock is taken non-blocking, so there is no context parameter: a writer
// either claims the free SQLite archive immediately or is told who holds it.
func acquireDirectSQLiteWriteLock(cfg *config.Config) (func(), error) {
	if cfg == nil {
		return nil, errors.New("nil config")
	}
	lock, err := tryAcquireWriteOwnerLock(cfg.Data.DataDir)
	if err != nil {
		if errors.As(err, &writeOwnerLockHeldError{}) {
			return nil, archiveOwnedError(cfg.Data.DataDir)
		}
		return nil, err
	}
	return func() {
		if cerr := lock.Close(); cerr != nil {
			logger.Warn("release write-owner lock", "error", cerr)
		}
	}, nil
}

// archiveOwnedError explains that the SQLite archive is owned by another
// msgvault process and how to proceed. When a local daemon is discoverable the
// message names it so the remedy ("msgvault serve stop") is concrete.
func archiveOwnedError(dataDir string) error {
	_ = dataDir
	return errors.New(
		"the msgvault archive is owned by another msgvault process; " +
			"wait for that operation to finish, then retry",
	)
}

// openStoreAndInit opens the local archive and initializes schema while the
// caller owns the direct-writer lock. store.Open + InitSchema create the
// database file on the first write command, which is the right behavior for a
// freshly installed CLI.
func openStoreAndInit() (*store.Store, error) {
	dbPath := cfg.DatabasePath()
	st, err := store.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := st.InitSchema(); err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return st, nil
}

func openWritableStoreAndInit() (*store.Store, func(), error) {
	return openWritableStoreAndInitLocked()
}

func openWritableStoreAndInitForIngest() (*store.Store, func(), error) {
	return openWritableStoreAndInitLocked()
}

func openWritableStoreAndInitLocked() (*store.Store, func(), error) {
	release, err := acquireDirectSQLiteWriteLock(cfg)
	if err != nil {
		return nil, nil, err
	}

	st, err := openStoreAndInit()
	if err != nil {
		release()
		return nil, nil, err
	}

	cleanup := func() {
		_ = st.Close()
		release()
	}
	return st, cleanup, nil
}
