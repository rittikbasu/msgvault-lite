package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

// formatShowingResults renders the results footer with singular/plural
// agreement ("Showing 1 result" vs "Showing 2 results").
func formatShowingResults(n int) string {
	if n == 1 {
		return "Showing 1 result"
	}
	return fmt.Sprintf("Showing %d results", n)
}

func formatSize(bytes int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)

	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.1fG", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.1fM", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.1fK", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}

func formatCount(value int64) string {
	return strconv.FormatInt(value, 10)
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
