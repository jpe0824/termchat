package daemon

import (
	"errors"
	"log/slog"
	"maps"
	"slices"
	"sync"

	"github.com/jpe0824/termchat/internal/config"
	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/notify"
	"github.com/jpe0824/termchat/internal/store"
)

// notifiedMemory is how many recently notified messages are remembered, so
// the same message arriving by a live push and by a catch-up at once notifies
// once.
const notifiedMemory = 1024

// errHandleUnknown: the user's own handle is not known yet, so nothing can
// tell whether a message is their own.
var errHandleUnknown = errors.New("the user's handle is not known yet")

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
	ByMsgID(conv, msgID string) (store.Message, error)
	ConvView(conv string) (store.ConvView, error)
}

// noteServer is the part of the IPC server the notification path uses.
type noteServer interface {
	Subscribers() []ipc.Subscriber
	// BroadcastNotify sends ev to the attached UIs that can show it and
	// returns how many it reached.
	BroadcastNotify(ev ipc.Event) int
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

	clock   Clock
	retries *retryQueue

	claims claims

	// mu guards every catchUp's fields.
	mu sync.Mutex
}

func newNotifications(cfg *config.Config, n Notifier, srv noteServer, st noteStore, log *slog.Logger, clock Clock) *notifications {
	return &notifications{cfg: cfg, n: n, srv: srv, st: st, log: log, clock: clock,
		retries: newRetryQueue(clock, log), claims: claims{held: map[msgKey]bool{}}}
}

// pass is what one evaluation of newly stored messages reads once and
// shares: the subscribers as they were when it began, and each
// conversation's view.
type pass struct {
	subs  []ipc.Subscriber
	views map[string]store.ConvView
}

func (n *notifications) newPass() *pass {
	return &pass{subs: n.srv.Subscribers(), views: map[string]store.ConvView{}}
}

// view returns conv's view, read once per pass. A failed read is not kept, so
// the next message of conv reads it again.
func (n *notifications) view(r *pass, conv string) (store.ConvView, error) {
	if v, ok := r.views[conv]; ok {
		return v, nil
	}
	v, err := n.st.ConvView(conv)
	if err == nil {
		r.views[conv] = v
	}
	return v, err
}

// anyRoute reports whether some route could deliver a notification now. When
// none can, nothing is read from the store on its behalf.
func (n *notifications) anyRoute(r *pass) bool {
	c := n.cfg.Notify
	return c.OS != config.OSOff || c.Hook != "" || c.Terminal && slices.ContainsFunc(r.subs, func(s ipc.Subscriber) bool { return s.CanNotify })
}

// focused reports whether an attached UI shows conv in a focused window.
func (r *pass) focused(conv string) bool {
	return slices.ContainsFunc(r.subs, func(s ipc.Subscriber) bool { return s.UI && s.Focused && s.Conv == conv })
}

// mode says how newly stored messages reach the user.
type mode struct {
	// batch, when set, is the catch-up the messages arrived in: each
	// conversation notifies at most once for it, with its newest message
	// that qualifies. Otherwise each message notifies on its own.
	batch *catchUp
	// silent: none of the messages notifies.
	silent bool
}

// livePush is the mode of a message pushed while connected.
var livePush = mode{}

// catchUp is one catch-up's once-per-conversation bookkeeping, so waking from
// sleep produces one notification per conversation however many messages
// arrived. Its fields are guarded by the notifications' mu.
type catchUp struct {
	open bool
	// best is, per conversation, the newest message so far that qualifies;
	// it is raised when the catch-up ends.
	best map[string]candidate
	// notified holds the conversations the catch-up has notified. A message
	// of it retried later notifies only for a conversation not in it.
	notified map[string]bool
}

type candidate struct {
	seq int64
	p   ipc.NotifyPayload
}

func newCatchUp() *catchUp {
	return &catchUp{open: true, best: map[string]candidate{}, notified: map[string]bool{}}
}

// endCatchUp raises b's notifications, one per conversation.
func (n *notifications) endCatchUp(b *catchUp) {
	n.mu.Lock()
	b.open = false
	convs := slices.Sorted(maps.Keys(b.best))
	ps := make([]ipc.NotifyPayload, len(convs))
	for i, c := range convs {
		ps[i] = b.best[c].p
		b.notified[c] = true
	}
	b.best = nil
	n.mu.Unlock()
	for _, p := range ps {
		n.dispatch(p)
	}
}

// consider raises a notification for each message other people posted in
// added, the events the store has just newly stored, that qualifies, as m
// says.
func (n *notifications) consider(added []model.Event, m mode) {
	if m.silent {
		return
	}
	// With the handle unset own messages cannot be told apart; they are
	// candidates anyway and wait for a retry.
	me := n.st.Me()
	var evs []model.Event
	seen := map[msgKey]bool{}
	for _, ev := range added {
		k := msgKey{ev.ConvID, ev.MsgID}
		if ev.Kind != model.MessagePosted || me != "" && ev.Actor == me || ev.MsgID == "" || seen[k] {
			continue
		}
		seen[k] = true
		evs = append(evs, ev)
	}
	n.evaluateAll(evs, m.batch, 0)
}

// evaluateAll evaluates evs, newest first, and raises or batches the
// notifications that qualify. retries is how many retries evs have had.
func (n *notifications) evaluateAll(evs []model.Event, b *catchUp, retries int) {
	if len(evs) == 0 {
		return
	}
	r := n.newPass()
	if !n.anyRoute(r) {
		return
	}
	for i := len(evs) - 1; i >= 0; i-- {
		ev := evs[i]
		if b != nil && n.covered(b, ev) {
			// Claimed so that a live push racing the catch-up cannot notify
			// for it separately.
			n.claims.claim(msgKey{ev.ConvID, ev.MsgID})
			continue
		}
		p, ok, err := n.evaluate(ev, r)
		switch {
		case err != nil:
			n.retries.failed(ev, b, retries, err)
		case ok:
			n.place(b, ev, p)
		}
	}
}

// covered reports whether ev cannot notify for b because b already holds a
// newer message of its conversation, or raised its conversation's one.
func (n *notifications) covered(b *catchUp, ev model.Event) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !b.open {
		return b.notified[ev.ConvID]
	}
	cur, ok := b.best[ev.ConvID]
	return ok && cur.seq >= ev.Seq
}

// place raises p, which ev qualified for, at once for a live push, as its
// conversation's candidate for an open catch-up, and for one that has ended
// only when it notified nothing for that conversation.
func (n *notifications) place(b *catchUp, ev model.Event, p ipc.NotifyPayload) {
	if b == nil {
		n.dispatch(p)
		return
	}
	n.mu.Lock()
	switch {
	case b.open:
		if cur, ok := b.best[ev.ConvID]; !ok || ev.Seq > cur.seq {
			b.best[ev.ConvID] = candidate{ev.Seq, p}
		}
		n.mu.Unlock()
		return
	case b.notified[ev.ConvID]:
		n.mu.Unlock()
		return
	}
	b.notified[ev.ConvID] = true
	n.mu.Unlock()
	n.dispatch(p)
}

// evaluate decides ev once: it reports whether ev notifies, and with what,
// or an error when the policy could not be evaluated. A message decided
// before, here or on another goroutine, does not notify again.
func (n *notifications) evaluate(ev model.Event, r *pass) (ipc.NotifyPayload, bool, error) {
	k := msgKey{ev.ConvID, ev.MsgID}
	if n.claims.has(k) {
		return ipc.NotifyPayload{}, false, nil
	}
	p, ok, err := n.decide(ev, r)
	if err != nil {
		return ipc.NotifyPayload{}, false, err
	}
	if !n.claims.claim(k) {
		return ipc.NotifyPayload{}, false, nil
	}
	return p, ok, nil
}

// claims remembers the last notifiedMemory messages whose policy was
// evaluated, oldest overwritten first.
type claims struct {
	mu   sync.Mutex
	held map[msgKey]bool
	ring [notifiedMemory]msgKey
	next int // the ring slot to fill next
}

// has reports whether k's policy has been evaluated already.
func (c *claims) has(k msgKey) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.held[k]
}

// claim records that k's policy was evaluated and reports whether this call
// was the first to do so.
func (c *claims) claim(k msgKey) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.held[k] {
		return false
	}
	if len(c.held) == notifiedMemory {
		delete(c.held, c.ring[c.next])
	}
	c.held[k] = true
	c.ring[c.next] = k
	c.next = (c.next + 1) % notifiedMemory
	return true
}

// decide applies the notification policy to the stored message ev announced.
// The message is read only when what the event and the conversation tell
// cannot rule it out, and then once. It returns an error when the policy
// could not be evaluated.
func (n *notifications) decide(ev model.Event, r *pass) (ipc.NotifyPayload, bool, error) {
	me := n.st.Me()
	if me == "" {
		return ipc.NotifyPayload{}, false, errHandleUnknown
	}
	v, err := n.view(r, ev.ConvID)
	if err != nil {
		return ipc.NotifyPayload{}, false, err
	}
	ctx := notify.Context{Me: me, RoomMode: n.cfg.Rooms[v.ID], Unread: ev.Seq > v.ReadSeq, Focused: r.focused(v.ID)}
	// The event cannot tell whether the message has since been deleted, or
	// edited to mention the user, so this check assumes neither.
	maybe := store.Message{ConvID: ev.ConvID, Author: ev.Actor, Seq: ev.Seq, Mentions: []string{me}}
	if !notify.ShouldNotify(maybe, v.Conversation, ctx) {
		return ipc.NotifyPayload{}, false, nil
	}
	m, err := n.st.ByMsgID(ev.ConvID, ev.MsgID)
	if err != nil {
		return ipc.NotifyPayload{}, false, err
	}
	ctx.Unread = m.Seq > v.ReadSeq
	if !notify.ShouldNotify(m, v.Conversation, ctx) {
		return ipc.NotifyPayload{}, false, nil
	}
	return payload(m, v.Conversation, me), true, nil
}

// dispatch sends p to the attached UIs that can show it when it may,
// otherwise to the operating system when it may, and independently runs the
// hook.
func (n *notifications) dispatch(p ipc.NotifyPayload) {
	cfg := n.cfg.Notify
	shown := cfg.Terminal && n.srv.BroadcastNotify(ipc.Event{Kind: ipc.KindNotify, Notify: &p}) > 0
	if !shown && cfg.OS != config.OSOff {
		n.n.OS(p)
	}
	if cfg.Hook != "" {
		n.n.Hook(p)
	}
}
