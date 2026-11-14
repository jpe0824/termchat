package tui

import (
	"context"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/sanitize"
)

const (
	splashFor  = 700 * time.Millisecond
	pollEvery  = 2 * time.Second
	pageSize   = 50
	maxReread  = 500
	retryEvery = 2 * time.Second
)

// focusArea is where keys go: the chat list, the thread's message
// selection, or the text input.
type focusArea int

const (
	focusList focusArea = iota
	focusThread
	focusInput
)

// Model is the root of the interface. Its update loop never waits on the
// backend: every call runs as a command and comes back as a message.
type Model struct {
	opts   Options
	st     styles
	ctx    context.Context
	cancel context.CancelFunc
	b      Backend
	adopt  chan Backend

	w, h       int
	splashDone bool
	// splashTimed records that the opening screen's time is up while the
	// launch dial is still out; it closes when the dial returns.
	splashTimed bool
	launching   bool
	starting    bool
	supervised  bool
	startErr    string
	downErr     error

	status     ipc.Status
	haveStatus bool
	procDown   bool
	noAccount  bool
	polling    bool
	server     string
	serverAsk  bool

	convs    []ipc.ConvView // display order: DMs, then rooms
	haveList bool
	cursor   int
	openID   string
	thread   *thread
	focus    focusArea
	showList bool
	help     bool

	chatsBusy, chatsAgain   bool
	threadBusy, threadAgain bool
	olderBusy               bool
	outboxBusy, outboxAgain bool

	drafts  map[string]*compose
	note    *notice
	noteGen int
	// enterConv is the conversation an enter waits to act in, "" for none.
	enterConv string
	enterGen  int

	// termFocused is the terminal's focus: assumed until a blur says not.
	termFocused bool
	quitting    bool
	focusSent   *focusReport
	focusBusy   bool
	readBusy    map[string]bool
	// readOpen and readNew say the open conversation is owed a MarkRead
	// for being opened, and for a message that arrived in it.
	readOpen, readNew bool
	// lastTyped is when a key last typed into the input, to tell a burst
	// of pasted keys from a lone esc or ↑.
	lastTyped time.Time
	// lastKey is when any key last arrived, so a ctrl+c inside a paste is
	// ignored.
	lastKey time.Time
	// exitKind is an esc, ↑ or ctrl+c waiting out the paste gap.
	exitKind  exitKind
	exitGen   int
	exitConv  string
	quitAsked time.Time
	// preview says the open conversation was put on screen by the
	// interface, not chosen: it is not marked read and does not hold the
	// keyboard until the user acts on it.
	preview bool
}

// New returns the interface's root model. With b nil and Options.Start set,
// the first connection is made inside the program, so the opening screen
// draws at once. With Options.Dial and Options.Send it reconnects whenever
// the background process goes away, without ever starting one.
func New(b Backend, opts Options) *Model {
	opts.fill()
	ctx, cancel := context.WithCancel(context.Background())
	return &Model{
		opts: opts, st: newStyles(opts.Env("NO_COLOR") == ""),
		ctx: ctx, cancel: cancel, b: b, procDown: b == nil, adopt: make(chan Backend, 1),
		splashDone: !opts.Splash, drafts: map[string]*compose{}, termFocused: true,
		readBusy: map[string]bool{},
	}
}

// Close stops the subscription goroutine.
func (m *Model) Close() { m.cancel() }

func (m *Model) Init() tea.Cmd {
	var cmds []tea.Cmd
	if m.opts.Splash {
		cmds = append(cmds, m.opts.Tick(splashFor, splashDoneMsg{}))
	}
	if m.b == nil && m.opts.Start != nil {
		m.launching, m.procDown = true, false
		start, ctx := m.opts.Start, m.ctx
		cmds = append(cmds, func() tea.Msg {
			b, err := start(ctx)
			return launchMsg{b: b, err: err}
		})
		return tea.Batch(cmds...)
	}
	cmds = append(cmds, m.supervise())
	if m.b != nil {
		cmds = append(cmds, m.readStatus(), m.requestChats())
	}
	return tea.Batch(cmds...)
}

// supervise starts the subscription goroutine, once.
func (m *Model) supervise() tea.Cmd {
	if m.opts.Send == nil || m.supervised {
		return nil
	}
	m.supervised = true
	sup := &supervisor{b: m.b, opts: m.opts, adopt: m.adopt}
	ctx := m.ctx
	return func() tea.Msg { go sup.run(ctx); return nil }
}

func (m *Model) onLaunch(msg launchMsg) tea.Cmd {
	m.launching = false
	if m.splashTimed {
		m.splashDone = true
	}
	if msg.err != nil || msg.b == nil {
		m.procDown, m.downErr = true, msg.err
		return m.supervise()
	}
	m.b = msg.b
	return tea.Batch(m.supervise(), m.readStatus(), m.requestChats())
}

// startProcess is the user's explicit request to start the background
// process while it is down; reconnects on their own never start it.
func (m *Model) startProcess() tea.Cmd {
	if !m.procDown || m.starting || m.launching || !m.canStart() {
		return nil
	}
	m.starting, m.startErr = true, ""
	start, ctx := m.opts.Start, m.ctx
	return func() tea.Msg {
		b, err := start(ctx)
		return startedMsg{b: b, err: err}
	}
}

// canStart reports whether starting the process could help: not when one
// of another version is running.
func (m *Model) canStart() bool {
	return m.opts.Start != nil && !errors.Is(m.downErr, ipc.ErrVersionMismatch)
}

func (m *Model) onStarted(msg startedMsg) {
	m.starting = false
	if msg.err != nil || msg.b == nil {
		if msg.err == nil {
			msg.err = ipc.ErrClosed
		}
		m.startErr = sanitize.Line(msg.err.Error())
		return
	}
	select {
	case m.adopt <- msg.b:
	default:
		_ = msg.b.Close()
	}
}

// Update handles msg, then reports focus and marks read if either is due.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := m.update(msg)
	return m, tea.Batch(cmd, m.syncFocus(), m.syncRead())
}

func (m *Model) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = max(msg.Width, 0), max(msg.Height, 0)
		if m.preview {
			m.previewFocus()
		}
	case tea.KeyPressMsg:
		return m.onKey(msg)
	case tea.PasteMsg:
		return m.onPaste(msg.Content)
	case tea.MouseClickMsg:
		return m.onClick(msg.Mouse())
	case tea.MouseWheelMsg:
		return m.onWheel(msg.Mouse())
	case tea.FocusMsg:
		m.termFocused = true
	case tea.BlurMsg:
		m.termFocused = false
	case enterMsg:
		return m.onEnterTimer(msg)
	case exitMsg:
		return m.onExitTimer(msg)
	case noticeExpiredMsg:
		m.onNoticeExpired(msg)
	case sentMsg:
		return m.onSent(msg)
	case actionMsg:
		return m.onAction(msg)
	case outboxMsg:
		return m.onOutbox(msg)
	case markedMsg:
		if msg.b == m.b {
			delete(m.readBusy, msg.conv)
		}
	case focusSetMsg:
		if msg.b == m.b {
			m.focusBusy = false
		}
	case splashDoneMsg:
		if m.launching {
			m.splashTimed = true
		} else {
			m.splashDone = true
		}
	case launchMsg:
		return m.onLaunch(msg)
	case startedMsg:
		m.onStarted(msg)
	case statusPollMsg:
		m.polling = false
		if m.noAccount && m.b != nil {
			return m.readStatus()
		}
	case subscribedMsg:
		if msg.b != m.b {
			// A new connection has been told nothing, and calls still out on
			// the old one will not hold up the new one. A resubscribe on the
			// same connection keeps both: it still knows the focus.
			m.focusSent, m.focusBusy = nil, false
			m.readBusy = map[string]bool{}
		}
		m.b, m.procDown, m.downErr, m.startErr = msg.b, false, nil, ""
		return m.rereadAll()
	case resyncMsg:
		return m.rereadAll()
	case backendDownMsg:
		m.procDown, m.downErr = true, msg.err
	case serverMsg:
		m.server = msg.name
	case eventMsg:
		return m.onEvent(msg.ev)
	case statusMsg:
		if msg.b != m.b {
			return nil
		}
		if msg.err != nil {
			return m.failed(msg.err)
		}
		return m.setStatus(msg.st)
	case chatsMsg:
		return m.onChats(msg)
	case threadMsg:
		return m.onThread(msg)
	case olderMsg:
		return m.onOlder(msg)
	}
	return nil
}

func (m *Model) View() tea.View {
	var content string
	if m.splashShown() {
		content = m.splash()
	} else {
		content = m.frame()
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.ReportFocus = true
	v.WindowTitle = "termchat"
	v.Cursor = m.inputCursor()
	return v
}

func (m *Model) splashShown() bool { return m.noAccount || !m.splashDone }

// open shows conv in the thread pane, scrolled to its newest message.
func (m *Model) open(conv string) tea.Cmd {
	if i := m.indexOf(conv); i >= 0 {
		m.cursor = i
	}
	m.showList = false
	m.toInput()
	m.readOpen, m.preview = true, false
	if m.openID == conv && m.thread != nil {
		return nil
	}
	m.openID, m.readNew = conv, false
	m.thread = newThread(conv)
	return m.requestThread()
}

func (m *Model) openConv() (ipc.ConvView, bool) {
	if i := m.indexOf(m.openID); i >= 0 {
		return m.convs[i], true
	}
	return ipc.ConvView{}, false
}

func (m *Model) indexOf(id string) int {
	for i, c := range m.convs {
		if c.ID == id && id != "" {
			return i
		}
	}
	return -1
}

func (m *Model) cursorID() string {
	if m.cursor >= 0 && m.cursor < len(m.convs) {
		return m.convs[m.cursor].ID
	}
	return ""
}

func (m *Model) me() string { return m.status.Me.Handle }

// convName is how a conversation is labelled: "# room" or the DM's peer.
func (m *Model) convName(c model.Conversation) string {
	if c.Kind == model.ConvRoom {
		return "# " + sanitize.Line(c.Name)
	}
	return sanitize.Line(c.Peer(m.me()))
}
