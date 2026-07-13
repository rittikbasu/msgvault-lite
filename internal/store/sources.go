package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrSourceNotFound is returned by GetSourceByID and GetSourceByIdentifier
// when no source row matches. Wrapped via fmt.Errorf("...: %w", ...) so
// callers can use errors.Is to distinguish absence from real DB
// errors.
var ErrSourceNotFound = errors.New("source not found")

// GetSourceByID returns the source with the given ID, or
// ErrSourceNotFound (wrapped) if no row matches.
func (s *Store) GetSourceByID(id int64) (*Source, error) {
	row := s.db.QueryRow(`
		SELECT id, source_type, identifier, display_name, google_user_id,
		       last_sync_at, sync_cursor, sync_config, oauth_app,
		       created_at, updated_at
		FROM sources
		WHERE id = ?
	`, id)

	source, err := scanSource(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("source %d: %w", id, ErrSourceNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get source by id: %w", err)
	}
	return source, nil
}

// GetSourcesByIdentifier returns all sources matching an identifier,
// regardless of source_type. Use this when the identifier may be
// shared across source types (e.g., gmail + mbox import).
func (s *Store) GetSourcesByIdentifier(
	identifier string,
) ([]*Source, error) {
	rows, err := s.db.Query(`
		SELECT id, source_type, identifier, display_name,
		       google_user_id, last_sync_at, sync_cursor, sync_config,
		       oauth_app, created_at, updated_at
		FROM sources
		WHERE identifier = ?
		ORDER BY source_type
	`, identifier)
	if err != nil {
		return nil, fmt.Errorf("query sources: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var sources []*Source
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, fmt.Errorf("scan source: %w", err)
		}
		sources = append(sources, src)
	}
	return sources, rows.Err()
}

// GetSourcesByIdentifierOrDisplayName returns all sources whose identifier or
// display_name matches the given value. This is the preferred single-query
// lookup when resolving a user-supplied email or identifier string.
func (s *Store) GetSourcesByIdentifierOrDisplayName(query string) ([]*Source, error) {
	rows, err := s.db.Query(`
		SELECT id, source_type, identifier, display_name,
		       google_user_id, last_sync_at, sync_cursor, sync_config,
		       oauth_app, created_at, updated_at
		FROM sources
		WHERE identifier = ? OR display_name = ?
		ORDER BY source_type
	`, query, query)
	if err != nil {
		return nil, fmt.Errorf("query sources: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var sources []*Source
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, fmt.Errorf("scan source: %w", err)
		}
		sources = append(sources, src)
	}
	return sources, rows.Err()
}

// GetSourcesByDisplayName returns all sources with the given display name.
// Use this as a fallback when looking up IMAP sources by their human-readable
// email address rather than the full imaps:// identifier.
// Note: display_name is not constrained to be unique — callers receive all
// matching rows if more than one source shares the same name.
func (s *Store) GetSourcesByDisplayName(displayName string) ([]*Source, error) {
	rows, err := s.db.Query(`
		SELECT id, source_type, identifier, display_name,
		       google_user_id, last_sync_at, sync_cursor, sync_config,
		       oauth_app, created_at, updated_at
		FROM sources
		WHERE display_name = ?
		ORDER BY source_type
	`, displayName)
	if err != nil {
		return nil, fmt.Errorf("query sources by display name: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var sources []*Source
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, fmt.Errorf("scan source: %w", err)
		}
		sources = append(sources, src)
	}
	return sources, rows.Err()
}

// GetSourcesByTypeAndAccount returns every source of the given source_type
// whose sync_config JSON carries the given account_email.
//
// Config-driven sources (calendar, and any future per-account fan-out) decouple
// their per-source identifier — a natural key like a calendarId — from the
// OAuth account/token key, which lives in sync_config.account_email. A single
// account may own many sources (e.g. several calendars), all sharing one token
// file. Filtering happens in Go after a typed list query so it stays
// dialect-portable (no SQLite json_extract vs PG ->> divergence); the set of one
// account's sources is small, so this is not a hot path. A source whose
// sync_config is NULL or unparseable is skipped rather than aborting the scan.
func (s *Store) GetSourcesByTypeAndAccount(sourceType, accountEmail string) ([]*Source, error) {
	all, err := s.ListSources(sourceType)
	if err != nil {
		return nil, fmt.Errorf("list sources by type %q: %w", sourceType, err)
	}
	accountEmail = strings.TrimSpace(accountEmail)
	var matched []*Source
	for _, src := range all {
		if !src.SyncConfig.Valid {
			continue
		}
		var cfg struct {
			AccountEmail string `json:"account_email"`
		}
		if err := json.Unmarshal([]byte(src.SyncConfig.String), &cfg); err != nil {
			continue
		}
		if accountEmail != "" && strings.EqualFold(strings.TrimSpace(cfg.AccountEmail), accountEmail) {
			matched = append(matched, src)
		}
	}
	return matched, nil
}
