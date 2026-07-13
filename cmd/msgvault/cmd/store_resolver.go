package cmd

import (
	"fmt"
	"os"

	"go.kenn.io/msgvault/internal/store"
)

// runStartupMigrations pulls legacy identity addresses from the global config
// and runs the one-time migration. If migration was performed, the notice is
// logged and printed to stderr. If the migration is deferred because no source
// exists yet, it will be retried after a source is created.
func runStartupMigrations(s *store.Store) error {
	addrs := cfg.Identity.Addresses
	res, err := s.RunStartupMigrations(addrs)
	if err != nil {
		logger.Warn("startup migration failed", "error", err)
		return err
	}
	switch {
	case res.Deferred:
		logger.Info("legacy [identity] block in config detected (migration deferred until a source exists)",
			"address_count", res.AddressCount,
			"hint", "run 'msgvault add-account ...' to create a source; the migration will retry on the next command")
	case res.Applied:
		logger.Info("legacy identity migrated",
			"addresses", res.AddressCount,
			"sources", res.SourceCount)
	}
	if res.Notice != "" {
		_, _ = fmt.Fprintln(os.Stderr, res.Notice)
	}
	return nil
}

// runStartupMigrationsForIngest remains a pre-source-create no-op so identity
// migration cannot race confirmation of the first source's own address.
func runStartupMigrationsForIngest(s *store.Store) error {
	_ = s
	return nil
}

func runPostSourceCreateMigrations(s *store.Store) error {
	return runStartupMigrations(s)
}
