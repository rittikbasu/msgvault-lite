package store_test

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

// attachmentCorpus seeds a multi-source corpus of messages with attachments
// to exercise content-hash dedup, cross-source dedup, and ON DELETE CASCADE
// against the live SQLite store.
type attachmentCorpus struct {
	t       *testing.T
	store   *store.Store
	srcA    *store.Source
	srcB    *store.Source
	convA   int64
	convB   int64
	msgRows map[string]int64 // gmail id → message row id
}

func newAttachmentCorpus(t *testing.T) *attachmentCorpus {
	t.Helper()
	st := testutil.NewTestStore(t)

	srcA, err := st.GetOrCreateSource("gmail", "alice@example.com")
	require.NoError(t, err, "GetOrCreateSource A")
	srcB, err := st.GetOrCreateSource("gmail", "bob@example.com")
	require.NoError(t, err, "GetOrCreateSource B")
	convA, err := st.EnsureConversation(srcA.ID, "thread-A", "Thread A")
	require.NoError(t, err, "EnsureConversation A")
	convB, err := st.EnsureConversation(srcB.ID, "thread-B", "Thread B")
	require.NoError(t, err, "EnsureConversation B")

	return &attachmentCorpus{
		t:       t,
		store:   st,
		srcA:    srcA,
		srcB:    srcB,
		convA:   convA,
		convB:   convB,
		msgRows: make(map[string]int64),
	}
}

func (c *attachmentCorpus) addMessage(gmailID string, sourceID, convID int64) int64 {
	c.t.Helper()
	id, err := c.store.UpsertMessage(&store.Message{
		ConversationID:  convID,
		SourceID:        sourceID,
		SourceMessageID: gmailID,
		SizeEstimate:    100,
	})
	require.NoErrorf(c.t, err, "UpsertMessage(%s)", gmailID)
	c.msgRows[gmailID] = id
	return id
}

func (c *attachmentCorpus) addAttachment(gmailID, filename, hash string) {
	c.t.Helper()
	msgID, ok := c.msgRows[gmailID]
	require.Truef(c.t, ok, "addAttachment: unknown gmail id %q", gmailID)
	storagePath := hash[:2] + "/" + hash
	err := c.store.UpsertAttachment(msgID, filename, "application/pdf",
		storagePath, hash, 100)
	require.NoErrorf(c.t, err, "UpsertAttachment(%s, %s)", gmailID, filename)
}

func (c *attachmentCorpus) attachmentRowCount() int {
	c.t.Helper()
	var n int
	err := c.store.DB().QueryRow(`SELECT COUNT(*) FROM attachments`).Scan(&n)
	require.NoError(c.t, err, "attachmentRowCount")
	return n
}

// attachmentRowsForHash counts attachment rows carrying the given content
// hash. The hash argument is always hashShared in the current suite but
// kept explicit so each call site reads as a content-hash assertion.
//
//nolint:unparam // hash intentionally parameterized; see doc comment.
func (c *attachmentCorpus) attachmentRowsForHash(hash string) int {
	c.t.Helper()
	var n int
	err := c.store.DB().QueryRow(
		c.store.Rebind(`SELECT COUNT(*) FROM attachments WHERE content_hash = ?`),
		hash,
	).Scan(&n)
	require.NoErrorf(c.t, err, "attachmentRowsForHash(%s)", hash)
	return n
}

// hashes used throughout the suite. Real values are 64-char hex; the values
// here are fixed sentinels that round-trip cleanly through the DB and the
// content_hash column has no parsing constraints inside the store layer.
const (
	hashShared = "h1sharedhash0000000000000000000000000000000000000000000000000abc"
	hashUniqA  = "h2uniqueA00000000000000000000000000000000000000000000000000000de"
	hashUniqB  = "h3uniqueB000000000000000000000000000000000000000000000000000ab12"
)

func TestGetMessageIncludesAttachmentWithNullableMetadata(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	src, err := st.GetOrCreateSource("gmail", "alice@example.com")
	require.NoError(err, "GetOrCreateSource")
	convID, err := st.EnsureConversation(src.ID, "thread-nullhash", "Thread Null Hash")
	require.NoError(err, "EnsureConversation")
	msgID, err := st.UpsertMessage(&store.Message{
		ConversationID:  convID,
		SourceID:        src.ID,
		SourceMessageID: "nullhash-msg",
		Subject:         sql.NullString{String: "Attachment", Valid: true},
		SizeEstimate:    100,
	})
	require.NoError(err, "UpsertMessage")

	var attachmentID int64
	err = st.DB().QueryRow(
		st.Rebind(`INSERT INTO attachments (message_id, filename, mime_type, storage_path, content_hash, size, created_at)
			VALUES (?, ?, ?, ?, NULL, ?, CURRENT_TIMESTAMP)
			RETURNING id`),
		msgID, nil, nil, "legacy/path.bin", nil,
	).Scan(&attachmentID)
	require.NoError(err, "insert nullable attachment metadata")

	got, err := st.GetMessage(msgID)
	require.NoError(err, "GetMessage")
	require.Len(got.Attachments, 1, "attachments")
	assert.Equal(attachmentID, got.Attachments[0].ID, "id")
	assert.Empty(got.Attachments[0].Filename, "filename")
	assert.Empty(got.Attachments[0].MimeType, "mime_type")
	assert.Zero(got.Attachments[0].Size, "size")
	assert.Empty(got.Attachments[0].ContentHash, "content_hash")
}

// TestAttachment_E2E_MultiMessageDedup verifies that multiple messages within
// a single source can reference the same content_hash via UpsertAttachment
// and that the helper is idempotent (re-upserting the same (message_id,
// content_hash) pair is a no-op).
func TestAttachment_E2E_MultiMessageDedup(t *testing.T) {
	assert := assert.New(t)
	c := newAttachmentCorpus(t)

	// Three messages in source A referencing the same content hash.
	c.addMessage("msg-1", c.srcA.ID, c.convA)
	c.addMessage("msg-2", c.srcA.ID, c.convA)
	c.addMessage("msg-3", c.srcA.ID, c.convA)
	c.addAttachment("msg-1", "shared.pdf", hashShared)
	c.addAttachment("msg-2", "shared.pdf", hashShared)
	c.addAttachment("msg-3", "shared.pdf", hashShared)

	// One row per message, all referencing the same hash.
	assert.Equal(3, c.attachmentRowsForHash(hashShared), "rows for hashShared")

	// Idempotent re-upsert: existing (message_id, content_hash) is a no-op.
	c.addAttachment("msg-2", "shared.pdf", hashShared)
	assert.Equal(3, c.attachmentRowsForHash(hashShared), "rows for hashShared after re-upsert")

	// IsAttachmentPathReferenced reports the hash storage path as referenced.
	referenced, err := c.store.IsAttachmentPathReferenced(hashShared[:2] + "/" + hashShared)
	require.NoError(t, err, "IsAttachmentPathReferenced")
	assert.True(referenced, "expected referenced=true while messages still hold the hash")
}

// TestAttachment_E2E_TombstoneRetainsArchivedAttachments verifies that source
// deletion tombstones retain both message and attachment archive rows.
func TestAttachment_E2E_TombstoneRetainsArchivedAttachments(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	c := newAttachmentCorpus(t)

	// Two messages in source A referencing the shared hash plus one with a
	// unique hash.
	c.addMessage("msg-1", c.srcA.ID, c.convA)
	c.addMessage("msg-2", c.srcA.ID, c.convA)
	c.addMessage("msg-3", c.srcA.ID, c.convA)
	c.addAttachment("msg-1", "shared.pdf", hashShared)
	c.addAttachment("msg-2", "shared.pdf", hashShared)
	c.addAttachment("msg-3", "unique.pdf", hashUniqA)

	assert.Equal(3, c.attachmentRowCount(), "initial attachment count")

	// A permanent source deletion tombstones msg-1 without deleting its archive.
	err := c.store.MarkMessageDeletedByGmailID(true, "msg-1")
	require.NoError(err, "MarkMessageDeletedByGmailID(permanent, msg-1)")

	assert.Equal(2, c.attachmentRowsForHash(hashShared), "archived attachment rows after delete")

	// The shared storage path is still referenced (msg-2 holds it).
	referenced, err := c.store.IsAttachmentPathReferenced(hashShared[:2] + "/" + hashShared)
	require.NoError(err, "IsAttachmentPathReferenced")
	assert.True(referenced, "shared path should remain referenced via msg-2 after msg-1 delete")

	// Tombstoning every source copy still retains both archived references.
	err = c.store.MarkMessageDeletedByGmailID(true, "msg-2")
	require.NoError(err, "MarkMessageDeletedByGmailID(permanent, msg-2)")
	assert.Equal(2, c.attachmentRowsForHash(hashShared), "archived rows after both deleted")

	referenced, err = c.store.IsAttachmentPathReferenced(hashShared[:2] + "/" + hashShared)
	require.NoError(err, "IsAttachmentPathReferenced after both deleted")
	assert.True(referenced, "shared path should remain referenced by tombstoned archive rows")
}

// NULL content_hash or empty storage_path are excluded from
// AttachmentPathsUniqueToSource (mirroring the existing focused test but in
// a multi-message context).
func TestAttachment_E2E_NullAndEmptyHashesIgnored(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	c := newAttachmentCorpus(t)

	c.addMessage("msg-a1", c.srcA.ID, c.convA)
	c.addMessage("msg-a2", c.srcA.ID, c.convA)
	c.addMessage("msg-a3", c.srcA.ID, c.convA)

	// Normal attachment with a unique content hash.
	c.addAttachment("msg-a1", "good.pdf", hashUniqA)

	// Attachment with NULL content_hash — must NOT appear in unique set.
	_, err := c.store.DB().Exec(c.store.Rebind(fmt.Sprintf(
		`INSERT INTO attachments (message_id, filename, mime_type, storage_path, content_hash, size, created_at)
		 VALUES (?, 'null-hash.pdf', 'application/pdf', 'nn/nullpath', NULL, 0, %s)`,
		"CURRENT_TIMESTAMP",
	)), c.msgRows["msg-a2"])
	require.NoError(err, "insert null-hash attachment")

	// Attachment with empty storage_path — also excluded.
	err = c.store.UpsertAttachment(c.msgRows["msg-a3"], "empty.pdf",
		"application/pdf", "", "emptypathhash", 0)
	require.NoError(err, "UpsertAttachment(empty)")

	paths, err := c.store.AttachmentPathsUniqueToSource(c.srcA.ID)
	require.NoError(err, "AttachmentPathsUniqueToSource")
	want := hashUniqA[:2] + "/" + hashUniqA
	if assert.Len(paths, 1, "paths want 1 only") {
		assert.Equal(want, paths[0], "paths[0]")
	}
}
