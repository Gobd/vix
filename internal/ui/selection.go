package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// chatSelection tracks an in-progress or completed mouse drag selection over
// the chat viewport. Coordinates are screen-absolute (col, row).
type chatSelection struct {
	active   bool // drag in progress
	startCol int
	startRow int
	endCol   int
	endRow   int
}

// clear resets selection state.
func (s *chatSelection) clear() {
	*s = chatSelection{}
}

// normalised returns (top-left, bottom-right) regardless of drag direction.
func (s chatSelection) normalised() (r0, c0, r1, c1 int) {
	r0, c0, r1, c1 = s.startRow, s.startCol, s.endRow, s.endCol
	if r0 > r1 || (r0 == r1 && c0 > c1) {
		r0, c0, r1, c1 = r1, c1, r0, c0
	}
	return
}

// hasArea returns true when the selection spans at least one character.
func (s chatSelection) hasArea() bool {
	r0, c0, r1, c1 := s.normalised()
	return r0 != r1 || c0 != c1
}

// applyHighlight takes the slice of visible chat lines (as rendered,
// ANSI-coloured strings), the screen row where the first line sits (chatTopRow),
// and the selection, and returns a new slice where the selected region is
// highlighted with a blue background. Lines outside the selection are
// returned unchanged.
func applyHighlight(lines []string, chatTopRow int, sel chatSelection) []string {
	if !sel.hasArea() {
		return lines
	}
	r0, c0, r1, c1 := sel.normalised()

	out := make([]string, len(lines))
	for i, line := range lines {
		screenRow := chatTopRow + i
		if screenRow < r0 || screenRow > r1 {
			out[i] = line
			continue
		}

		lineWidth := ansi.StringWidth(line)

		var lo, hi int
		if screenRow == r0 && screenRow == r1 {
			lo, hi = c0, c1
		} else if screenRow == r0 {
			lo, hi = c0, lineWidth
		} else if screenRow == r1 {
			lo, hi = 0, c1
		} else {
			lo, hi = 0, lineWidth
		}

		if lo >= lineWidth {
			out[i] = line
			continue
		}
		if hi > lineWidth {
			hi = lineWidth
		}
		if lo >= hi {
			out[i] = line
			continue
		}

		// Split line into three segments: before, selected, after.
		before := ansi.Cut(line, 0, lo)
		selected := ansi.Cut(line, lo, hi)
		after := ansi.Cut(line, hi, lineWidth)

		// Strip ANSI from the selected segment before highlighting.
		// ansi.Cut passes through all escape sequences (including those
		// from outside the cut range), so `selected` may contain colour
		// codes that override the highlight. Stripping gives plain text
		// that renders cleanly on the blue background.
		out[i] = before + "\x1b[0m\x1b[44m" + ansi.Strip(selected) + "\x1b[0m" + after
	}
	return out
}

// extractSelectedText strips ANSI from the visible lines and returns the
// plain-text content within the selection, with newlines between rows.
func extractSelectedText(lines []string, chatTopRow int, sel chatSelection) string {
	if !sel.hasArea() {
		return ""
	}
	r0, c0, r1, c1 := sel.normalised()

	var parts []string
	for i, line := range lines {
		screenRow := chatTopRow + i
		if screenRow < r0 || screenRow > r1 {
			continue
		}

		plain := ansi.Strip(line)
		runes := []rune(plain)
		n := len(runes)

		var lo, hi int
		if screenRow == r0 && screenRow == r1 {
			lo, hi = c0, c1
		} else if screenRow == r0 {
			lo, hi = c0, n
		} else if screenRow == r1 {
			lo, hi = 0, c1
		} else {
			lo, hi = 0, n
		}

		if lo > n {
			lo = n
		}
		if hi > n {
			hi = n
		}
		if lo > hi {
			lo = hi
		}

		parts = append(parts, string(runes[lo:hi]))
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}
