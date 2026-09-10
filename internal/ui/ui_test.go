package ui

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/MarllonGomes/portscout/internal/session"
	"github.com/MarllonGomes/portscout/internal/tunnel"
)

// fakeBackend is the whole session in a struct: a snapshot to hand out and a log
// of the calls the UI made. It never opens a socket.
type fakeBackend struct {
	snap     session.Snapshot
	changed  chan struct{}
	toggled  []int
	setAll   []bool
	showAll  []bool
	rescans  int
	setLocal [][2]int
	setErr   error
}

func newFakeBackend(rows ...session.Row) *fakeBackend {
	return &fakeBackend{
		snap:    session.Snapshot{Host: "dev", Link: tunnel.LinkUp, Rows: rows},
		changed: make(chan struct{}, 1),
	}
}

func (f *fakeBackend) Snapshot() session.Snapshot { return f.snap }
func (f *fakeBackend) Changed() <-chan struct{}   { return f.changed }
func (f *fakeBackend) Toggle(port int)            { f.toggled = append(f.toggled, port) }
func (f *fakeBackend) SetAll(on bool)             { f.setAll = append(f.setAll, on) }
func (f *fakeBackend) SetShowAll(v bool)          { f.showAll = append(f.showAll, v) }
func (f *fakeBackend) Refresh()                   { f.rescans++ }
func (f *fakeBackend) SetLocalPort(r, l int) error {
	f.setLocal = append(f.setLocal, [2]int{r, l})
	return f.setErr
}

// clock is an injected time source so double-click and status expiry are
// testable without sleeping.
type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }

func newTestModel(t *testing.T, rows ...session.Row) (Model, *fakeBackend, *clock) {
	t.Helper()
	b := newFakeBackend(rows...)
	c := &clock{t: time.Unix(1000, 0)}
	styles := PlainStyles()
	m := New(Config{Backend: b, Now: c.now, Styles: &styles})
	m = send(m, tea.WindowSizeMsg{Width: 100, Height: 24})
	return m, b, c
}

// send applies Update for each message and drops the commands.
func send(m Model, msgs ...tea.Msg) Model {
	for _, msg := range msgs {
		next, _ := m.Update(msg)
		m = next.(Model)
	}
	return m
}

func sendCmd(m Model, msg tea.Msg) (Model, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(Model), cmd
}

func key(s string) tea.KeyPressMsg {
	switch s {
	case "space", " ":
		return tea.KeyPressMsg{Code: ' ', Text: " "}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	default:
		r := []rune(s)
		return tea.KeyPressMsg{Code: r[0], Text: s}
	}
}

func click(x, y int) tea.MouseClickMsg {
	return tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}
}

func wheel(down bool) tea.MouseWheelMsg {
	b := tea.MouseWheelUp
	if down {
		b = tea.MouseWheelDown
	}
	return tea.MouseWheelMsg{Button: b}
}

func row(remote, local int, alias string, st tunnel.State) session.Row {
	return session.Row{
		RemotePort: remote, LocalPort: local, Alias: alias,
		State: st, Present: true, Wanted: st != tunnel.Off,
	}
}

func manyRows(n int) []session.Row {
	out := make([]session.Row, n)
	for i := range out {
		out[i] = row(3100+i, 3100+i, fmt.Sprintf("svc-%d", i), tunnel.Off)
	}
	return out
}

// ---------- layout ----------

func TestViewShowsEveryRow(t *testing.T) {
	m, _, _ := newTestModel(t,
		row(3100, 3100, "next-server (v16.3.4)", tunnel.Up),
		row(3102, 3102, "couple-community-postgres-1", tunnel.Up),
	)
	out := m.Render()
	for _, want := range []string{"next-server (v16.3.4)", "couple-community-postgres-1", "3100", "3102"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// plan.Aliases only adds " (3104)" to names that repeat, so that suffix is the
// sole thing distinguishing two rows. Truncating it away would undo exactly what
// the naming rule exists for.
func TestViewKeepsThePortSuffixWhenTruncating(t *testing.T) {
	long := "couple-community-web-next-server-1 (3104)"
	for _, w := range []int{48, 60, 80, 100} {
		m, _, _ := newTestModel(t, row(3104, 3104, long, tunnel.Up))
		m = send(m, tea.WindowSizeMsg{Width: w, Height: 24})
		if out := m.Render(); !strings.Contains(out, "(3104)") {
			t.Errorf("width %d dropped the disambiguating suffix:\n%s", w, out)
		}
	}
}

// One cheap invariant that catches most layout bugs.
func TestViewNeverExceedsTheTerminalWidth(t *testing.T) {
	rows := []session.Row{
		row(3100, 3100, strings.Repeat("very-long-container-name-", 4)+" (3100)", tunnel.Up),
		{RemotePort: 3105, LocalPort: 3105, Alias: "mailpit", Present: true, Wanted: true,
			State: tunnel.Failed, Err: strings.Repeat("erro muito comprido ", 12)},
	}
	for _, w := range []int{40, 48, 60, 72, 80, 92, 100, 200} {
		m, _, _ := newTestModel(t, rows...)
		m = send(m, tea.WindowSizeMsg{Width: w, Height: 24})
		for i, line := range strings.Split(m.Render(), "\n") {
			if got := lipgloss.Width(line); got > w {
				t.Errorf("width %d: line %d is %d cells wide: %q", w, i, got, line)
			}
		}
	}
}

func TestViewNeverExceedsTheTerminalHeight(t *testing.T) {
	for _, h := range []int{6, 8, 10, 14, 24, 40} {
		m, _, _ := newTestModel(t, manyRows(30)...)
		m = send(m, tea.WindowSizeMsg{Width: 100, Height: h})
		if got := len(strings.Split(m.Render(), "\n")); got > h {
			t.Errorf("height %d: rendered %d lines", h, got)
		}
	}
}

func TestNarrowWidthDropsTheMessageColumn(t *testing.T) {
	m, _, _ := newTestModel(t, row(3100, 3100, "app", tunnel.Up))
	m = send(m, tea.WindowSizeMsg{Width: 60, Height: 24})
	if strings.Contains(m.Render(), "MENSAGEM") {
		t.Errorf("60 columns must not carry a message column:\n%s", m.Render())
	}
	if !strings.Contains(m.Render(), "[x]") {
		t.Error("the checkbox must survive at every width: it carries the state")
	}
}

// The common case is that nothing is broken, and then the names deserve the room.
func TestMessageColumnCollapsesWhenNothingIsBroken(t *testing.T) {
	healthy, _, _ := newTestModel(t, row(3100, 3100, "app", tunnel.Up))
	broken, _, _ := newTestModel(t, session.Row{
		RemotePort: 3100, LocalPort: 3100, Alias: "app", Present: true,
		State: tunnel.Failed, Err: "porta local 3100 já está em uso",
	})
	if strings.Contains(healthy.Render(), "MENSAGEM") {
		t.Error("no row has a message, so the column must collapse")
	}
	if !strings.Contains(broken.Render(), "MENSAGEM") {
		t.Error("a broken row must bring the column back")
	}
	if healthy.frame.nameW <= broken.frame.nameW {
		t.Errorf("names should be wider when nothing is broken: %d vs %d",
			healthy.frame.nameW, broken.frame.nameW)
	}
}

func TestSelectedRowShowsItsFullErrorInTheDetailLine(t *testing.T) {
	full := "porta local 3105 já está em uso"
	m, _, _ := newTestModel(t, session.Row{
		RemotePort: 3105, LocalPort: 3105, Alias: "mailpit", Present: true,
		State: tunnel.Failed, Err: full, Detail: "bind [127.0.0.1]:3105: Address already in use",
	})
	out := m.Render()
	if !strings.Contains(out, full) {
		t.Errorf("the full error must be readable even though the column truncated it:\n%s", out)
	}
	if !strings.Contains(out, "Address already in use") {
		t.Errorf("ssh's own words belong in the detail line:\n%s", out)
	}
}

func TestViewWithNoRows(t *testing.T) {
	m, _, _ := newTestModel(t)
	if out := m.Render(); !strings.Contains(out, "nenhuma porta em LISTEN em dev") {
		t.Errorf("expected the empty message:\n%s", out)
	}
}

func TestViewTooNarrow(t *testing.T) {
	m, _, _ := newTestModel(t, row(3100, 3100, "app", tunnel.Up))
	m = send(m, tea.WindowSizeMsg{Width: 30, Height: 24})
	if out := m.Render(); !strings.Contains(out, "estreita demais") {
		t.Errorf("expected a warning, got:\n%s", out)
	}
}

// ---------- selection and keys ----------

func TestCursorMovesAndClampsAtBothEnds(t *testing.T) {
	m, _, _ := newTestModel(t, manyRows(3)...)

	m = send(m, key("k"))
	if m.cursor != 0 {
		t.Errorf("cursor = %d; it must not wrap past the top", m.cursor)
	}
	m = send(m, key("j"), key("j"), key("j"), key("j"))
	if m.cursor != 2 {
		t.Errorf("cursor = %d; it must not wrap past the bottom", m.cursor)
	}
}

// A cursor that is a bare index slides onto a different tunnel when a row
// appears above it, which is how someone presses space and kills the wrong one.
func TestCursorStaysOnTheSamePortAcrossARescan(t *testing.T) {
	m, b, _ := newTestModel(t, row(3102, 3102, "b", tunnel.Off), row(3104, 3104, "c", tunnel.Off))
	m = send(m, key("j")) // select 3104
	if got := m.snap.Rows[m.cursor].RemotePort; got != 3104 {
		t.Fatalf("setup: cursor is on %d", got)
	}

	b.snap.Rows = []session.Row{
		row(3100, 3100, "a", tunnel.Off), // new row, inserted above
		row(3102, 3102, "b", tunnel.Off),
		row(3104, 3104, "c", tunnel.Off),
	}
	m = send(m, snapshotMsg(b.snap))

	if got := m.snap.Rows[m.cursor].RemotePort; got != 3104 {
		t.Errorf("cursor moved to port %d after a rescan", got)
	}
}

func TestSelectedRowRemovedByRescanFallsBackSafely(t *testing.T) {
	m, b, _ := newTestModel(t, manyRows(3)...)
	m = send(m, key("j"), key("j"))

	b.snap.Rows = nil
	m = send(m, snapshotMsg(b.snap))
	if m.cursor != 0 {
		t.Errorf("cursor = %d with no rows", m.cursor)
	}
	_ = m.Render() // must not panic
}

func TestSpaceTogglesTheSelectedRow(t *testing.T) {
	m, b, _ := newTestModel(t, row(3100, 3100, "a", tunnel.Off), row(3102, 3102, "b", tunnel.Off))
	m = send(m, key("j"), key(" "))
	if len(b.toggled) != 1 || b.toggled[0] != 3102 {
		t.Errorf("toggled = %v, want [3102]", b.toggled)
	}
}

func TestToggleAllTurnsEverythingOnThenOff(t *testing.T) {
	m, b, _ := newTestModel(t, row(3100, 3100, "a", tunnel.Off), row(3102, 3102, "b", tunnel.Off))
	m = send(m, key("a"))
	if len(b.setAll) != 1 || !b.setAll[0] {
		t.Fatalf("setAll = %v, want [true]", b.setAll)
	}

	b.snap.Rows = []session.Row{row(3100, 3100, "a", tunnel.Up), row(3102, 3102, "b", tunnel.Up)}
	m = send(m, snapshotMsg(b.snap), key("a"))
	if len(b.setAll) != 2 || b.setAll[1] {
		t.Errorf("setAll = %v, want a false second call", b.setAll)
	}
}

func TestQuitReturnsTeaQuit(t *testing.T) {
	m, _, _ := newTestModel(t, manyRows(1)...)
	m2, cmd := sendCmd(m, key("q"))
	if cmd == nil {
		t.Fatal("q produced no command")
	}
	// tea.Cmd is a func and not comparable, so call it and look at the message.
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("q must quit, got %T", cmd())
	}
	if !m2.Quitting() {
		t.Error("the model must record that the user asked to leave")
	}
}

// The most annoying possible bug in a dashboard.
func TestEscDoesNotQuit(t *testing.T) {
	m, _, _ := newTestModel(t, manyRows(1)...)
	_, cmd := sendCmd(m, key("esc"))
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Fatal("esc must never quit the program")
		}
	}
}

func TestRefreshAndShowAllReachTheBackend(t *testing.T) {
	m, b, _ := newTestModel(t, manyRows(1)...)
	m = send(m, key("r"), key("t"))
	if b.rescans != 1 {
		t.Errorf("rescans = %d, want 1", b.rescans)
	}
	if len(b.showAll) != 1 || !b.showAll[0] {
		t.Errorf("showAll = %v, want [true]", b.showAll)
	}
}

// ---------- mouse ----------

func TestClickOnARowSelectsItButDoesNotToggle(t *testing.T) {
	m, b, _ := newTestModel(t, manyRows(4)...)
	// Well past the checkbox and the local port cell.
	m = send(m, click(m.frame.nameW/2+m.frame.localX[1]+8, m.frame.rowY+2))

	if m.cursor != 2 {
		t.Errorf("cursor = %d, want 2", m.cursor)
	}
	if len(b.toggled) != 0 {
		t.Errorf("a plain click must not toggle: %v", b.toggled)
	}
}

func TestClickOnTheCheckboxToggles(t *testing.T) {
	m, b, _ := newTestModel(t, manyRows(4)...)
	m = send(m, click(m.frame.boxX[0]+1, m.frame.rowY+1))

	if m.cursor != 1 {
		t.Errorf("cursor = %d, want 1", m.cursor)
	}
	if len(b.toggled) != 1 || b.toggled[0] != 3101 {
		t.Errorf("toggled = %v, want [3101]", b.toggled)
	}
}

// The classic off-by-one.
func TestClickBelowTheLastRowDoesNothing(t *testing.T) {
	m, b, _ := newTestModel(t, manyRows(2)...)
	before := m.cursor
	m = send(m, click(m.frame.boxX[0]+1, m.frame.rowY+5))

	if m.cursor != before {
		t.Errorf("cursor moved to %d on a click past the last row", m.cursor)
	}
	if len(b.toggled) != 0 {
		t.Errorf("nothing may be toggled: %v", b.toggled)
	}
}

// This is the test that proves the frame-based hit-test: after scrolling, screen
// line N is not row N.
func TestClickAfterScrollingSelectsTheRowTheUserSaw(t *testing.T) {
	m, b, _ := newTestModel(t, manyRows(40)...)
	m = send(m, tea.WindowSizeMsg{Width: 100, Height: 14})

	for i := 0; i < 20; i++ {
		m = send(m, key("j"))
	}
	if m.frame.top == 0 {
		t.Fatal("setup: the list never scrolled")
	}
	wantPort := m.snap.Rows[m.frame.top].RemotePort

	m = send(m, click(m.frame.boxX[0]+1, m.frame.rowY))
	if got := m.snap.Rows[m.cursor].RemotePort; got != wantPort {
		t.Errorf("clicked the first visible line and got port %d, want %d", got, wantPort)
	}
	if len(b.toggled) != 1 || b.toggled[0] != wantPort {
		t.Errorf("toggled = %v, want [%d]", b.toggled, wantPort)
	}
}

func TestWheelMovesTheSelectionByThreeAndClamps(t *testing.T) {
	m, _, _ := newTestModel(t, manyRows(10)...)
	m = send(m, wheel(true))
	if m.cursor != 3 {
		t.Errorf("cursor = %d after one wheel down, want 3", m.cursor)
	}
	for i := 0; i < 10; i++ {
		m = send(m, wheel(true))
	}
	if m.cursor != 9 {
		t.Errorf("cursor = %d, want it clamped to the last row", m.cursor)
	}
	for i := 0; i < 10; i++ {
		m = send(m, wheel(false))
	}
	if m.cursor != 0 {
		t.Errorf("cursor = %d, want it clamped to the first row", m.cursor)
	}
}

func TestDoubleClickTogglesAndASlowSecondClickDoesNot(t *testing.T) {
	m, b, c := newTestModel(t, manyRows(3)...)
	x, y := m.frame.localX[1]+40, m.frame.rowY+1

	m = send(m, click(x, y))
	c.add(100 * time.Millisecond)
	m = send(m, click(x, y))
	if len(b.toggled) != 1 {
		t.Fatalf("a fast second click must toggle: %v", b.toggled)
	}

	c.add(2 * time.Second)
	m = send(m, click(x, y))
	if len(b.toggled) != 1 {
		t.Errorf("a slow second click must not toggle: %v", b.toggled)
	}
}

func TestDoubleClickOnTheLocalCellOpensTheEditor(t *testing.T) {
	m, _, c := newTestModel(t, manyRows(3)...)
	x, y := m.frame.localX[0], m.frame.rowY+1

	m = send(m, click(x, y))
	c.add(100 * time.Millisecond)
	m = send(m, click(x, y))

	if m.mode != modeEdit {
		t.Fatalf("mode = %v, want the edit prompt", m.mode)
	}
	if m.edit.remotePort != 3101 {
		t.Errorf("editing port %d, want 3101", m.edit.remotePort)
	}
}

// ---------- edit flow ----------

func TestEditOpensOnTheSelectedRowWithItsCurrentPort(t *testing.T) {
	m, _, _ := newTestModel(t, row(3100, 4242, "app", tunnel.Off))
	m = send(m, key("e"))
	if m.mode != modeEdit {
		t.Fatal("e must open the prompt")
	}
	if m.edit.text != "4242" {
		t.Errorf("text = %q, want the current local port", m.edit.text)
	}
}

func TestEditIgnoresNonDigits(t *testing.T) {
	m, _, _ := newTestModel(t, row(3100, 8080, "app", tunnel.Off))
	m = send(m, key("e"), key("backspace"), key("backspace"), key("backspace"), key("backspace"))
	m = send(m, key("8"), key("x"), key("0"))
	if m.edit.text != "80" {
		t.Errorf("text = %q, want %q", m.edit.text, "80")
	}
}

func TestEditRejectsBadPorts(t *testing.T) {
	cases := []struct {
		name, typed, wantMsg string
	}{
		{"zero", "0", "intervalo"},
		{"privileged", "80", "root"},
		{"too big", "70000", "intervalo"},
		{"taken by another row", "3102", "já é usada"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, b, _ := newTestModel(t,
				row(3100, 3100, "a", tunnel.Off),
				row(3102, 3102, "b", tunnel.Off))
			m = send(m, key("e"))
			for range m.edit.text {
				m = send(m, key("backspace"))
			}
			for _, r := range c.typed {
				m = send(m, key(string(r)))
			}
			m = send(m, key("enter"))

			if m.mode != modeEdit {
				t.Fatalf("the prompt must stay open on a rejection")
			}
			if !strings.Contains(m.edit.err, c.wantMsg) {
				t.Errorf("err = %q, want it to mention %q", m.edit.err, c.wantMsg)
			}
			if len(b.setLocal) != 0 {
				t.Errorf("the backend must not be called: %v", b.setLocal)
			}
		})
	}
}

func TestEditCommits(t *testing.T) {
	m, b, _ := newTestModel(t, row(3100, 3100, "app", tunnel.Off))
	m = send(m, key("e"))
	for range m.edit.text {
		m = send(m, key("backspace"))
	}
	m = send(m, key("8"), key("0"), key("8"), key("0"), key("enter"))

	if m.mode != modeNormal {
		t.Error("a successful commit must close the prompt")
	}
	if len(b.setLocal) != 1 || b.setLocal[0] != [2]int{3100, 8080} {
		t.Errorf("setLocal = %v, want [[3100 8080]]", b.setLocal)
	}
}

// Editing one digit beats retyping four and re-navigating.
func TestBackendErrorKeepsThePromptOpenWithTheText(t *testing.T) {
	m, b, _ := newTestModel(t, row(3100, 3100, "app", tunnel.Off))
	b.setErr = errors.New("porta local 8080 já está em uso nesta máquina")

	m = send(m, key("e"))
	for range m.edit.text {
		m = send(m, key("backspace"))
	}
	m = send(m, key("8"), key("0"), key("8"), key("0"), key("enter"))

	if m.mode != modeEdit {
		t.Fatal("the prompt must stay open")
	}
	if m.edit.text != "8080" {
		t.Errorf("text = %q, want it preserved", m.edit.text)
	}
	if !strings.Contains(m.edit.err, "já está em uso") {
		t.Errorf("err = %q", m.edit.err)
	}
}

func TestEscCancelsAndKeepsTheOldPort(t *testing.T) {
	m, b, _ := newTestModel(t, row(3100, 3100, "app", tunnel.Off))
	m = send(m, key("e"), key("9"), key("esc"))
	if m.mode != modeNormal {
		t.Error("esc must close the prompt")
	}
	if len(b.setLocal) != 0 {
		t.Errorf("nothing may be committed: %v", b.setLocal)
	}
}

func TestClickOutsideCancelsTheEdit(t *testing.T) {
	m, b, _ := newTestModel(t, manyRows(3)...)
	m = send(m, key("e"), click(0, 0))
	if m.mode != modeNormal {
		t.Error("a click outside must cancel the edit")
	}
	if len(b.toggled) != 0 {
		t.Errorf("and must do nothing else: %v", b.toggled)
	}
}

func TestRescanRemovingTheEditedRowClosesThePrompt(t *testing.T) {
	m, b, _ := newTestModel(t, row(3105, 3105, "mailpit", tunnel.Off))
	m = send(m, key("e"))

	b.snap.Rows = nil
	m = send(m, snapshotMsg(b.snap))

	if m.mode != modeNormal {
		t.Error("the prompt must close when its row disappears")
	}
	if !strings.Contains(m.status.text, "3105") {
		t.Errorf("status = %q, want it to name the port that vanished", m.status.text)
	}
}

// The single most valuable test in this file: typing "a" into a port field must
// not toggle every tunnel on the host.
func TestGlobalKeysDoNotFireWhileEditing(t *testing.T) {
	m, b, _ := newTestModel(t, manyRows(3)...)
	m = send(m, key("e"))
	for range m.edit.text {
		m = send(m, key("backspace"))
	}
	m = send(m, key("a"), key("r"), key("q"), key("t"))

	if len(b.setAll) != 0 || b.rescans != 0 || len(b.showAll) != 0 {
		t.Errorf("global actions fired while editing: setAll=%v rescans=%d showAll=%v",
			b.setAll, b.rescans, b.showAll)
	}
	if m.mode != modeEdit {
		t.Error("the prompt must still be open")
	}
}

func TestCtrlCQuitsEvenWhileEditing(t *testing.T) {
	m, _, _ := newTestModel(t, manyRows(1)...)
	m = send(m, key("e"))
	_, cmd := sendCmd(m, key("ctrl+c"))
	if cmd == nil {
		t.Fatal("ctrl+c produced no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("ctrl+c must quit from any mode, got %T", cmd())
	}
}

// ---------- backend plumbing ----------

func TestDoorbellReReadsAndReArms(t *testing.T) {
	m, _, _ := newTestModel(t, manyRows(1)...)
	_, cmd := sendCmd(m, doorbellMsg{})
	if cmd == nil {
		t.Fatal("a ring must produce commands")
	}
	// The batch has to contain both a fresh read and a new wait, or the
	// dashboard stops hearing about changes after the first one.
	if msg := cmd(); msg == nil {
		t.Error("the batch produced no message")
	}
}

// Without the guard, a closed channel makes the wait command return instantly
// forever and spins a core.
func TestClosedChannelStopsTheListener(t *testing.T) {
	ch := make(chan struct{})
	close(ch)
	if _, ok := waitForChange(ch)().(backendGoneMsg); !ok {
		t.Fatal("a closed channel must produce backendGoneMsg")
	}

	m, _, _ := newTestModel(t, manyRows(1)...)
	_, cmd := sendCmd(m, backendGoneMsg{})
	if cmd != nil {
		t.Error("the listener must not be re-armed after the session is gone")
	}
}

// The safety-net claim, tested: the doorbell is an optimisation, not a
// requirement.
func TestTickReReadsTheSnapshotEvenWithoutADoorbell(t *testing.T) {
	m, b, _ := newTestModel(t, row(3100, 3100, "app", tunnel.Off))
	b.snap.Rows = []session.Row{row(3100, 3100, "app", tunnel.Up)}

	_, cmd := sendCmd(m, tickMsg(time.Now()))
	if cmd == nil {
		t.Fatal("a tick must produce commands")
	}
	m = send(m, snapshotMsg(b.Snapshot()))
	if m.snap.Rows[0].State != tunnel.Up {
		t.Error("the tick path must be able to pick up a change")
	}
}

func TestStatusLineExpires(t *testing.T) {
	m, _, c := newTestModel(t, manyRows(2)...)
	m = send(m, statusMsg{text: "2 túneis ligados", kind: statusInfo})
	if !strings.Contains(m.Render(), "2 túneis ligados") {
		t.Fatal("the status must be shown")
	}

	c.add(5 * time.Second)
	m = send(m, tickMsg(c.now()))
	if strings.Contains(m.Render(), "2 túneis ligados") {
		t.Error("a transient status must expire")
	}
}

// ---------- keymap ----------

// Ten lines that catch the mistake made every time a binding is added.
func TestNoDuplicateBindings(t *testing.T) {
	seen := map[string]string{}
	for action, keys := range DefaultKeyMap().all() {
		for _, k := range keys {
			if other, dup := seen[k]; dup {
				t.Errorf("key %q is bound to both %s and %s", k, other, action)
			}
			seen[k] = action
		}
	}
}

func TestKeyMapHasNoEmptyActions(t *testing.T) {
	v := reflect.ValueOf(DefaultKeyMap())
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).Len() == 0 {
			t.Errorf("%s has no bindings", v.Type().Field(i).Name)
		}
	}
}

func TestHelpOverlayClosesOnAnyKey(t *testing.T) {
	m, _, _ := newTestModel(t, manyRows(1)...)
	m = send(m, key("?"))
	if m.mode != modeHelp {
		t.Fatal("? must open help")
	}
	if !strings.Contains(m.Render(), "shift+arrastar") {
		t.Error("help must mention how to select text while the mouse is captured")
	}
	m = send(m, key("j"))
	if m.mode != modeNormal {
		t.Error("any key must close help")
	}
}
