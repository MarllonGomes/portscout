package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MarllonGomes/portscout/internal/session"
	"github.com/MarllonGomes/portscout/internal/tunnel"
)

func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	// In v2 these are declarative view state rather than program options, so
	// they live here next to what they affect.
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// Render exposes the screen for tests, which never construct a tea.Program.
func (m Model) Render() string { return m.render() }

func (m Model) render() string {
	if m.frame.tier == tierTiny {
		return fmt.Sprintf("janela estreita demais (mín. %d colunas)", narrowMinWidth)
	}
	if m.mode == modeHelp {
		return m.renderHelp()
	}

	var b strings.Builder
	b.WriteString(m.renderHeader())
	b.WriteString(m.renderRows())
	b.WriteString(m.renderDetail())
	if m.height >= 10 {
		b.WriteString(m.renderHints())
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m Model) renderHeader() string {
	left := m.styles.Title.Render(" portscout · " + m.snap.Host)
	right := m.headerRight()

	gap := m.width - lipglossWidth(left) - lipglossWidth(right) - 1
	if gap < 1 {
		gap = 1
	}
	line := left + strings.Repeat(" ", gap) + right + "\n"
	if m.headerHeight() == 1 {
		return line
	}
	return line + "\n" + m.renderHeadings()
}

func (m Model) headerRight() string {
	if m.snap.Fatal || m.snap.LinkErr != "" {
		return m.styles.Error.Render(m.snap.LinkErr + " ")
	}
	if m.snap.ScanErr != "" {
		return m.styles.Error.Render(m.snap.ScanErr + " ")
	}

	active := 0
	for _, r := range m.snap.Rows {
		if r.State == tunnel.Up {
			active++
		}
	}
	status := fmt.Sprintf("%d/%d ativos", active, len(m.snap.Rows))

	switch {
	case m.snap.Link != tunnel.LinkUp:
		status += " · conectando"
	case m.snap.Scanning:
		status += " · varrendo"
	case !m.snap.LastScan.IsZero():
		status += fmt.Sprintf(" · varredura há %s", shortAge(m.now().Sub(m.snap.LastScan)))
	}
	return m.styles.Info.Render(status + " ")
}

func (m Model) renderHeadings() string {
	f := m.frame
	var b strings.Builder
	b.WriteString(strings.Repeat(" ", gutterW+boxW+1))
	if f.tier >= tierWide {
		b.WriteString(pad("ESTADO", stateW) + " ")
	}
	b.WriteString(padLeft("LOCAL", portW) + strings.Repeat(" ", pinW+arrowW))
	b.WriteString(padLeft("REMOTA", portW) + " ")
	b.WriteString(pad("NOME", f.nameW))
	if f.msgW > 0 {
		b.WriteString(" " + pad("MENSAGEM", f.msgW))
	}
	return m.styles.Heading.Render(strings.TrimRight(b.String(), " ")) + "\n"
}

func (m Model) renderRows() string {
	if len(m.snap.Rows) == 0 {
		return fmt.Sprintf("\n  nenhuma porta em LISTEN em %s\n", m.snap.Host)
	}
	var b strings.Builder
	for i := m.frame.top; i < m.frame.top+m.frame.rowCount; i++ {
		b.WriteString(m.renderRow(i, m.snap.Rows[i]) + "\n")
	}
	return b.String()
}

func (m Model) renderRow(i int, r session.Row) string {
	f := m.frame
	selected := i == m.cursor

	gutter := "  "
	if selected {
		gutter = "> "
	}

	var b strings.Builder
	b.WriteString(gutter)
	b.WriteString(m.styleFor(r).Render(checkbox(r)))
	b.WriteString(" ")
	if f.tier >= tierWide {
		b.WriteString(m.styleFor(r).Render(pad(stateWord(r), stateW)) + " ")
	}

	// The edited row shows the prompt in place of the port, so the name and
	// remote port the user is remapping stay visible next to what they type.
	if m.mode == modeEdit && r.RemotePort == m.edit.remotePort {
		b.WriteString(m.styles.Edit.Render(padLeft(m.edit.text, portW)))
	} else {
		b.WriteString(padLeft(strconv.Itoa(r.LocalPort), portW))
	}
	if r.Pinned {
		b.WriteString("*")
	} else {
		b.WriteString(" ")
	}
	b.WriteString(" -> ")
	b.WriteString(padLeft(strconv.Itoa(r.RemotePort), portW) + " ")

	name := truncateAlias(r.Alias, f.nameW)
	if !r.Present {
		b.WriteString(m.styles.Absent.Render(pad(name, f.nameW)))
	} else {
		b.WriteString(pad(name, f.nameW))
	}

	if f.msgW > 0 {
		b.WriteString(" " + m.styles.Failed.Render(pad(truncateAlias(rowMessage(r), f.msgW), f.msgW)))
	}

	line := strings.TrimRight(b.String(), " ")
	if selected {
		return m.styles.Selected.Render(line)
	}
	return line
}

// rowMessage is the short version for the column; the detail line carries the
// full text for the selected row.
func rowMessage(r session.Row) string {
	if r.Err != "" {
		return r.Err
	}
	if !r.Present {
		return "sumiu do remoto"
	}
	return ""
}

func checkbox(r session.Row) string {
	switch {
	case r.State == tunnel.Up:
		return "[x]"
	case r.State == tunnel.Connecting:
		return "[~]"
	case r.State == tunnel.Failed:
		return "[!]"
	default:
		return "[ ]"
	}
}

func stateWord(r session.Row) string {
	switch r.State {
	case tunnel.Up:
		return "ativa"
	case tunnel.Connecting:
		return "conectando"
	case tunnel.Failed:
		return "erro"
	default:
		return "off"
	}
}

func (m Model) styleFor(r session.Row) interface{ Render(...string) string } {
	switch r.State {
	case tunnel.Up:
		return m.styles.Up
	case tunnel.Connecting:
		return m.styles.Pending
	case tunnel.Failed:
		return m.styles.Failed
	default:
		return m.styles.Off
	}
}

// renderDetail is where a full error, a transient confirmation, or a summary of
// the selected row goes. The column says which rows are broken; this says what
// broke.
func (m Model) renderDetail() string {
	if m.mode == modeEdit {
		if m.edit.err != "" {
			return " " + m.styles.Error.Render(m.edit.err) + "\n"
		}
		return " " + m.styles.Info.Render("enter confirma · esc cancela") + "\n"
	}
	if m.status.text != "" {
		return " " + m.styleForStatus().Render(m.status.text) + "\n"
	}

	r, ok := m.selected()
	if !ok {
		return "\n"
	}
	if r.Err != "" {
		text := fmt.Sprintf("erro %d: %s", r.RemotePort, r.Err)
		if r.Detail != "" {
			text += " — " + firstLine(r.Detail)
		}
		return " " + m.styles.Error.Render(truncateAlias(text, m.width-2)) + "\n"
	}

	// Binds do not earn a column — you always reach the port through the tunnel
	// anyway — but they do answer "why can't I hit this from my LAN".
	where := r.RemoteHost
	if where == "" {
		where = "127.0.0.1"
	}
	text := fmt.Sprintf("%d -> %d · %s · escuta em %s no remoto",
		r.LocalPort, r.RemotePort, r.Alias, where)
	return " " + m.styles.Info.Render(truncateAlias(text, m.width-2)) + "\n"
}

func (m Model) styleForStatus() interface{ Render(...string) string } {
	switch m.status.kind {
	case statusWarn:
		return m.styles.Warn
	case statusError:
		return m.styles.Error
	default:
		return m.styles.Info
	}
}

func (m Model) renderHints() string {
	hint := func(key, label string) string {
		return m.styles.HintKey.Render(key) + m.styles.Hint.Render(" "+label)
	}
	parts := []string{
		hint("espaço", "liga/desliga"),
		hint("e", "porta local"),
		hint("a", "tudo"),
		hint("r", "varrer"),
		hint("?", "ajuda"),
		hint("q", "sair"),
	}
	// Drop hints from the right until the line fits. A footer that overflows
	// wraps and eats a row of the table.
	sep := m.styles.Hint.Render(" · ")
	for len(parts) > 1 {
		line := " " + strings.Join(parts, sep)
		if lipglossWidth(line) <= m.width {
			return line
		}
		parts = parts[:len(parts)-1]
	}
	return " " + truncateAlias(parts[0], m.width-1)
}

func (m Model) renderHelp() string {
	rows := [][2]string{
		{"↑ ↓ / k j", "mover"},
		{"pgup pgdn", "página"},
		{"g / G", "topo / fim"},
		{"espaço, enter", "liga/desliga a linha"},
		{"e", "editar a porta local"},
		{"a", "liga/desliga tudo que está visível"},
		{"t", "mostrar também as portas de sistema"},
		{"r, F5", "varrer agora"},
		{"q, ctrl+c", "sair (derruba os túneis)"},
		{"", ""},
		{"clique no [ ]", "liga/desliga aquela linha"},
		{"clique na linha", "só seleciona"},
		{"clique duplo", "liga/desliga"},
		{"duplo na porta local", "editar"},
		{"roda", "move a seleção"},
		{"", ""},
		{"shift+arrastar", "selecionar texto (o mouse está capturado)"},
	}
	var b strings.Builder
	b.WriteString(m.styles.Title.Render(" portscout — ajuda") + "\n\n")
	for _, r := range rows {
		if r[0] == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString("  " + m.styles.HintKey.Render(pad(r[0], 22)) + m.styles.Hint.Render(r[1]) + "\n")
	}
	b.WriteString("\n  " + m.styles.Info.Render("qualquer tecla fecha"))
	return b.String()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

func shortAge(d time.Duration) string {
	switch {
	case d < time.Second:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dmin", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}
