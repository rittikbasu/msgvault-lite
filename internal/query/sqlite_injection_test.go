package query

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSQLInjection_InvalidViewType tests that invalid ViewType values are rejected.
// This ensures that even if a malicious or buggy caller passes an out-of-range
// ViewType, it won't result in undefined behavior or SQL injection.
func TestSQLInjection_InvalidViewType(t *testing.T) {
	env := newTestEnv(t)

	// Attempt to aggregate with an invalid ViewType value
	// This simulates a potential attack vector if enum validation is missing
	invalidViewType := ViewType(999)

	_, err := env.Engine.Aggregate(env.Ctx, invalidViewType, DefaultAggregateOptions())
	assert.ErrorContains(t, err, "unsupported view type")
}

// TestSQLInjection_InvalidSortField tests that invalid SortField values are handled safely.
// The sortClause function should validate the SortField is within expected range.
func TestSQLInjection_InvalidSortField(t *testing.T) {
	env := newTestEnv(t)

	// Create options with an invalid SortField value
	opts := DefaultAggregateOptions()
	opts.SortField = SortField(999) // Invalid sort field

	// The current implementation falls through to default, which is unsafe
	// because it allows arbitrary sort field values without explicit validation.
	// After the fix, this should return an error.
	_, err := env.Engine.Aggregate(env.Ctx, ViewSenders, opts)
	require.ErrorContains(t, err, "unsupported sort field")
}

// TestSQLInjection_InvalidMessageSortField tests that invalid MessageSortField values
// are handled safely in ListMessages.
func TestSQLInjection_InvalidMessageSortField(t *testing.T) {
	env := newTestEnv(t)

	// Create filter with an invalid MessageSortField value
	filter := MessageFilter{
		Sorting: MessageSorting{
			Field:     MessageSortField(999), // Invalid sort field
			Direction: SortAsc,
		},
	}

	// The current implementation falls through to default, which is unsafe.
	// After the fix, this should return an error.
	_, err := env.Engine.ListMessages(env.Ctx, filter)
	require.ErrorContains(t, err, "unsupported message sort field")
}

// TestSQLInjection_InvalidTimeGranularity tests that invalid TimeGranularity values
// are handled safely when used in queries.
func TestSQLInjection_InvalidTimeGranularity(t *testing.T) {
	env := newTestEnv(t)

	opts := DefaultAggregateOptions()
	opts.TimeGranularity = TimeGranularity(999) // Invalid granularity

	// When aggregating by time with an invalid granularity, should return error
	_, err := env.Engine.Aggregate(env.Ctx, ViewTime, opts)
	require.ErrorContains(t, err, "unsupported time granularity")
}

// TestSQLInjection_FilterStringsAreSafelyParameterized verifies that filter
// string fields like Sender, Label, etc. are properly parameterized and
// cannot be used for SQL injection.
func TestSQLInjection_FilterStringsAreSafelyParameterized(t *testing.T) {
	env := newTestEnv(t)

	// These SQL injection payloads should be treated as literal strings,
	// not executed as SQL.
	injectionPayloads := []string{
		"'; DROP TABLE messages; --",
		"alice@example.com' OR '1'='1",
		"alice@example.com\" OR \"1\"=\"1",
		"alice@example.com; DELETE FROM messages WHERE '1'='1",
		"alice@example.com UNION SELECT * FROM messages--",
	}

	for _, payload := range injectionPayloads {
		t.Run("Sender_"+payload[:min(20, len(payload))], func(t *testing.T) {
			filter := MessageFilter{Sender: payload}
			// Should not panic or cause SQL error - just return empty results
			msgs, err := env.Engine.ListMessages(env.Ctx, filter)
			require.NoError(t, err, "unexpected error with payload %q", payload)
			// Should return 0 results (no match), not all messages
			assert.Empty(t, msgs, "expected 0 results for SQL injection payload")
		})

		t.Run("Label_"+payload[:min(20, len(payload))], func(t *testing.T) {
			filter := MessageFilter{Label: payload}
			msgs, err := env.Engine.ListMessages(env.Ctx, filter)
			require.NoError(t, err, "unexpected error with payload %q", payload)
			assert.Empty(t, msgs, "expected 0 results for SQL injection payload")
		})
	}

	// Verify the database is still intact after all injection attempts
	var count int
	err := env.DB.QueryRow("SELECT COUNT(*) FROM messages").Scan(&count)
	require.NoError(t, err, "failed to count messages after injection tests")
	assert.Equal(t, 5, count, "expected 5 messages in database after injection tests (standard seed data)")
}
