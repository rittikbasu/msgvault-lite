package store

const (
	liveMessagesUnaliased = "deleted_from_source_at IS NULL"
	liveMessagesM         = "m.deleted_from_source_at IS NULL"
)

// LiveMessagesWhere returns the SQL predicate that selects live
// messages. Source-deleted rows are filtered only when
// hideDeletedFromSource is true; archive views may intentionally show them.
func LiveMessagesWhere(alias string, hideDeletedFromSource bool) string {
	if !hideDeletedFromSource {
		return "1 = 1"
	}
	if alias == "" {
		return liveMessagesUnaliased
	}
	if alias == "m" {
		return liveMessagesM
	}
	return alias + ".deleted_from_source_at IS NULL"
}

// SourceDeletedMessagesWhere returns the SQL predicate that selects messages
// retained in the archive but deleted from their source account.
func SourceDeletedMessagesWhere(alias string) string {
	if alias == "" {
		return "deleted_from_source_at IS NOT NULL"
	}
	return alias + ".deleted_from_source_at IS NOT NULL"
}
