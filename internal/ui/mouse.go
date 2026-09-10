package ui

import tea "charm.land/bubbletea/v2"

// zone is what the pointer is over.
type zone int

const (
	zoneNone zone = iota
	zoneToggle
	zoneLocal
	zoneRow
)

// rowAt maps a screen line to an index into the visible rows, or -1 when the
// click landed on chrome or on empty space below the last row.
func (m Model) rowAt(y int) int {
	i := y - m.frame.rowY
	if i < 0 || i >= m.frame.rowCount {
		return -1
	}
	return m.frame.top + i
}

// zoneAt resolves a click against the frame that was actually rendered.
func (m Model) zoneAt(x, y int) (zone, int) {
	row := m.rowAt(y)
	if row < 0 || row >= len(m.snap.Rows) {
		return zoneNone, -1
	}
	switch {
	case x >= m.frame.boxX[0] && x <= m.frame.boxX[1]:
		return zoneToggle, row
	case x >= m.frame.localX[0] && x <= m.frame.localX[1]:
		return zoneLocal, row
	default:
		return zoneRow, row
	}
}

func (m Model) onClick(e tea.Mouse) (tea.Model, tea.Cmd) {
	if e.Button != tea.MouseLeft {
		// Right and middle belong to the terminal: context menu and
		// primary-selection paste.
		return m, nil
	}

	// A click outside the prompt cancels the edit and does nothing else. The
	// worst case is retyping four digits.
	if m.mode == modeEdit {
		m.mode = modeNormal
		return m, nil
	}
	if m.mode == modeHelp {
		m.mode = modeNormal
		return m, nil
	}

	z, row := m.zoneAt(e.X, e.Y)
	if z == zoneNone {
		return m, nil
	}

	doubleClick := m.lastClick.y == e.Y && m.now().Sub(m.lastClick.at) <= doubleClickWindow
	m.lastClick = clickState{y: e.Y, at: m.now()}

	m.cursor = row
	m.reframe()

	switch {
	case z == zoneToggle:
		// The checkbox looks like a checkbox, so clicking it means what it
		// looks like.
		return m.toggleSelected()
	case z == zoneLocal && doubleClick:
		return m.openEdit()
	case doubleClick:
		return m.toggleSelected()
	}

	// A single click anywhere else only selects. A misclick on a wide row must
	// not kill a tunnel someone is using, and the most common reason to click a
	// row is to read its error in the detail line.
	return m, nil
}

func (m Model) onWheel(e tea.Mouse) Model {
	// The wheel moves the selection, not the viewport. If it scrolled the
	// viewport on its own, the cursor could sit off-screen and space would
	// toggle a row the user cannot see.
	const lines = 3
	switch e.Button {
	case tea.MouseWheelUp:
		return m.moveCursor(-lines)
	case tea.MouseWheelDown:
		return m.moveCursor(lines)
	}
	return m
}
