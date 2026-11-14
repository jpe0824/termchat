package tui

import (
	"github.com/jpe0824/termchat/internal/ipc"
)

// Messages into the update loop. Results carry the backend they came from
// so an answer from a connection that has since been replaced is dropped.
type (
	splashDoneMsg  struct{}
	statusPollMsg  struct{}
	eventMsg       struct{ ev ipc.Event }
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
	// enterMsg lets an enter act once no input followed it.
	enterMsg         struct{ gen int }
	noticeExpiredMsg struct{ gen int }
	// sentMsg is not tied to a connection: a message queued on one that
	// has since gone is still queued, and its draft is still done.
	sentMsg struct {
		conv, text string
		err        error
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
	markedMsg   struct{ conv string }
	focusSetMsg struct{}
)
