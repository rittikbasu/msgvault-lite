package cmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/store"
)

// TestIsFTSIntegrityError_Classification verifies that the hint-classifier
// cleanly separates FTS5 shadow-table errors from core-table errors. Messages come from real
// PRAGMA integrity_check output; the shapes below are what users will see.
func TestIsFTSIntegrityError_Classification(t *testing.T) {
	tests := []struct {
		msg    string
		wantFT bool
	}{
		{
			msg:    "malformed inverted index for FTS5 table main.messages_fts",
			wantFT: true,
		},
		{
			msg:    "row 42 missing from index messages_fts_idx",
			wantFT: true,
		},
		{
			msg:    "Tree 26 page 8231140 cell 2: Rowid 421177 out of order",
			wantFT: false,
		},
		{
			msg:    "non-unique entry in index sqlite_autoindex_messages_1",
			wantFT: false,
		},
		{
			msg:    "",
			wantFT: false,
		},
	}

	for _, tc := range tests {
		got := isFTSIntegrityError(tc.msg)
		assert.Equal(t, tc.wantFT, got, "isFTSIntegrityError(%q)", tc.msg)
	}
}

func TestRunIntegrityCheckDetectsForeignKeyViolations(t *testing.T) {
	require := require.New(t)
	st, err := store.OpenForTest(filepath.Join(t.TempDir(), "msgvault.db"))
	require.NoError(err)
	t.Cleanup(func() { require.NoError(st.Close()) })
	require.NoError(st.InitSchema())
	st.DB().SetMaxOpenConns(1)

	_, err = st.DB().Exec("PRAGMA foreign_keys = OFF")
	require.NoError(err)
	_, err = st.DB().Exec(`
		INSERT INTO attachments (message_id, part_index, storage_path)
		VALUES (999999, 0, 'orphan')
	`)
	require.NoError(err)
	_, err = st.DB().Exec("PRAGMA foreign_keys = ON")
	require.NoError(err)

	errs, err := runIntegrityCheck(st)
	require.NoError(err)
	require.Len(errs, 1)
	assert.Contains(t, errs[0], "foreign key violation")
}

// TestNewVerifyResult checks the machine-readable summary math behind
// `verify --json`: difference is signed (gmailTotal-archived), raw-MIME
// coverage is a percentage of the archived count that must not divide by zero
// on an empty archive, and integrity/sample state is represented without
// claiming an integrity check ran when it did not.
func TestNewVerifyResult(t *testing.T) {
	integrityOK := true
	tests := []struct {
		name        string
		found       bool
		integrity   *bool
		gmailTotal  int64
		archived    int64
		withRaw     int64
		sampleSize  int
		verified    int
		errors      int
		interrupted bool
		wantDiff    int64
		wantPct     float64
	}{
		{
			name:       "missing from archive",
			found:      true,
			integrity:  &integrityOK,
			gmailTotal: 102415,
			archived:   8508,
			withRaw:    8508,
			sampleSize: 100,
			verified:   100,
			wantDiff:   93907,
			wantPct:    100,
		},
		{
			name:       "empty archive avoids divide by zero",
			found:      true,
			integrity:  &integrityOK,
			gmailTotal: 500,
			archived:   0,
			withRaw:    0,
			wantDiff:   500,
			wantPct:    0,
		},
		{
			name:        "extra in archive is negative",
			found:       true,
			integrity:   &integrityOK,
			gmailTotal:  100,
			archived:    120,
			withRaw:     120,
			sampleSize:  25,
			verified:    24,
			errors:      1,
			interrupted: true,
			wantDiff:    -20,
			wantPct:     100,
		},
		{
			name:       "partial raw coverage",
			found:      true,
			integrity:  nil,
			gmailTotal: 10,
			archived:   10,
			withRaw:    5,
			wantDiff:   0,
			wantPct:    50,
		},
		{
			name:       "account not found in archive",
			found:      false,
			integrity:  &integrityOK,
			gmailTotal: 25,
			archived:   0,
			withRaw:    0,
			wantDiff:   25,
			wantPct:    0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			got := newVerifyResult(
				"user@example.com",
				tc.found,
				tc.integrity,
				tc.gmailTotal,
				tc.archived,
				tc.withRaw,
				tc.sampleSize,
				tc.verified,
				tc.errors,
				tc.interrupted,
			)
			assert.Equal("user@example.com", got.Email)
			assert.Equal(tc.found, got.ArchiveAccountFound)
			assert.Equal(tc.integrity != nil, got.DatabaseIntegrityChecked)
			assert.Equal(tc.integrity, got.DatabaseIntegrityOK)
			assert.Equal(tc.gmailTotal, got.GmailMessagesTotal)
			assert.Equal(tc.archived, got.ArchivedMessages)
			assert.Equal(tc.withRaw, got.RawMIMEMessages)
			assert.Equal(tc.wantDiff, got.Difference)
			assert.InDelta(tc.wantPct, got.RawMIMECoveragePct, 0.0001)
			assert.Equal(tc.sampleSize, got.SampleSize)
			assert.Equal(tc.verified, got.SampleVerified)
			assert.Equal(tc.errors, got.SampleErrors)
			assert.Equal(tc.interrupted, got.SampleInterrupted)
		})
	}
}

func TestVerifyAcceptanceError(t *testing.T) {
	integrityOK := true
	integrityFailed := false
	tests := []struct {
		name    string
		result  verifyResult
		wantErr string
	}{
		{
			name:   "healthy",
			result: newVerifyResult("user@example.com", true, &integrityOK, 10, 10, 10, 5, 5, 0, false),
		},
		{
			name:    "missing account",
			result:  newVerifyResult("user@example.com", false, &integrityOK, 10, 0, 0, 0, 0, 0, false),
			wantErr: "not found",
		},
		{
			name:    "database corruption",
			result:  newVerifyResult("user@example.com", true, &integrityFailed, 10, 10, 10, 5, 5, 0, false),
			wantErr: "integrity",
		},
		{
			name:    "count mismatch",
			result:  newVerifyResult("user@example.com", true, &integrityOK, 10, 9, 9, 5, 5, 0, false),
			wantErr: "count mismatch",
		},
		{
			name:    "missing raw MIME",
			result:  newVerifyResult("user@example.com", true, &integrityOK, 10, 10, 9, 5, 5, 0, false),
			wantErr: "raw MIME",
		},
		{
			name:    "sample error",
			result:  newVerifyResult("user@example.com", true, &integrityOK, 10, 10, 10, 5, 4, 1, false),
			wantErr: "sample",
		},
		{
			name:    "interrupted",
			result:  newVerifyResult("user@example.com", true, &integrityOK, 10, 10, 10, 5, 2, 0, true),
			wantErr: "interrupted",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyAcceptanceError(tc.result)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestSyncInterruptionErrorPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := syncInterruptionError(ctx)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
	assert.ErrorContains(t, err, "sync interrupted")
}

func TestVerifyDoesNotCreateMissingArchive(t *testing.T) {
	tmpDir := t.TempDir()
	savedCfg := cfg
	cfg = &config.Config{HomeDir: tmpDir, Data: config.DataConfig{DataDir: tmpDir}}
	t.Cleanup(func() { cfg = savedCfg })

	err := runVerifyLocal(&cobra.Command{}, []string{"user@gmail.com"})
	require.Error(t, err)
	require.ErrorContains(t, err, "open database read-only")

	_, statErr := os.Stat(filepath.Join(tmpDir, "msgvault.db"))
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}
