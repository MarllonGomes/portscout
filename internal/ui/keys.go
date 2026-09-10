package ui

import tea "charm.land/bubbletea/v2"

// KeyMap is every binding, in one place so a test can assert none collide.
type KeyMap struct {
	Up        []string
	Down      []string
	PageUp    []string
	PageDown  []string
	Top       []string
	Bottom    []string
	Toggle    []string
	ToggleAll []string
	Edit      []string
	Refresh   []string
	ShowAll   []string
	Help      []string
	Quit      []string
}

func DefaultKeyMap() KeyMap {
	return KeyMap{
		Up:        []string{"up", "k"},
		Down:      []string{"down", "j"},
		PageUp:    []string{"pgup"},
		PageDown:  []string{"pgdown"},
		Top:       []string{"home", "g"},
		Bottom:    []string{"end", "G"},
		Toggle:    []string{"space", "enter"},
		ToggleAll: []string{"a"},
		Edit:      []string{"e"},
		Refresh:   []string{"r", "f5"},
		ShowAll:   []string{"t"},
		Help:      []string{"?", "f1"},
		Quit:      []string{"q"},
	}
}

// all returns every binding grouped by action name, for the collision test.
func (k KeyMap) all() map[string][]string {
	return map[string][]string{
		"Up": k.Up, "Down": k.Down, "PageUp": k.PageUp, "PageDown": k.PageDown,
		"Top": k.Top, "Bottom": k.Bottom, "Toggle": k.Toggle, "ToggleAll": k.ToggleAll,
		"Edit": k.Edit, "Refresh": k.Refresh, "ShowAll": k.ShowAll, "Help": k.Help,
		"Quit": k.Quit,
	}
}

func matches(k tea.KeyPressMsg, binding []string) bool {
	s := k.String()
	for _, b := range binding {
		if s == b {
			return true
		}
	}
	return false
}

func (m Model) normalKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case matches(k, m.keys.Quit):
		m.quitting = true
		return m, tea.Quit
	case matches(k, m.keys.Up):
		return m.moveCursor(-1), nil
	case matches(k, m.keys.Down):
		return m.moveCursor(1), nil
	case matches(k, m.keys.PageUp):
		return m.moveCursor(-m.rowsHeight()), nil
	case matches(k, m.keys.PageDown):
		return m.moveCursor(m.rowsHeight()), nil
	case matches(k, m.keys.Top):
		return m.moveCursor(-len(m.snap.Rows)), nil
	case matches(k, m.keys.Bottom):
		return m.moveCursor(len(m.snap.Rows)), nil
	case matches(k, m.keys.Toggle):
		return m.toggleSelected()
	case matches(k, m.keys.ToggleAll):
		return m.toggleAll()
	case matches(k, m.keys.Edit):
		return m.openEdit()
	case matches(k, m.keys.Refresh):
		m.backend.Refresh()
		return m, nil
	case matches(k, m.keys.ShowAll):
		m.showAll = !m.showAll
		m.backend.SetShowAll(m.showAll)
		return m, nil
	case matches(k, m.keys.Help):
		m.mode = modeHelp
		return m, nil
	}
	// esc closes things; it must never quit the program.
	return m, nil
}
