package query

import (
	"context"
	"time"

	"github.com/rittikbasu/msgvault-lite/internal/search"
)

// Engine is the interface for SQLite query operations.
type Engine interface {
	// Aggregate performs grouping based on the provided ViewType (Sender, Domain, etc.)
	Aggregate(ctx context.Context, groupBy ViewType, opts AggregateOptions) ([]AggregateRow, error)

	// SubAggregate performs aggregation on a filtered subset of messages.
	// This is used for sub-grouping after drill-down, e.g., drilling into
	// "Sender: foo@example.com" and then sub-grouping by Recipients or Labels.
	// The filter specifies the parent context (sender, domain, etc.) and
	// groupBy specifies what dimension to aggregate by.
	SubAggregate(ctx context.Context, filter MessageFilter, groupBy ViewType, opts AggregateOptions) ([]AggregateRow, error)

	// Message queries
	ListMessages(ctx context.Context, filter MessageFilter) ([]MessageSummary, error)
	GetMessage(ctx context.Context, id int64) (*MessageDetail, error)
	GetMessageBySourceID(ctx context.Context, sourceMessageID string) (*MessageDetail, error)
	GetAttachment(ctx context.Context, id int64) (*AttachmentInfo, error)

	// GetMessageRaw returns the decompressed raw MIME data for a message.
	// Returns nil, nil if no raw data is stored for the given ID.
	GetMessageRaw(ctx context.Context, id int64) ([]byte, error)

	// Search - full-text search using FTS5 (includes message body)
	Search(ctx context.Context, query *search.Query, limit, offset int) ([]MessageSummary, error)

	// SearchFast searches message metadata only (no body text).
	// Searches: subject, sender email/name (case-insensitive).
	// The filter parameter allows contextual search within a drill-down.
	SearchFast(ctx context.Context, query *search.Query, filter MessageFilter, limit, offset int) ([]MessageSummary, error)

	// SearchFastCount returns the total count of messages matching a search query.
	// This is used for pagination UI to show "N of M results".
	SearchFastCount(ctx context.Context, query *search.Query, filter MessageFilter) (int64, error)

	// SearchFastWithStats performs a metadata search and returns paginated
	// results, total count, and aggregate stats in one response.
	//
	// queryStr is the raw search string (needed for stats; search.Query doesn't store it).
	// statsGroupBy controls which view's key columns are used for stats search filtering.
	SearchFastWithStats(ctx context.Context, query *search.Query, queryStr string,
		filter MessageFilter, statsGroupBy ViewType, limit, offset int) (*SearchFastResult, error)

	// GetGmailIDsByFilter returns Gmail message IDs (source_message_id) matching a filter.
	// This is useful for batch operations like staging messages for deletion.
	GetGmailIDsByFilter(ctx context.Context, filter MessageFilter) ([]string, error)

	// SearchByDomains returns messages where any participant (from, to, cc, or bcc)
	// belongs to one of the given domains.
	SearchByDomains(ctx context.Context, domains []string, after, before *time.Time, limit, offset int) ([]MessageSummary, error)

	// Account queries
	ListAccounts(ctx context.Context) ([]AccountInfo, error)

	// Stats
	GetTotalStats(ctx context.Context, opts StatsOptions) (*TotalStats, error)

	// Close releases any resources held by the engine.
	Close() error
}

// SearchFastResult holds the combined results of a fast search:
// paginated messages, total count, and aggregate stats — all from a single
// materialized scan of the matching message IDs.
type SearchFastResult struct {
	Messages   []MessageSummary
	TotalCount int64
	Stats      *TotalStats
}

// TotalStats provides overall database statistics.
//
// MessageCount is the total count over the filtered population and, unless
// HideDeletedFromSource is set, includes messages deleted from their source
// account (the archive retains them). ActiveMessageCount and
// SourceDeletedMessageCount break MessageCount into its two populations so
// callers can label a total instead of silently picking one semantic.
type TotalStats struct {
	MessageCount              int64
	ActiveMessageCount        int64
	SourceDeletedMessageCount int64
	TotalSize                 int64
	AttachmentCount           int64
	AttachmentSize            int64
	LabelCount                int64
	AccountCount              int64
}
