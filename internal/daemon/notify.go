package daemon

import (
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"github.com/jpe0824/termchat/internal/config"
	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/notify"
	"github.com/jpe0824/termchat/internal/sanitize"
	"github.com/jpe0824/termchat/internal/store"
)

const (
	// notifyTextMax is the most runes of message text a notification carries.
	notifyTextMax = 200
	// notifiedMemory is how many recently notified messages are remembered, so
	// the same message arriving by a live push and by a catch-up at once
	// notifies once.
	notifiedMemory = 1024
)

// Notifier raises the notification routes the daemon does not handle itself.
// Both methods must return at once: they are called from the goroutine that
// applies connector updates, so anything slow belongs on the notifier's own
// goroutine.
type Notifier interface {
	// OS posts p through the operating system.
	OS(p ipc.NotifyPayload)
	// Hook runs the user's hook command for p, when one is configured.
	Hook(p ipc.NotifyPayload)
}

type nopNotifier struct{}

func (nopNotifier) OS(ipc.NotifyPayload)   {}
func (nopNotifier) Hook(ipc.NotifyPayload) {}

type msgKey struct{ conv, msgID string }

// notifications decides which stored messages notify the user and where the
// notification goes. It runs on whichever goroutine stores the message and
// only reads the store and hands off to the Notifier.
type notifications struct {
	cfg *config.Config
	n   Notifier
	srv *ipc.Server
	st  *store.Store
	log *slog.Logger

	mu       sync.Mutex
	notified map[msgKey]bool
	order    []msgKey
}

func newNotifications(d *daemon, st *store.Store, log *slog.Logger) *notifications {
	return &notifications{cfg: d.cfg, n: d.notifier, srv: d.srv, st: st, log: log, notified: map[msgKey]bool{}}
}

// catchUpBatch holds, per conversation, the newest message of a catch-up
// that qualifies, so waking from sleep produces one notification per
// conversation however many messages arrived.
type catchUpBatch struct {
	best map[string]candidate
}

type candidate struct {
	seq int64
	p   ipc.NotifyPayload
}

func newCatchUpBatch() *catchUpBatch { return &catchUpBatch{best: map[string]candidate{}} }

// flush raises the batch's notifications.
func (b *catchUpBatch) flush(n *notifications) {
	convs := make([]string, 0, len(b.best))
	for c := range b.best {
		convs = append(convs, c)
	}
	slices.Sort(convs)
	for _, c := range convs {
		n.dispatch(b.best[c].p)
	}
}

// unseen returns the messages other people posted in evs that the store does
// not hold yet. It must run before evs are applied.
func (n *notifications) unseen(evs []model.Event) []model.Event {
	me := n.st.Me()
	if me == "" {
		return nil
	}
	var out []model.Event
	seen := map[msgKey]bool{}
	for _, ev := range evs {
		k := msgKey{ev.ConvID, ev.MsgID}
		if ev.Kind != model.MessagePosted || ev.Actor == me || ev.MsgID == "" || seen[k] {
			continue
		}
		seen[k] = true
		if _, err := n.st.ByMsgID(ev.ConvID, ev.MsgID); errors.Is(err, store.ErrNotFound) {
			out = append(out, ev)
		}
	}
	return out
}

// consider raises a notification for each of evs, which the store has just
// accepted as new messages, that qualifies: at once when b is nil, otherwise
// the newest per conversation into b.
func (n *notifications) consider(evs []model.Event, b *catchUpBatch) {
	var fresh []model.Event
	for _, ev := range evs {
		if n.claim(msgKey{ev.ConvID, ev.MsgID}) {
			fresh = append(fresh, ev)
		}
	}
	for i := len(fresh) - 1; i >= 0; i-- {
		ev := fresh[i]
		if b != nil {
			if cur, ok := b.best[ev.ConvID]; ok && cur.seq >= ev.Seq {
				continue
			}
		}
		p, ok := n.decide(ev)
		switch {
		case !ok:
		case b == nil:
			n.dispatch(p)
		default:
			b.best[ev.ConvID] = candidate{ev.Seq, p}
		}
	}
}

// claim reports whether k has not been notified or considered before.
func (n *notifications) claim(k msgKey) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.notified[k] {
		return false
	}
	n.notified[k] = true
	n.order = append(n.order, k)
	if len(n.order) > notifiedMemory {
		delete(n.notified, n.order[0])
		n.order = n.order[1:]
	}
	return true
}

// decide applies the notification policy to the stored message ev announced.
func (n *notifications) decide(ev model.Event) (ipc.NotifyPayload, bool) {
	m, err := n.st.ByMsgID(ev.ConvID, ev.MsgID)
	if err != nil {
		return ipc.NotifyPayload{}, false
	}
	v, err := n.st.ConvView(ev.ConvID)
	if err != nil {
		return ipc.NotifyPayload{}, false
	}
	me := n.st.Me()
	ctx := notify.Context{Me: me, RoomMode: n.cfg.Rooms[v.ID], Unread: m.Seq > v.ReadSeq}
	for _, s := range n.srv.Subscribers() {
		if !s.UI {
			continue
		}
		ctx.TUIAttached = true
		if s.Focused && s.Conv == v.ID {
			ctx.FocusedConv = v.ID
		}
	}
	if !notify.ShouldNotify(m, v.Conversation, ctx) {
		return ipc.NotifyPayload{}, false
	}
	return payload(m, v.Conversation, me), true
}

// payload describes m for a notification.
func payload(m store.Message, conv model.Conversation, me string) ipc.NotifyPayload {
	p := ipc.NotifyPayload{
		ConvID: conv.ID, ConvName: conv.Name, DM: conv.Kind == model.ConvDM,
		Sender:  oneLine(cmpOr(m.AuthorName, m.Author)),
		Text:    shorten(oneLine(m.Text), notifyTextMax),
		Mention: slices.Contains(m.Mentions, me),
	}
	if p.DM {
		p.ConvName = ""
		for _, h := range conv.Members {
			if h != me {
				p.ConvName = h
				break
			}
		}
	}
	p.ConvName = oneLine(p.ConvName)
	return p
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// oneLine removes terminal control sequences and collapses all whitespace,
// line breaks included, to single spaces.
func oneLine(s string) string { return strings.Join(strings.Fields(sanitize.Text(s)), " ") }

// shorten cuts s to max runes, marking the cut with an ellipsis.
func shorten(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// dispatch sends p to the attached UIs when it may, otherwise to the
// operating system when it may, and independently runs the hook.
func (n *notifications) dispatch(p ipc.NotifyPayload) {
	cfg := n.cfg.Notify
	ui := slices.ContainsFunc(n.srv.Subscribers(), func(s ipc.Subscriber) bool { return s.UI })
	switch {
	case ui && cfg.Terminal:
		n.srv.BroadcastUI(ipc.Event{Kind: ipc.KindNotify, Notify: &p})
	case cfg.OS:
		n.n.OS(p)
	}
	if cfg.Hook != "" {
		n.n.Hook(p)
	}
}
