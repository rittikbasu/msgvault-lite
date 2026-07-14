package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFormatShowingResults(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "Showing 0 results"},
		{1, "Showing 1 result"},
		{2, "Showing 2 results"},
		{100, "Showing 100 results"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, formatShowingResults(tt.n), "formatShowingResults(%d)", tt.n)
	}
}
