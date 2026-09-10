package ui

import (
	"fmt"
	"strconv"

	tea "charm.land/bubbletea/v2"
)

// editKey owns the keyboard while the inline port prompt is open.
func (m Model) editKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.mode = modeNormal
		return m, nil

	case "enter":
		return m.commitEdit()

	case "backspace":
		if n := len(m.edit.text); n > 0 {
			m.edit.text = m.edit.text[:n-1]
			m.edit.err = ""
		}
		return m, nil
	}

	// Digits only, silently. An error toast for pressing "x" is noise.
	if len(k.String()) == 1 && k.String()[0] >= '0' && k.String()[0] <= '9' {
		if len(m.edit.text) < 5 {
			m.edit.text += k.String()
			m.edit.err = ""
		}
	}
	return m, nil
}

func (m Model) commitEdit() (tea.Model, tea.Cmd) {
	// An empty field cancels rather than erroring: it is what backspacing to
	// nothing and pressing enter obviously means.
	if m.edit.text == "" {
		m.mode = modeNormal
		return m, nil
	}
	port, err := strconv.Atoi(m.edit.text)
	if err != nil {
		m.edit.err = "porta inválida"
		return m, nil
	}
	if local, ok := m.validateLocalPort(port); !ok {
		m.edit.err = local
		return m, nil
	}

	// The session moves a running tunnel itself, and its error is the authority:
	// only it knows what it has already bound.
	if err := m.backend.SetLocalPort(m.edit.remotePort, port); err != nil {
		// The prompt stays open with the text intact, so the user edits one
		// digit instead of retyping and re-navigating.
		m.edit.err = err.Error()
		return m, nil
	}
	m.mode = modeNormal
	text := fmt.Sprintf("porta local de %d agora é %d", m.edit.remotePort, port)
	return m, func() tea.Msg { return statusMsg{text: text, kind: statusInfo} }
}

// validateLocalPort applies the checks the UI can make from the snapshot alone,
// so the user gets an answer immediately instead of after a failed bind.
//
// Whether the port is busy on this machine is deliberately not here: only the
// session knows what it has already bound.
func (m Model) validateLocalPort(port int) (msg string, ok bool) {
	switch {
	case port < 1:
		return "porta fora do intervalo (1024–65535)", false
	case port < 1024:
		return "portas abaixo de 1024 exigem root; escolha outra", false
	case port > 65535:
		return "porta fora do intervalo (1024–65535)", false
	}
	for _, r := range m.snap.Rows {
		if r.RemotePort != m.edit.remotePort && r.LocalPort == port {
			return fmt.Sprintf("porta local %d já é usada por %s", port, r.Alias), false
		}
	}
	return "", true
}
