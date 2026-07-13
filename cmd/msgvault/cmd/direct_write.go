package cmd

import (
	"errors"
	"fmt"

	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/store"
)

// acquireDirectSQLiteWriteLock claims the cross-process write-owner lock on
// behalf of a direct (non-daemon) CLI writer using the local SQLite archive.
// PostgreSQL deployments do not use this local filesystem lock. On success it
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
	if store.IsPostgresURL(cfg.DatabaseDSN()) {
		return func() {}, nil
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

// openStoreAndInitWith opens the local archive and initializes schema while the
// caller owns the direct-writer lock. store.Open + InitSchema create the
// database file on first use, which is the right behavior for a
// freshly-installed CLI; init-db remains the explicit setup command for users
// who want to pre-create the DB.
func openStoreAndInitWith(migrate func(*store.Store) error) (*store.Store, error) {
	dbPath := cfg.DatabaseDSN()
	st, err := store.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := st.InitSchema(); err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	if err := migrate(st); err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("startup migrations: %w", err)
	}
	return st, nil
}

func openWritableStoreAndInit() (*store.Store, func(), error) {
	return openWritableStoreAndInitWith(runStartupMigrations)
}

func openWritableStoreAndInitForIngest() (*store.Store, func(), error) {
	return openWritableStoreAndInitWith(runStartupMigrationsForIngest)
}

func openWritableStoreAndInitWith(migrate func(*store.Store) error) (*store.Store, func(), error) {
	release, err := acquireDirectSQLiteWriteLock(cfg)
	if err != nil {
		return nil, nil, err
	}

	st, err := openStoreAndInitWith(migrate)
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
