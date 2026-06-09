package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestSelectionHighlight prints highlighted output to stdout so you can see
// the actual terminal rendering. Run with: go test ./internal/ui/ -run TestSelectionHighlight -v
func TestSelectionHighlight(t *testing.T) {
	lines := []string{
		"plain text, nothing special here",
		"\x1b[31mred error: something went wrong\x1b[0m",
		"\x1b[33myellow warning: check this out\x1b[0m",
		"\x1b[32mgreen ok: all systems nominal\x1b[0m",
		"mixed: \x1b[31mred\x1b[0m and \x1b[33myellow\x1b[0m and plain",
		"│ table │ cell one │ cell two │",
		"\x1b[36m│ cyan table │ cell │\x1b[0m",
	}

	chatTopRow := 10
	sel := chatSelection{
		startCol: 5,
		startRow: 10,
		endCol:   20,
		endRow:   10 + len(lines) - 1,
	}

	highlighted := applyHighlight(lines, chatTopRow, sel)
	extracted := extractSelectedText(lines, chatTopRow, sel)

	fmt.Println("\n=== RENDERED (blue highlight should appear on cols 5-20) ===")
	for i, h := range highlighted {
		orig := ansi.Strip(lines[i])
		fmt.Printf("orig %d: %s\n", i, orig)
		fmt.Printf("high %d: %s\x1b[0m\n", i, h)
		fmt.Println()
	}

	fmt.Println("=== EXTRACTED TEXT (trimmed) ===")
	fmt.Println(strings.ReplaceAll(extracted, "\n", "↵\n"))
}
