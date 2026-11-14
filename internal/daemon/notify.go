package daemon

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

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
	// notifyNameMax is the most runes of a sender or conversation name.
	notifyNameMax = 64
	// notifiedMemory is how many recently notified messages are remembered, so
	// the same message arriving by a live push and by a catch-up at once
	// notifies once.
	notifiedMemory = 1024
	// maxPending bounds the messages waiting for a retry.
	maxPending = 64
)

// retryDelays are the waits before the first, second and third retry of a
// message whose policy could not be evaluated.
var retryDelays = []time.Duration{time.Second, 5 * time.Second, 30 * time.Second}

// errHandleUnknown: the user's own handle is not known yet, so nothing can
// tell whether a message is their own.
var errHandleUnknown = errors.New("the user's handle is not known yet")

// pendingMsg is a message whose policy could not be evaluated. It holds
// identifiers only, never text; a retry reads the message again.
type pendingMsg struct {
	ev      model.Event
	retries int // retries already made
	due     time.Time
}

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

// noteStore is the part of the store the notification path reads.
type noteStore interface {
	Me() string
	HasEvent(conv string, seq int64) bool
	ByMsgID(conv, msgID string) (store.Message, error)
	ConvView(conv string) (store.ConvView, error)
}

// noteServer is the part of the IPC server the notification path uses.
type noteServer interface {
	Subscribers() []ipc.Subscriber
	BroadcastNotify(ipc.Event)
}

// notifications decides which stored messages notify the user and where the
// notification goes. It runs on whichever goroutine stores the message and
// only reads the store and hands off to the Notifier.
type notifications struct {
	cfg *config.Config
	n   Notifier
	srv noteServer
	st  noteStore
	log *slog.Logger

	clock Clock
	wake  chan struct{} // pending changed

	mu       sync.Mutex
	notified map[msgKey]bool
	order    []msgKey
	pending  []pendingMsg // oldest first
}

func newNotifications(cfg *config.Config, n Notifier, srv noteServer, st noteStore, log *slog.Logger, clock Clock) *notifications {
	return &notifications{cfg: cfg, n: n, srv: srv, st: st, log: log, clock: clock,
		wake: make(chan struct{}, 1), notified: map[msgKey]bool{}}
}

// canShowInTerminal reports whether an attached UI declared that it can show
// a notification written to its terminal.
func (n *notifications) canShowInTerminal() bool {
	return slices.ContainsFunc(n.srv.Subscribers(), func(s ipc.Subscriber) bool { return s.CanNotify })
}

// anyRoute reports whether some route could deliver a notification now. When
// none can, nothing is read from the store on its behalf.
func (n *notifications) anyRoute() bool {
	c := n.cfg.Notify
	return c.OS || c.Hook != "" || c.Terminal && n.canShowInTerminal()
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
	if !n.anyRoute() {
		return nil
	}
	// With the handle unset own messages cannot be told apart; they are
	// candidates anyway and wait for a retry in consider.
	me := n.st.Me()
	var out []model.Event
	seen := map[msgKey]bool{}
	for _, ev := range evs {
		k := msgKey{ev.ConvID, ev.MsgID}
		if ev.Kind != model.MessagePosted || me != "" && ev.Actor == me || ev.MsgID == "" || seen[k] {
			continue
		}
		seen[k] = true
		if !n.st.HasEvent(ev.ConvID, ev.Seq) {
			out = append(out, ev)
		}
	}
	return out
}

// consider raises a notification for each of evs, which the store has just
// accepted as new messages, that qualifies: at once when b is nil, otherwise
// the newest per conversation into b.
func (n *notifications) consider(evs []model.Event, b *catchUpBatch) {
	if !n.anyRoute() {
		return
	}
	for i := len(evs) - 1; i >= 0; i-- {
		ev := evs[i]
		k := msgKey{ev.ConvID, ev.MsgID}
		if b != nil {
			if cur, ok := b.best[ev.ConvID]; ok && cur.seq >= ev.Seq {
				// Older than the batch's pick: claimed so that a live push
				// racing the catch-up cannot notify for it separately.
				n.claim(k)
				continue
			}
		}
		if n.decided(k) {
			continue
		}
		p, ok, err := n.decide(ev)
		if err != nil {
			n.log.Warn("cannot evaluate notification policy", "conv", ev.ConvID, "err", err)
			n.enqueue(ev)
			continue
		}
		if !n.claim(k) {
			continue // another goroutine decided it first
		}
		switch {
		case !ok:
		case b == nil:
			n.dispatch(p)
		default:
			b.best[ev.ConvID] = candidate{ev.Seq, p}
		}
	}
}

// decided reports whether k's policy has been evaluated already.
func (n *notifications) decided(k msgKey) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.notified[k]
}

// claim records that k's policy was evaluated and reports whether this call
// was the first to do so.
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

// decide applies the notification policy to the stored message ev announced,
// reading the message and its conversation once each. It returns an error
// when the policy could not be evaluated.
func (n *notifications) decide(ev model.Event) (ipc.NotifyPayload, bool, error) {
	me := n.st.Me()
	if me == "" {
		return ipc.NotifyPayload{}, false, errHandleUnknown
	}
	m, err := n.st.ByMsgID(ev.ConvID, ev.MsgID)
	if err != nil {
		return ipc.NotifyPayload{}, false, err
	}
	v, err := n.st.ConvView(ev.ConvID)
	if err != nil {
		return ipc.NotifyPayload{}, false, err
	}
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
		return ipc.NotifyPayload{}, false, nil
	}
	return payload(m, v.Conversation, me), true, nil
}

// payload describes m for a notification.
func payload(m store.Message, conv model.Conversation, me string) ipc.NotifyPayload {
	p := ipc.NotifyPayload{
		ConvID: conv.ID, ConvName: conv.Name, DM: conv.Kind == model.ConvDM,
		Sender:  shorten(oneLine(cmpOr(m.AuthorName, m.Author)), notifyNameMax),
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
	p.ConvName = shorten(oneLine(p.ConvName), notifyNameMax)
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

// dispatch sends p to the attached UIs that can show it when it may, otherwise to the
// operating system when it may, and independently runs the hook.
func (n *notifications) dispatch(p ipc.NotifyPayload) {
	cfg := n.cfg.Notify
	switch {
	case cfg.Terminal && n.canShowInTerminal():
		n.srv.BroadcastNotify(ipc.Event{Kind: ipc.KindNotify, Notify: &p})
	case cfg.OS:
		n.n.OS(p)
	}
	if cfg.Hook != "" {
		n.n.Hook(p)
	}
}

// enqueue records ev, whose policy could not be evaluated, for a retry. The
// oldest record goes when the queue is full.
func (n *notifications) enqueue(ev model.Event) {
	slim := model.Event{ConvID: ev.ConvID, MsgID: ev.MsgID, Seq: ev.Seq, Kind: ev.Kind}
	n.mu.Lock()
	if slices.ContainsFunc(n.pending, func(p pendingMsg) bool { return p.ev.ConvID == slim.ConvID && p.ev.MsgID == slim.MsgID }) {
		n.mu.Unlock()
		return
	}
	var dropped *model.Event
	if len(n.pending) >= maxPending {
		d := n.pending[0].ev
		dropped = &d
		n.pending = n.pending[1:]
	}
	n.pending = append(n.pending, pendingMsg{ev: slim, due: n.clock.Now().Add(retryDelays[0])})
	n.mu.Unlock()
	if dropped != nil {
		n.log.Warn("dropped pending notification: too many waiting", "conv", dropped.ConvID)
	}
	select {
	case n.wake <- struct{}{}:
	default:
	}
}

// run retries pending messages when they fall due, on one goroutine, until
// ctx ends. Pending records are in memory only and end with the process.
func (n *notifications) run(ctx context.Context) {
	for {
		var timer <-chan time.Time
		if wait, ok := n.untilNextDue(); ok {
			timer = n.clock.After(wait)
		}
		select {
		case <-ctx.Done():
			return
		case <-n.wake:
		case <-timer:
			n.retryDue(ctx)
		}
	}
}

func (n *notifications) untilNextDue() (time.Duration, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.pending) == 0 {
		return 0, false
	}
	next := n.pending[0].due
	for _, p := range n.pending {
		if p.due.Before(next) {
			next = p.due
		}
	}
	return max(next.Sub(n.clock.Now()), 0), true
}

// retryDue evaluates every pending message that is due. Each evaluation
// reads the message and its conversation afresh, so a message read, deleted
// or shown in a focused UI meanwhile is not notified.
func (n *notifications) retryDue(ctx context.Context) {
	n.mu.Lock()
	now := n.clock.Now()
	var due []pendingMsg
	n.pending = slices.DeleteFunc(n.pending, func(p pendingMsg) bool {
		if p.due.After(now) {
			return false
		}
		due = append(due, p)
		return true
	})
	n.mu.Unlock()
	for _, p := range due {
		if ctx.Err() != nil {
			return
		}
		n.retry(p)
	}
}

func (n *notifications) retry(p pendingMsg) {
	k := msgKey{p.ev.ConvID, p.ev.MsgID}
	if n.decided(k) || !n.anyRoute() {
		return
	}
	pl, ok, err := n.decide(p.ev)
	if err != nil {
		if p.retries+1 >= len(retryDelays) {
			n.log.Warn("gave up evaluating notification policy", "conv", p.ev.ConvID)
			return
		}
		n.log.Warn("cannot evaluate notification policy", "conv", p.ev.ConvID, "err", err)
		p.retries++
		p.due = n.clock.Now().Add(retryDelays[p.retries])
		n.mu.Lock()
		n.pending = append(n.pending, p)
		n.mu.Unlock()
		return
	}
	if n.claim(k) && ok {
		n.dispatch(pl)
	}
}
