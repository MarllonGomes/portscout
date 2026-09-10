package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// tier is how much of a row fits at the current width.
type tier int

const (
	tierTiny   tier = iota // too narrow to be useful
	tierNarrow             // checkbox only, no state word
	tierWide               // state word, no message column
	tierFull               // everything
)

// Column geometry. Only ASCII goes inside the aligned columns: "→" and "●" are
// East-Asian *ambiguous* width, so a terminal configured for ambiguous-wide
// draws them two cells wide and every column after them shifts.
const (
	gutterW = 2 // "> " or "  "
	boxW    = 3 // "[x]"
	stateW  = 10
	portW   = 5
	pinW    = 1 // "*" when the user chose this local port
	arrowW  = 4 // " -> "

	minNameW = 12
	minMsgW  = 20

	fullMinWidth   = 92
	wideMinWidth   = 72
	narrowMinWidth = 48
)

func tierFor(width int) tier {
	switch {
	case width >= fullMinWidth:
		return tierFull
	case width >= wideMinWidth:
		return tierWide
	case width >= narrowMinWidth:
		return tierNarrow
	default:
		return tierTiny
	}
}

// frame is the geometry View rendered. Hit-testing reads it, so a click can
// never resolve to a row the user did not actually see.
//
// It is computed in Update and only read in View. View has a value receiver and
// cannot store what it drew, so any layout decided during rendering would be
// invisible to the mouse handler — and the two would drift apart silently.
type frame struct {
	tier     tier
	width    int
	rowY     int // screen line of the first data row
	rowCount int // data rows actually drawn
	top      int // index of the row drawn at rowY

	boxX   [2]int // inclusive x range of the checkbox cell
	localX [2]int // inclusive x range of the local port cell
	nameW  int
	msgW   int // 0 when the column is collapsed
}

// prefixWidth is everything left of the name column.
func (f frame) prefixWidth() int {
	w := gutterW + boxW + 1 + portW + pinW + arrowW + portW + 1
	if f.tier >= tierWide {
		w += stateW + 1
	}
	return w
}

// computeFrame decides the layout for one render. showMsg is false when no
// visible row has anything to say, which gives the names the extra width in the
// common case where nothing is broken.
func computeFrame(width, rowY, rowCount, top int, showMsg bool) frame {
	f := frame{tier: tierFor(width), width: width, rowY: rowY, rowCount: rowCount, top: top}
	if f.tier == tierTiny {
		return f
	}

	x := gutterW
	f.boxX = [2]int{x, x + boxW - 1}
	x += boxW + 1
	if f.tier >= tierWide {
		x += stateW + 1
	}
	f.localX = [2]int{x, x + portW - 1}
	x += portW + pinW + arrowW + portW + 1

	rest := width - x
	if rest < minNameW {
		rest = minNameW
	}
	if f.tier == tierFull && showMsg && rest >= minNameW+1+minMsgW {
		f.msgW = rest - minNameW - 1
		if f.msgW > 30 {
			f.msgW = 30
		}
		f.nameW = rest - f.msgW - 1
	} else {
		f.nameW = rest
	}
	return f
}

// truncateAlias shortens a name to w cells, keeping the trailing " (NNNN)" that
// plan.Aliases adds to disambiguate a container publishing several ports.
//
// Cutting from the right would drop the one part of the name that differs
// between two rows, which is the entire reason the suffix exists.
func truncateAlias(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if runeLen(s) <= w {
		return s
	}
	if head, suffix, ok := splitPortSuffix(s); ok && runeLen(suffix)+2 <= w {
		keep := w - runeLen(suffix) - 1
		return truncRunes(head, keep) + "…" + suffix
	}
	if w == 1 {
		return "…"
	}
	return truncRunes(s, w-1) + "…"
}

// splitPortSuffix pulls a trailing " (1234)" off a name.
func splitPortSuffix(s string) (head, suffix string, ok bool) {
	if !strings.HasSuffix(s, ")") {
		return s, "", false
	}
	i := strings.LastIndex(s, " (")
	if i < 0 {
		return s, "", false
	}
	for _, r := range s[i+2 : len(s)-1] {
		if r < '0' || r > '9' {
			return s, "", false
		}
	}
	return s[:i], s[i:], true
}

func runeLen(s string) int { return len([]rune(s)) }

func truncRunes(s string, n int) string {
	r := []rune(s)
	if n >= len(r) {
		return s
	}
	if n < 0 {
		n = 0
	}
	return string(r[:n])
}

// pad right-pads to exactly w cells, truncating if needed.
func pad(s string, w int) string {
	if w <= 0 {
		return ""
	}
	n := runeLen(s)
	if n > w {
		return truncRunes(s, w)
	}
	return s + strings.Repeat(" ", w-n)
}

// padLeft right-aligns to exactly w cells.
func padLeft(s string, w int) string {
	if w <= 0 {
		return ""
	}
	n := runeLen(s)
	if n > w {
		return truncRunes(s, w)
	}
	return strings.Repeat(" ", w-n) + s
}

// lipglossWidth is the rendered width of a string, ignoring escape codes. Using
// len() here would count ANSI bytes and break every alignment.
func lipglossWidth(s string) int { return lipgloss.Width(s) }
