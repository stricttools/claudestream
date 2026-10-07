package printer

import (
	"fmt"
	"strings"

	"github.com/mattn/go-runewidth"
)

// Table lays header and rows out as lines of columns separated by two
// spaces, each column but the last padded to its widest cell by display
// width. Every row must have as many cells as the header.
func Table(header []string, rows [][]string) ([]string, error) {
	if len(header) == 0 {
		return nil, fmt.Errorf("printer: a table needs at least one column")
	}
	widths := make([]int, len(header))
	all := append([][]string{header}, rows...)
	for i, row := range all {
		if len(row) != len(header) {
			return nil, fmt.Errorf("printer: table row %d has %d cells, want %d", i, len(row), len(header))
		}
		for j, cell := range row {
			widths[j] = max(widths[j], runewidth.StringWidth(cell))
		}
	}
	lines := make([]string, 0, len(all))
	for _, row := range all {
		var b strings.Builder
		for j, cell := range row {
			if j == len(row)-1 {
				b.WriteString(cell)
				break
			}
			b.WriteString(runewidth.FillRight(cell, widths[j]))
			b.WriteString("  ")
		}
		lines = append(lines, strings.TrimRight(b.String(), " "))
	}
	return lines, nil
}
