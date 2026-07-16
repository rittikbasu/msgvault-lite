package store_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/rittikbasu/msgvault-lite/internal/store"
)

func TestLiveMessagesWhere_NoAlias(t *testing.T) {
	got := store.LiveMessagesWhere("", true)
	want := "deleted_from_source_at IS NULL"
	assert.Equal(t, want, got)
}

func TestLiveMessagesWhere_WithAlias(t *testing.T) {
	got := store.LiveMessagesWhere("m", true)
	want := "m.deleted_from_source_at IS NULL"
	assert.Equal(t, want, got)
}

func TestLiveMessagesWhere_TableDriven(t *testing.T) {
	cases := []struct {
		alias                 string
		hideDeletedFromSource bool
		want                  string
	}{
		{"", true, "deleted_from_source_at IS NULL"},
		{"", false, "1 = 1"},
		{"m", true, "m.deleted_from_source_at IS NULL"},
		{"m", false, "1 = 1"},
		{"msg", true, "msg.deleted_from_source_at IS NULL"},
		{"msg", false, "1 = 1"},
	}
	for _, tc := range cases {
		got := store.LiveMessagesWhere(tc.alias, tc.hideDeletedFromSource)
		assert.Equal(t, tc.want, got, "LiveMessagesWhere(%q, %v)", tc.alias, tc.hideDeletedFromSource)
	}
}
