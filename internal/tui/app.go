package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/notify"
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

	chatsRead, threadRead, outboxRead flight
	olderBusy                         bool

	drafts map[string]*compose
	note   *notice
	noteT  timer
	// enterConv is the conversation an enter waits to act in, "" for none.
	enterConv string
	enterT    timer

	// termFocus is the terminal's focus. It stays unknown until the terminal
	// sends a focus or blur event, which some terminals and multiplexers
	// never do.
	termFocus termFocus
	// focusUntrusted ignores the terminal's focus events, so focus stays
	// unknown. Herdr sends focus in when a pane is clicked but no focus out
	// when the person switches to another app, which would leave the open
	// conversation marked read while nobody looks at it.
	focusUntrusted bool
	// inputSince says a key or mouse input arrived after the newest message
	// in the open conversation: with focus unknown, that is the evidence
	// someone is looking.
	inputSince bool
	quitting   bool
	focusSent  *focusReport
	focusBusy  bool
	readBusy   map[string]bool
	// readOpen and readNew say the open conversation is owed a MarkRead
	// for being opened, and for a message that arrived in it.
	readOpen, readNew bool
	// tm is the key timing that tells pastes from deliberate presses.
	tm timing
	// preview says the open conversation was put on screen by the
	// interface, not chosen: it is not marked read and does not hold the
	// keyboard until the user acts on it.
	preview bool

	// ov is the open overlay, nil for none; ovGen numbers them.
	ov    *overlay
	ovGen int
	// people is the people list of the current connection, and byHandle
	// the same people by handle.
	people     []model.Person
	byHandle   map[string]model.Person
	peopleBusy bool
	// openRooms are the open rooms the user has not joined.
	openRooms []model.Conversation
	// pendingCursor is a conversation the chat list's cursor goes to once
	// the list holds it.
	pendingCursor string
	helpTop       int
	helpLaid      struct {
		width int
		lines []string
	}
	// osc is the notification escape sequence variant, "" for none.
	osc    string
	flashT timer
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
		splashDone: !opts.Splash, drafts: map[string]*compose{},
		readBusy: map[string]bool{}, osc: notify.ResolveOSC(opts.NotifyOSC, opts.Env),
		focusUntrusted: opts.Env("HC_TERMINAL_ID") != "",
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
	sup := &supervisor{b: m.b, opts: m.opts, adopt: m.adopt, canNotify: m.osc != ""}
	ctx := m.ctx
	return func() tea.Msg { go sup.run(ctx); return nil }
}

// Update handles msg, then reports focus and marks read if either is due.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := m.update(msg)
	return m, tea.Batch(cmd, m.syncFocus(), m.syncRead())
}

func (m *Model) update(msg tea.Msg) tea.Cmd {
	switch msg.(type) {
	case tea.KeyPressMsg, tea.PasteMsg, tea.MouseClickMsg, tea.MouseWheelMsg:
		m.inputSince = true
	}
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
		if !m.focusUntrusted {
			m.termFocus = focusIn
		}
	case tea.BlurMsg:
		if !m.focusUntrusted {
			m.termFocus = focusOut
		}
	case timerMsg:
		return m.onTimer(msg)
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
			m.setPeople(nil)
			m.peopleBusy = false
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
		return m.onEvents([]ipc.Event{msg.ev})
	case eventsMsg:
		return m.onEvents(msg.evs)
	case peopleMsg:
		m.onPeople(msg)
	case ovDoneMsg:
		return m.onOvDone(msg)
	case searchMsg:
		m.onSearch(msg)
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
	var cursor *tea.Cursor
	switch {
	case m.splashShown():
		content = m.splash()
	case !m.fits():
		content = tooSmall[:min(len(tooSmall), max(m.w, 0))]
	default:
		f := m.layFrame()
		content, cursor = m.frame(f), m.inputCursor(f)
	}
	v := tea.NewView(m.withBanner(content))
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.ReportFocus = true
	v.WindowTitle = "termchat"
	v.Cursor = cursor
	return v
}

func (m *Model) splashShown() bool { return m.noAccount || !m.splashDone }

// open shows conv in the thread pane, scrolled to its newest message.
func (m *Model) open(conv string) tea.Cmd { return m.openSeek(conv, 0) }

// openSeek is open; a seq other than 0 is a message the first read of a
// newly opened thread must reach.
func (m *Model) openSeek(conv string, seq int64) tea.Cmd {
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
	if seq != 0 {
		m.thread.jump, m.thread.want = seq, m.jumpWant(conv, seq)
	}
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

// flight is a read of which one is out at a time; a request meanwhile
// queues exactly one more.
type flight struct{ busy, again bool }

// begin reports whether the read may go out now; otherwise it is queued.
func (f *flight) begin() bool {
	if f.busy {
		f.again = true
		return false
	}
	f.busy = true
	return true
}

// end marks the read back and reports whether another was queued.
func (f *flight) end() (again bool) {
	again = f.again
	f.busy, f.again = false, false
	return again
}

// stale reports whether a result came from a connection since replaced,
// or, for conv not "", for a thread no longer open.
func (m *Model) stale(b Backend, conv string) bool {
	return b != m.b || conv != "" && (m.thread == nil || m.thread.conv != conv)
}

// convName is how a conversation is labelled: "# room" or the DM's peer.
func (m *Model) convName(c model.Conversation) string {
	if c.Kind == model.ConvRoom {
		return "# " + sanitize.Line(c.Name)
	}
	return sanitize.Line(c.Peer(m.me()))
}

// convRef is how a conversation is named inside a sentence: "#room", or
// "@peer" for a DM.
func (m *Model) convRef(c model.Conversation) string {
	if c.Kind == model.ConvRoom {
		return "#" + sanitize.Line(c.Name)
	}
	return "@" + sanitize.Line(c.Peer(m.me()))
}
