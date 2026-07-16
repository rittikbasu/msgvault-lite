package store_test

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/rittikbasu/msgvault-lite/internal/store"
	"github.com/rittikbasu/msgvault-lite/internal/testutil"
)

func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: true}
}

// longPadding is ~3000 tokens of generic non-matching text used to inflate
// the subject-hit document's length without introducing the search term.
var longPadding = strings.Repeat("alpha beta gamma delta epsilon ", 600)

// TestFTSRank_LengthNormalization pins SQLite FTS5's BM25 document-length
// normalization for an adversarial corpus:
//   - subject-hit: subject = "zappa", body padded with ~3000 tokens
//   - body-hit:    subject = "alpha", body = "zappa" (short)
//
// The short body-hit must rank first despite the subject's higher weight.
func TestFTSRank_LengthNormalization(t *testing.T) {
	assertSQLiteBodyHitWins(t)
}

func assertSQLiteBodyHitWins(t *testing.T) {
	t.Helper()
	st := testutil.NewTestStore(t)

	src, err := st.GetOrCreateSource("gmail", "divergence@example.com")
	require.NoError(t, err, "GetOrCreateSource")
	convID, err := st.EnsureConversation(src.ID, "divergence-thread", "Divergence Fixture")
	require.NoError(t, err, "EnsureConversation")

	mk := func(label, subject, body string) int64 {
		id, err := st.UpsertMessage(&store.Message{
			ConversationID:  convID,
			SourceID:        src.ID,
			SourceMessageID: label,
			Subject:         nullString(subject),
			Snippet:         nullString(body),
			SizeEstimate:    int64(len(subject) + len(body)),
		})
		require.NoErrorf(t, err, "UpsertMessage(%q)", label)
		require.NoErrorf(t, st.UpsertFTS(id, subject, body, "noreply@example.com", "", ""), "UpsertFTS(%q)", label)
		return id
	}

	subjectHitID := mk("adv-subject-hit", "zappa", longPadding)
	bodyHitID := mk("adv-body-hit", "alpha", "zappa")

	// Filler docs so avgdl reflects a realistic corpus rather than two
	// extreme outliers.
	for i := range 10 {
		mk(
			"adv-filler-"+string(rune('a'+i)),
			"filler subject "+string(rune('a'+i)),
			"filler body content with several distinct words for normalization",
		)
	}

	results, total, err := st.SearchMessages("zappa", 0, 20)
	require.NoError(t, err, "SearchMessages")
	require.Falsef(t, total < 2 || len(results) < 2,
		"got total=%d len=%d, want >= 2 (both adversarial docs must match)", total, len(results))

	gotFirst, gotSecond := results[0].ID, results[1].ID
	wantFirst, wantSecond := bodyHitID, subjectHitID
	if gotFirst != wantFirst || gotSecond != wantSecond {
		assert.Failf(t,
			"sqlite: documented divergence not reproduced",
			"  got order:  [%d, %d, ...]\n"+
				"  want order: [%d (body-hit), %d (subject-hit), ...]\n"+
				"  subject-hit id=%d, body-hit id=%d\n"+
				"BM25 length normalization should make the short body-hit "+
				"outrank the long subject-hit.",
			gotFirst, gotSecond, wantFirst, wantSecond,
			subjectHitID, bodyHitID,
		)
	}
}
