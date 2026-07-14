package store

import (
	"database/sql"
	"errors"
	"fmt"
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
		       last_sync_at, sync_cursor, oauth_app,
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
// shared across source rows.
func (s *Store) GetSourcesByIdentifier(
	identifier string,
) ([]*Source, error) {
	rows, err := s.db.Query(`
		SELECT id, source_type, identifier, display_name,
		       google_user_id, last_sync_at, sync_cursor,
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
		       google_user_id, last_sync_at, sync_cursor,
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
// Note: display_name is not constrained to be unique — callers receive all
// matching rows if more than one source shares the same name.
func (s *Store) GetSourcesByDisplayName(displayName string) ([]*Source, error) {
	rows, err := s.db.Query(`
		SELECT id, source_type, identifier, display_name,
		       google_user_id, last_sync_at, sync_cursor,
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
