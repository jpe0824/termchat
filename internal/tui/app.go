package tui

import (
	"context"
	"errors"
	"net"
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

type pane int

const (
	paneSidebar pane = iota
	paneThread
)

// Messages into the update loop. Results carry the backend they came from
// so an answer from a connection that has since been replaced is dropped.
type (
	splashDoneMsg  struct{}
	statusPollMsg  struct{}
	eventMsg       struct{ ev ipc.Event }
	subscribedMsg  struct{ b Backend }
	backendDownMsg struct{ err error }
	resyncMsg      struct{}
	serverMsg      struct{ name string }
	statusMsg      struct {
		b   Backend
		st  ipc.Status
		err error
	}
	chatsMsg struct {
		b     Backend
		convs []ipc.ConvView
		err   error
	}
	threadMsg struct {
		b     Backend
		conv  string
		limit int
		msgs  []ipc.Message
		err   error
	}
	olderMsg struct {
		b      Backend
		conv   string
		before int64
		msgs   []ipc.Message
		err    error
	}
)

// Model is the root of the interface. Its update loop never waits on the
// backend: every call runs as a command and comes back as a message.
type Model struct {
	opts   Options
	st     styles
	ctx    context.Context
	cancel context.CancelFunc
	b      Backend

	w, h       int
	splashDone bool

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
	focus    pane
	showList bool
	help     bool

	chatsBusy, chatsAgain   bool
	threadBusy, threadAgain bool
	olderBusy               bool
}

// New returns the interface's root model. b may be nil when the background
// process could not be reached; with Options.Dial and Options.Send the
// interface keeps trying.
func New(b Backend, opts Options) *Model {
	opts.fill()
	ctx, cancel := context.WithCancel(context.Background())
	return &Model{
		opts: opts, st: newStyles(opts.Env("NO_COLOR") == ""),
		ctx: ctx, cancel: cancel, b: b, procDown: b == nil,
		splashDone: !opts.Splash,
	}
}

// Close stops the subscription goroutine.
func (m *Model) Close() { m.cancel() }

func (m *Model) Init() tea.Cmd {
	var cmds []tea.Cmd
	if m.opts.Splash {
		cmds = append(cmds, m.opts.Tick(splashFor, splashDoneMsg{}))
	}
	if m.opts.Send != nil {
		sup := &supervisor{b: m.b, opts: m.opts}
		ctx := m.ctx
		cmds = append(cmds, func() tea.Msg { go sup.run(ctx); return nil })
	}
	if m.b != nil {
		cmds = append(cmds, m.readStatus(), m.requestChats())
	}
	return tea.Batch(cmds...)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = max(msg.Width, 0), max(msg.Height, 0)
		return m, nil
	case tea.KeyPressMsg:
		return m, m.onKey(msg)
	case tea.MouseClickMsg:
		return m, m.onClick(msg.Mouse())
	case tea.MouseWheelMsg:
		return m, m.onWheel(msg.Mouse())
	case splashDoneMsg:
		m.splashDone = true
	case statusPollMsg:
		m.polling = false
		if m.noAccount && m.b != nil {
			return m, m.readStatus()
		}
	case subscribedMsg:
		m.b, m.procDown = msg.b, false
		return m, m.rereadAll()
	case resyncMsg:
		return m, m.rereadAll()
	case backendDownMsg:
		m.procDown = true
	case serverMsg:
		m.server = msg.name
	case eventMsg:
		return m, m.onEvent(msg.ev)
	case statusMsg:
		if msg.b != m.b {
			return m, nil
		}
		if msg.err != nil {
			return m, m.failed(msg.err)
		}
		return m, m.setStatus(msg.st)
	case chatsMsg:
		return m, m.onChats(msg)
	case threadMsg:
		return m, m.onThread(msg)
	case olderMsg:
		return m, m.onOlder(msg)
	}
	return m, nil
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
	return v
}

func (m *Model) splashShown() bool { return m.noAccount || !m.splashDone }

// failed handles a read error: a gone or stopping background process is
// shown in the top bar while the subscription goroutine reconnects.
func (m *Model) failed(err error) tea.Cmd {
	switch {
	case errors.Is(err, ipc.ErrNoAccount):
		return m.setNoAccount()
	case errors.Is(err, ipc.ErrClosed), errors.Is(err, ipc.ErrShuttingDown):
		m.procDown = true
	}
	return nil
}

// setNoAccount keeps the opening screen up and asks for the status every
// pollEvery until an account appears; one poll is pending at a time.
func (m *Model) setNoAccount() tea.Cmd {
	m.noAccount = true
	if m.polling {
		return nil
	}
	m.polling = true
	return m.opts.Tick(pollEvery, statusPollMsg{})
}

func (m *Model) setStatus(st ipc.Status) tea.Cmd {
	m.status, m.haveStatus = st, true
	if st.Account == "" {
		return m.setNoAccount()
	}
	var cmds []tea.Cmd
	if !m.serverAsk && m.opts.Server != nil {
		m.serverAsk = true
		get := m.opts.Server
		cmds = append(cmds, func() tea.Msg { return serverMsg{serverName(get())} })
	}
	if m.noAccount {
		m.noAccount = false
		cmds = append(cmds, m.requestChats(), m.requestThread())
	}
	return tea.Batch(cmds...)
}

// serverName is the host of a "host:port" address, for display.
func serverName(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		addr = host
	}
	return sanitize.Line(addr)
}

func (m *Model) onEvent(ev ipc.Event) tea.Cmd {
	var conv string
	switch ev.Kind {
	case ipc.KindStatus:
		if ev.Status != nil {
			return m.setStatus(*ev.Status)
		}
		return nil
	case ipc.KindMessage:
		if ev.Message != nil {
			conv = ev.Message.ConvID
		}
	case ipc.KindOutbox:
		if ev.Message != nil {
			conv = ev.Message.ConvID
		} else if ev.Outbox != nil {
			conv = ev.Outbox.ConvID
		}
	case ipc.KindConv:
		if ev.Conv != nil {
			conv = ev.Conv.ID
		}
	default:
		return nil
	}
	cmds := []tea.Cmd{m.requestChats()}
	if conv != "" && conv == m.openID {
		cmds = append(cmds, m.requestThread())
	}
	return tea.Batch(cmds...)
}

// open shows conv in the thread pane, scrolled to its newest message.
func (m *Model) open(conv string) tea.Cmd {
	if i := m.indexOf(conv); i >= 0 {
		m.cursor = i
	}
	m.showList = false
	if m.openID == conv && m.thread != nil {
		return nil
	}
	m.openID = conv
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
