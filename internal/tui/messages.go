package tui

import (
	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
)

// Messages into the update loop. Results carry the backend they came from
// so an answer from a connection that has since been replaced is dropped.
type (
	splashDoneMsg  struct{}
	statusPollMsg  struct{}
	eventMsg       struct{ ev ipc.Event }
	eventsMsg      struct{ evs []ipc.Event }
	subscribedMsg  struct{ b Backend }
	backendDownMsg struct{ err error }
	// launchMsg is the first connection attempt's result; startedMsg is
	// the result of the user asking to start the background process.
	launchMsg struct {
		b   Backend
		err error
	}
	startedMsg struct {
		b   Backend
		err error
	}
	resyncMsg struct{}
	serverMsg struct{ name string }
	statusMsg struct {
		b   Backend
		st  ipc.Status
		err error
	}
	chatsMsg struct {
		b     Backend
		convs []ipc.ConvView
		open  []model.Conversation
		err   error
	}
	// threadMsg is a re-read of the newest limit messages; reach, when not
	// 0, is the seq it was sized to reach.
	threadMsg struct {
		b     Backend
		conv  string
		limit int
		reach int64
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
	// timerMsg is a tick of timer id's arming gen; for the search timer,
	// of overlay ov.
	timerMsg struct {
		id  timerID
		gen timer
		ov  int
	}
	// sentMsg is not tied to a connection: a message queued on one that
	// has since gone is still queued, and its draft is still done. draft
	// is the start of the draft it came from, which text may differ from
	// by a leading /.
	sentMsg struct {
		conv, text, draft string
		err               error
	}
	actionMsg struct {
		b               Backend
		op, conv, msgID string
		err             error
	}
	outboxMsg struct {
		b     Backend
		conv  string
		items []ipc.OutboxItem
		err   error
	}
	markedMsg struct {
		b    Backend
		conv string
	}
	focusSetMsg struct{ b Backend }
	peopleMsg   struct {
		b      Backend
		gen    int
		people []model.Person
		err    error
	}
	// ovDoneMsg is an overlay write's answer, for the overlay gen.
	ovDoneMsg struct {
		b              Backend
		gen            int
		op, done, conv string
		err            error
	}
	// configMsg is a read of the settings, the seq'th asked for.
	configMsg struct {
		b    Backend
		seq  int
		list []ipc.Setting
		err  error
	}
	// settingSavedMsg is a setting change's answer, for the overlay gen.
	settingSavedMsg struct {
		b   Backend
		gen int
		key string
		s   ipc.Setting
		err error
	}
	searchMsg struct {
		b    Backend
		gen  int
		seq  timer
		q    string
		msgs []ipc.Message
		err  error
	}
)
