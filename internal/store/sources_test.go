package store_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
	"go.kenn.io/msgvault/internal/testutil/storetest"
)

func TestStore_GetSourcesByIdentifier(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)

	src, err := st.GetOrCreateSource("gmail", "user@example.com")
	require.NoError(err, "create gmail source")

	sources, err := st.GetSourcesByIdentifier("user@example.com")
	require.NoError(err, "GetSourcesByIdentifier")
	require.Len(sources, 1)

	assert.Equal(src.ID, sources[0].ID, "sources[0].ID")
	assert.Equal("gmail", sources[0].SourceType, "sources[0].SourceType")

	_, err = st.GetOrCreateSource("mbox", "user@example.com")
	require.Error(err)
	assert.ErrorContains(err, "only gmail is allowed")
}

func TestStore_GetSourcesByIdentifier_NotFound(t *testing.T) {
	st := testutil.NewTestStore(t)

	sources, err := st.GetSourcesByIdentifier("nobody@example.com")
	require.NoError(t, err, "GetSourcesByIdentifier")
	assert.Empty(t, sources)
}

func TestStore_AttachmentPathsUniqueToSource(t *testing.T) {
	require := require.New(t)
	f := storetest.New(t)

	// Create a second source with its own conversation.
	otherSrc, err := f.Store.GetOrCreateSource("gmail", "other@example.com")
	require.NoError(err, "create other source")
	otherConv, err := f.Store.EnsureConversation(otherSrc.ID, "other-thread", "Other")
	require.NoError(err, "ensure other conv")
	otherMsgID, err := f.Store.UpsertMessage(&store.Message{
		ConversationID:  otherConv,
		SourceID:        otherSrc.ID,
		SourceMessageID: "other-msg-1",
	})
	require.NoError(err, "create other message")

	// Attachment unique to the default source.
	uniqueMsg := f.CreateMessage("msg-unique")
	err = f.Store.UpsertAttachment(uniqueMsg, "u.pdf", "application/pdf",
		"aa/uniquehash", "uniquehash", 10)
	require.NoError(err, "upsert unique attachment")

	// Attachment shared with another source (same content_hash).
	sharedMsg := f.CreateMessage("msg-shared")
	err = f.Store.UpsertAttachment(sharedMsg, "s.pdf", "application/pdf",
		"bb/sharedhash", "sharedhash", 20)
	require.NoError(err, "upsert shared attachment in default source")
	err = f.Store.UpsertAttachment(otherMsgID, "s.pdf", "application/pdf",
		"bb/sharedhash", "sharedhash", 20)
	require.NoError(err, "upsert shared attachment in other source")

	// Attachment with NULL content_hash (must be excluded).
	nullHashMsg := f.CreateMessage("msg-null-hash")
	_, err = f.Store.DB().Exec(
		f.Store.Rebind(`INSERT INTO attachments (message_id, filename, mime_type, storage_path, content_hash, size, created_at)
		 VALUES (?, 'n.pdf', 'application/pdf', 'cc/x', NULL, 30, CURRENT_TIMESTAMP)`),
		nullHashMsg,
	)
	require.NoError(err, "insert null-hash attachment")

	// Attachment with empty storage_path (must be excluded).
	emptyPathMsg := f.CreateMessage("msg-empty-path")
	err = f.Store.UpsertAttachment(emptyPathMsg, "e.pdf", "application/pdf",
		"", "emptypathhash", 40)
	require.NoError(err, "upsert empty-path attachment")

	// URL-backed attachment rows are links, not local files to clean up.
	urlBackedMsg := f.CreateMessage("msg-url-backed")
	_, err = f.Store.DB().Exec(
		f.Store.Rebind(`INSERT INTO attachments (message_id, filename, mime_type, storage_path, content_hash, size, created_at)
		 VALUES (?, 'deck.pptx', 'reference', 'https://sp/deck.pptx', '', 0, CURRENT_TIMESTAMP)`),
		urlBackedMsg,
	)
	require.NoError(err, "insert URL-backed attachment")

	// Two messages in the default source referencing the same unique hash
	// should collapse to a single storage_path in the result.
	dupMsg := f.CreateMessage("msg-dup-hash")
	err = f.Store.UpsertAttachment(dupMsg, "u.pdf", "application/pdf",
		"aa/uniquehash", "uniquehash", 10)
	require.NoError(err, "upsert duplicate-of-unique attachment")

	paths, err := f.Store.AttachmentPathsUniqueToSource(f.Source.ID)
	require.NoError(err, "AttachmentPathsUniqueToSource")

	require.Len(paths, 1, "paths: %v", paths)
	assert.Equal(t, "aa/uniquehash", paths[0], "path[0]")
}

func TestStore_GetSourceByID(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := storetest.New(t)

	got, err := f.Store.GetSourceByID(f.Source.ID)
	require.NoError(err, "GetSourceByID")
	require.NotNil(got, "expected non-nil source")
	assert.Equal(f.Source.ID, got.ID, "ID")
	assert.Equal(f.Source.Identifier, got.Identifier, "Identifier")
}

func TestStore_GetSourceByID_NotFound(t *testing.T) {
	f := storetest.New(t)

	_, err := f.Store.GetSourceByID(99999)
	require.Error(t, err, "expected error for non-existent ID")
}

func TestStore_IsAttachmentPathReferenced(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := storetest.New(t)

	msgID := f.CreateMessage("msg-ref-1")
	err := f.Store.UpsertAttachment(msgID, "a.pdf", "application/pdf",
		"aa/hash1", "hash1", 10)
	require.NoError(err, "UpsertAttachment")

	referenced, err := f.Store.IsAttachmentPathReferenced("aa/hash1")
	require.NoError(err, "IsAttachmentPathReferenced (hit)")
	assert.True(referenced, "expected true for referenced path")

	referenced, err = f.Store.IsAttachmentPathReferenced("zz/nothere")
	require.NoError(err, "IsAttachmentPathReferenced (miss)")
	assert.False(referenced, "expected false for unreferenced path")
}
