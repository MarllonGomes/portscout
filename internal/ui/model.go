// Package ui is the terminal dashboard: it renders a session snapshot and turns
// keys and clicks into requests back to it.
//
// Every user-facing string in the program lives here. The layers below it are
// language-free, so the wording is in one place.
package ui

import (
	"fmt"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MarllonGomes/portscout/internal/session"
)

// Backend is everything the dashboard needs. Every method must return without
// blocking on ssh: the UI calls them straight from Update, and an Update that
// blocks freezes the terminal, including ctrl+c.
type Backend interface {
	// Snapshot returns the current state. It must be cheap: the UI reads it on
	// every doorbell ring and once a second regardless.
	Snapshot() session.Snapshot
	// Changed is a doorbell, not a stream. The answer to a ring is always a
	// fresh Snapshot, so nothing is ever missed by a dropped ring.
	Changed() <-chan struct{}
	Toggle(remotePort int)
	SetAll(on bool)
	SetShowAll(v bool)
	Refresh()
	// SetLocalPort is the one synchronous call, because its error is what the
	// edit prompt has to show.
	SetLocalPort(remotePort, localPort int) error
}

type mode int

const (
	modeNormal mode = iota
	modeEdit
	modeHelp
)

// statusKind colours the detail line.
type statusKind int

const (
	statusInfo statusKind = iota
	statusWarn
	statusError
)

type status struct {
	text    string
	kind    statusKind
	expires time.Time // zero means sticky
}

// editState is the inline local-port prompt.
//
// It keys on the remote port, never on a row index: a rescan can reorder or
// remove rows while the prompt is open.
type editState struct {
	remotePort int
	text       string
	err        string
}

type clickState struct {
	y  int
	at time.Time
}

// doubleClickWindow is deliberately generous. This is a dashboard, not a game,
// and a missed double click silently does nothing, which is worse than a slow one.
const doubleClickWindow = 400 * time.Millisecond

// Model is the whole dashboard. Everything except the injected fields is derived
// from the last snapshot, so a test can build any screen by feeding one message.
type Model struct {
	backend Backend
	keys    KeyMap
	now     func() time.Time
	styles  Styles

	snap       session.Snapshot
	noticeSeen uint64 // the last notice sequence latched into the status line
	cursor     int    // index into snap.Rows
	top        int    // first visible row

	width, height int
	frame         frame

	mode      mode
	edit      editState
	status    status
	lastClick clickState
	showAll   bool
	quitting  bool
}

// Config wires a dashboard. Now is injected so double-click and status expiry
// are testable without sleeping.
type Config struct {
	Backend Backend
	Now     func() time.Time
	Styles  *Styles
}

func New(cfg Config) Model {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	styles := DefaultStyles()
	if cfg.Styles != nil {
		styles = *cfg.Styles
	}
	m := Model{
		backend: cfg.Backend,
		keys:    DefaultKeyMap(),
		now:     now,
		styles:  styles,
		width:   100,
		height:  24,
	}
	if cfg.Backend != nil {
		m.snap = cfg.Backend.Snapshot()
	}
	m.reframe()
	return m
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(readSnapshot(m.backend), waitForChange(m.backend.Changed()), tick())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// ctrl+c is handled before anything else can swallow it, in every mode.
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "ctrl+c" {
		m.quitting = true
		return m, tea.Quit
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.reframe()
		return m, nil

	case snapshotMsg:
		return m.applySnapshot(session.Snapshot(msg)), nil

	case doorbellMsg:
		return m, tea.Batch(readSnapshot(m.backend), waitForChange(m.backend.Changed()))

	case backendGoneMsg:
		// Without this the closed channel would make the wait command return
		// instantly forever and spin a core.
		m.status = status{text: "a sessão terminou", kind: statusError}
		return m, nil

	case tickMsg:
		if !m.status.expires.IsZero() && m.now().After(m.status.expires) {
			m.status = status{}
		}
		return m, tea.Batch(readSnapshot(m.backend), tick())

	case statusMsg:
		m.status = status{text: msg.text, kind: msg.kind, expires: m.now().Add(4 * time.Second)}
		return m, nil

	case tea.MouseWheelMsg:
		return m.onWheel(msg.Mouse()), nil

	case tea.MouseClickMsg:
		return m.onClick(msg.Mouse())
	}

	// Modes own the keyboard entirely: a key typed into the prompt must never
	// reach the global map. Typing "a" into the port field toggling every tunnel
	// is the classic version of this bug.
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch m.mode {
		case modeEdit:
			return m.editKey(k)
		case modeHelp:
			m.mode = modeNormal
			return m, nil
		default:
			return m.normalKey(k)
		}
	}
	return m, nil
}

func (m Model) applySnapshot(snap session.Snapshot) Model {
	prev := m.snap.Rows
	m.snap = snap
	m.cursor = remapCursor(prev, m.cursor, snap.Rows)

	// The edited row may have vanished under the prompt.
	if m.mode == modeEdit {
		if _, ok := rowByPort(snap.Rows, m.edit.remotePort); !ok {
			m.mode = modeNormal
			m.status = status{
				text:    fmt.Sprintf("a porta %d sumiu do remoto", m.edit.remotePort),
				kind:    statusWarn,
				expires: m.now().Add(4 * time.Second),
			}
		}
	}
	// Latch a notice once, by sequence. Keying off the text would re-show the
	// same message on every later snapshot, which is how a transient line ends
	// up pinned to the screen forever.
	if snap.NoticeSeq != m.noticeSeen {
		m.noticeSeen = snap.NoticeSeq
		if snap.Notice != "" {
			m.status = status{text: snap.Notice, kind: statusInfo, expires: m.now().Add(6 * time.Second)}
		}
	}
	m.reframe()
	return m
}

// remapCursor keeps the selection on the same remote port across a rescan.
//
// Rows appear and disappear while the user is reading. A cursor that is a bare
// index silently slides onto a different tunnel, which is how someone presses
// space and kills the wrong one.
func remapCursor(old []session.Row, oldIdx int, fresh []session.Row) int {
	if len(fresh) == 0 {
		return 0
	}
	if oldIdx >= 0 && oldIdx < len(old) {
		want := old[oldIdx].RemotePort
		for i, r := range fresh {
			if r.RemotePort == want {
				return i
			}
		}
	}
	if oldIdx >= len(fresh) {
		return len(fresh) - 1
	}
	if oldIdx < 0 {
		return 0
	}
	return oldIdx
}

func rowByPort(rows []session.Row, port int) (session.Row, bool) {
	for _, r := range rows {
		if r.RemotePort == port {
			return r, true
		}
	}
	return session.Row{}, false
}

func (m Model) selected() (session.Row, bool) {
	if m.cursor < 0 || m.cursor >= len(m.snap.Rows) {
		return session.Row{}, false
	}
	return m.snap.Rows[m.cursor], true
}

// reframe recomputes the geometry and keeps the cursor on screen.
func (m *Model) reframe() {
	m.clampCursor()

	rowsHeight := m.rowsHeight()
	if m.cursor < m.top {
		m.top = m.cursor
	}
	if rowsHeight > 0 && m.cursor >= m.top+rowsHeight {
		m.top = m.cursor - rowsHeight + 1
	}
	if m.top < 0 {
		m.top = 0
	}

	count := len(m.snap.Rows) - m.top
	if count > rowsHeight {
		count = rowsHeight
	}
	if count < 0 {
		count = 0
	}

	showMsg := false
	for i := m.top; i < m.top+count; i++ {
		if m.snap.Rows[i].Err != "" || !m.snap.Rows[i].Present {
			showMsg = true
			break
		}
	}
	m.frame = computeFrame(m.width, m.headerHeight(), count, m.top, showMsg)
}

func (m Model) headerHeight() int {
	if m.height < 7 {
		return 1 // title only
	}
	return 3 // title, blank, column headings
}

// rowsHeight is what is left after the chrome.
func (m Model) rowsHeight() int {
	h := m.height - m.headerHeight() - 1 // detail line
	if m.height >= 10 {
		h-- // footer hints
	}
	if h < 1 {
		h = 1
	}
	return h
}

func (m *Model) clampCursor() {
	if m.cursor >= len(m.snap.Rows) {
		m.cursor = len(m.snap.Rows) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m Model) moveCursor(delta int) Model {
	// No wrapping: htop does not wrap either, and wrapping makes it easy to
	// overshoot onto a tunnel you did not mean to touch.
	m.cursor += delta
	m.reframe()
	return m
}

func (m Model) toggleSelected() (Model, tea.Cmd) {
	r, ok := m.selected()
	if !ok {
		return m, nil
	}
	m.backend.Toggle(r.RemotePort)
	return m, nil
}

func (m Model) toggleAll() (Model, tea.Cmd) {
	anyOff := false
	for _, r := range m.snap.Rows {
		if !r.Wanted {
			anyOff = true
			break
		}
	}
	m.backend.SetAll(anyOff)

	// Say how many rows were affected out loud: "all" is a destructive-feeling
	// action and the user should see its blast radius.
	verb := "desligados"
	if anyOff {
		verb = "ligados"
	}
	text := fmt.Sprintf("%d túneis %s", len(m.snap.Rows), verb)
	return m, func() tea.Msg { return statusMsg{text: text, kind: statusInfo} }
}

func (m Model) openEdit() (Model, tea.Cmd) {
	r, ok := m.selected()
	if !ok {
		return m, nil
	}
	m.mode = modeEdit
	m.edit = editState{remotePort: r.RemotePort, text: strconv.Itoa(r.LocalPort)}
	return m, nil
}

// Quitting is true once the user asked to leave, so the caller knows to print
// the parting line.
func (m Model) Quitting() bool { return m.quitting }
