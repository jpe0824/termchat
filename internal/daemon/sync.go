package daemon

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jpe0824/termchat/internal/connector"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/store"
)

const (
	// historyPage is the number of events fetched per History request.
	historyPage = 50
	// maxMessageEvents bounds the per-message events one batch announces;
	// past it subscribers get one conv event and re-read, so a long catch-up
	// cannot overflow their queues.
	maxMessageEvents = 256
	// applyChunk is the most events of a live push or sync page written in
	// one store transaction. A history page is written whole: it is fetched
	// at a bounded rate, so it holds up a live update at most once per page.
	applyChunk = 10
	// resyncAfter is how long a failed catch-up waits before trying again.
	resyncAfter = 30 * time.Second
)

// drain applies pushes from the connector until ctx ends. It blocks on
// nothing but the store and the updates channel.
func (l *ledger) drain(ctx context.Context, updates <-chan connector.Update) {
	for {
		select {
		case <-ctx.Done():
			return
		case u := <-updates:
			l.handleUpdate(u)
		}
	}
}

func (l *ledger) handleUpdate(u connector.Update) {
	switch {
	case u.Event != nil:
		ev := *u.Event
		l.nt.consider(l.ingest([]model.Event{ev}), livePush)
		if l.unknown(ev.Actor, ev.Target) {
			signal(l.peopleCh)
		}
	case u.Read != nil:
		if err := l.write(func() error { return l.st.SetRead(u.Read.ConvID, u.Read.Seq) }); err != nil {
			l.log.Warn("read position for unknown conversation", "conv", u.Read.ConvID)
			return
		}
		l.emitConv(u.Read.ConvID)
	case u.Conv != nil:
		l.upsertConv(*u.Conv)
	default:
		signal(l.stateCh)
	}
}

// upsertConv stores the server's view of a conversation and announces it.
func (l *ledger) upsertConv(c model.Conversation) {
	if err := l.write(func() error { return l.st.UpsertConversation(c) }); err != nil {
		l.log.Error("store conversation", "conv", c.ID, "err", err)
		return
	}
	if me := l.st.Me(); me != "" && len(c.Members) > 0 && !slices.Contains(c.Members, me) {
		l.markGone(c.ID)
		return
	}
	l.emitConv(c.ID)
}

func (l *ledger) markGone(conv string) {
	if err := l.write(func() error { return l.st.MarkGone(conv) }); err != nil {
		l.log.Warn("mark conversation gone", "conv", conv, "err", err)
		return
	}
	l.emitRemoved(conv)
}

// supervise reacts to connection changes and people refresh requests.
func (a *account) supervise() {
	signal(a.stateCh) // the connector may already be connected
	var retry <-chan time.Time
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-retry:
			retry = nil
			signal(a.stateCh)
		case <-a.stateCh:
			if !a.onState() {
				retry = a.d.clock.After(resyncAfter)
			}
		case <-a.peopleCh:
			if err := a.refreshPeople(); err != nil {
				a.log.Warn("refresh people", "err", err)
			}
		}
	}
}

// onState handles a possible connection change. It reports false when a
// catch-up failed for a reason other than going offline.
func (a *account) onState() bool {
	s := a.conn.State()
	a.setState(s)
	a.emitStatus()
	if s != connector.StateConnected {
		return true
	}
	a.setSyncing(true)
	err := a.catchUp()
	a.setSyncing(false)
	if err != nil && (errors.Is(err, connector.ErrOffline) || a.ctx.Err() != nil) {
		return true // the next state change starts over
	}
	if err != nil {
		a.log.Error("catch-up failed", "err", err)
	}
	// Sending is safe even after a failed catch-up: message ids make it
	// idempotent, and the retry fills in what was missed.
	a.setOnline()
	a.kickAll()
	signal(a.bfWake)
	a.emitStatus()
	return err == nil
}

func (a *account) refreshPeople() error {
	people, err := a.conn.People(a.ctx)
	if err != nil {
		return err
	}
	if err := a.write(func() error { return a.st.UpsertPeople(people) }); err != nil {
		return err
	}
	a.learn(people)
	return nil
}

// catchUp brings the store level with the server after a connect.
func (a *account) catchUp() error {
	ctx := a.ctx
	me := a.conn.Me()
	a.mu.Lock()
	a.me = me
	a.mu.Unlock()
	if err := a.write(func() error { return a.st.SetMe(me.Handle) }); err != nil {
		return err
	}
	if err := a.refreshPeople(); err != nil {
		return err
	}
	mine, open, err := a.conn.Conversations(ctx)
	if err != nil {
		return err
	}
	local, err := a.st.Conversations()
	if err != nil {
		return err
	}
	localRead := map[string]int64{}
	var heldBefore []string
	for _, v := range local {
		localRead[v.ID] = v.ReadSeq
		heldBefore = append(heldBefore, v.ID)
	}
	listed := map[string]bool{}
	for _, c := range mine {
		listed[c.ID] = true
		if err := a.write(func() error { return a.st.UpsertConversation(c) }); err != nil {
			return err
		}
	}
	a.rooms.set(open)
	for _, v := range local {
		if !listed[v.ID] {
			a.markGone(v.ID)
		}
	}

	// A conversation with nothing held starts from its newest page; the
	// backfill fetches the rest. When the account has synced before, that
	// first fill of a conversation new to this device may notify, once, like
	// any other catch-up; the first catch-up on a device never does, so the
	// whole backlog is not announced.
	batch := newCatchUp()
	defer a.nt.endCatchUp(batch)
	firstEver := len(local) == 0
	for _, c := range mine {
		if c.LastSeq == 0 || a.st.OldestSeq(c.ID) != 0 {
			continue
		}
		evs, err := a.conn.History(ctx, c.ID, 0, historyPage)
		if err != nil {
			return err
		}
		fill := mode{batch: batch, silent: firstEver || slices.Contains(heldBefore, c.ID)}
		a.nt.consider(a.storeHistory(c.ID, evs), fill)
	}

	cursors := map[string]int64{}
	for _, c := range mine {
		cur := a.st.Cursor(c.ID)
		if cur == 0 && a.st.OldestSeq(c.ID) != 0 {
			// Events are held, so 0 is a failed query, not "from the start".
			a.log.Error("cursor unavailable; skipping conversation this sync", "conv", c.ID)
			continue
		}
		cursors[c.ID] = cur
	}
	if err := a.syncCursors(ctx, cursors, mode{batch: batch}); err != nil {
		return err
	}

	for _, c := range mine {
		if r := localRead[c.ID]; r > c.ReadSeq {
			if err := a.conn.MarkRead(ctx, c.ID, r); err != nil {
				a.log.Warn("push read position", "conv", c.ID, "err", err)
			}
		}
	}
	ids := make([]string, len(mine))
	for i, c := range mine {
		ids[i] = c.ID
	}
	a.emitConvs(ids)
	return nil
}

// syncCursors calls Sync until the server has nothing more, storing what it
// returns and notifying as m says. Cursors advance past every event returned,
// stored or not, so a rejected event cannot make the loop ask for the same
// page forever.
func (a *account) syncCursors(ctx context.Context, cursors map[string]int64, m mode) error {
	for len(cursors) > 0 {
		evs, more, gone, err := a.conn.Sync(ctx, cursors)
		if err != nil {
			return err
		}
		a.nt.consider(a.ingest(evs), m)
		for _, ev := range evs {
			if _, ok := cursors[ev.ConvID]; ok && ev.Seq > cursors[ev.ConvID] {
				cursors[ev.ConvID] = ev.Seq
			}
		}
		for _, g := range gone {
			delete(cursors, g)
			a.markGone(g)
		}
		if !more || len(evs) == 0 {
			return nil
		}
	}
	return nil
}

// syncConv catches up one conversation.
func (a *account) syncConv(ctx context.Context, conv string) error {
	cur := a.st.Cursor(conv)
	if cur == 0 && a.st.OldestSeq(conv) != 0 {
		return errors.New("cursor unavailable")
	}
	batch := newCatchUp()
	defer a.nt.endCatchUp(batch)
	return a.syncCursors(ctx, map[string]int64{conv: cur}, mode{batch: batch})
}

// apply stores events and returns those the store accepted and, of those,
// the ones it had not held before. It writes in chunks so a live update
// waits for one chunk at most.
func (l *ledger) apply(evs []model.Event) (accepted, added []model.Event) {
	return l.applyIn(evs, applyChunk)
}

// applyIn is apply with chunks of size events. The store rejects a whole
// batch for one malformed event, so a rejected chunk is retried one event at
// a time and the bad ones recorded as skipped, which lets the cursor move
// past them.
func (l *ledger) applyIn(evs []model.Event, size int) (accepted, added []model.Event) {
	put := func(evs ...model.Event) error {
		return l.write(func() error {
			n, err := l.st.Apply(evs...)
			added = append(added, n...)
			return err
		})
	}
	for chunk := range slices.Chunk(evs, size) {
		if err := put(chunk...); err == nil {
			accepted = append(accepted, chunk...)
			continue
		}
		for _, ev := range chunk {
			if err := put(ev); err != nil {
				l.log.Warn("skipping event the store rejected", "conv", ev.ConvID, "seq", ev.Seq, "kind", ev.Kind, "err", err)
				// Only a malformed event is recorded as skipped; a storage
				// failure leaves the gap so the next sync fetches it again.
				if errors.Is(err, store.ErrInvalid) && ev.ConvID != "" && ev.Seq >= 1 {
					if err := l.write(func() error { return l.st.Skip(ev.ConvID, ev.Seq) }); err != nil {
						l.log.Error("record skipped event", "conv", ev.ConvID, "seq", ev.Seq, "err", err)
					}
				}
				continue
			}
			accepted = append(accepted, ev)
		}
	}
	return accepted, added
}

// ingest stores events and announces those the store accepted: each message
// as well when the batch is short enough for subscribers to take one event
// per message. It returns the events the store had not held before, which
// the callers that may notify pass to the notifications: live pushes and
// sync pages. History fetched on request or in the backfill never notifies.
func (l *ledger) ingest(evs []model.Event) []model.Event {
	accepted, added := l.apply(evs)
	l.announce(accepted, len(evs) <= maxMessageEvents)
	return added
}

// announce tells subscribers about stored events: each message when
// perMessage, and every conversation touched.
func (l *ledger) announce(evs []model.Event, perMessage bool) {
	var convs []string
	type key struct{ conv, msgID string }
	seen := map[key]bool{}
	own := false
	me := l.st.Me()
	for _, ev := range evs {
		if !slices.Contains(convs, ev.ConvID) {
			convs = append(convs, ev.ConvID)
		}
		if ev.Kind == model.MessagePosted && ev.Actor == me {
			own = true
		}
		k := key{ev.ConvID, ev.MsgID}
		if perMessage && ev.MsgID != "" && !seen[k] {
			seen[k] = true
			l.emitMessage(ev.ConvID, ev.MsgID)
		}
	}
	l.emitConvs(convs)
	if own {
		l.emitStatus()
	}
}
