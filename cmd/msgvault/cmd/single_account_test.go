package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/rittikbasu/msgvault-lite/internal/testutil"
	"github.com/rittikbasu/msgvault-lite/internal/testutil/storetest"
)

func TestSingleGmailSyncSource(t *testing.T) {
	f := storetest.New(t)

	source, err := singleGmailSyncSource(f.Store, nil)
	require.NoError(t, err)
	assert.Equal(t, f.Source.ID, source.ID)

	source, err = singleGmailSyncSource(f.Store, []string{"test@example.com"})
	require.NoError(t, err)
	assert.Equal(t, f.Source.ID, source.ID)

	_, err = singleGmailSyncSource(f.Store, []string{"other@example.com"})
	require.Error(t, err)
	assert.ErrorContains(t, err, "archive belongs to test@example.com")
}

func TestSingleGmailSyncSourceRejectsMultipleSources(t *testing.T) {
	f := storetest.New(t)
	_, err := f.Store.GetOrCreateSource(sourceTypeGmail, "other@example.com")
	require.NoError(t, err)

	_, err = singleGmailSyncSource(f.Store, nil)
	require.Error(t, err)
	assert.ErrorContains(t, err, "exactly one configured Gmail source; found 2")
}

func TestSingleGmailSyncSourceRejectsNonGmailSource(t *testing.T) {
	st := testutil.NewTestStore(t)
	_, err := st.GetOrCreateSource("imap", "test@example.com")
	require.Error(t, err)
	assert.ErrorContains(t, err, "only gmail is allowed")
}

func TestValidateSingleGmailArchive(t *testing.T) {
	f := storetest.New(t)
	require.NoError(t, validateSingleGmailArchive(f.Store, "test@example.com"))

	err := validateSingleGmailArchive(f.Store, "other@example.com")
	require.Error(t, err)
	assert.ErrorContains(t, err, "exactly one account")
}

func TestValidateSingleGmailArchiveRejectsOtherSourceTypes(t *testing.T) {
	st := testutil.NewTestStore(t)
	_, err := st.GetOrCreateSource("imap", "test@example.com")
	require.Error(t, err)
	assert.ErrorContains(t, err, "only gmail is allowed")
}
