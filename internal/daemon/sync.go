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
	// applyChunk is the most events written in one store transaction.
	applyChunk = 10
	// resyncAfter is how long a failed catch-up waits before trying again.
	resyncAfter = 30 * time.Second
)

// drain applies pushes from the connector. It blocks on nothing but the
// store and the updates channel, and calls no connector method: identity
// and state come from the account's cached copies.
func (a *account) drain(updates <-chan connector.Update) {
	for {
		select {
		case <-a.ctx.Done():
			return
		case u := <-updates:
			a.handleUpdate(u)
		}
	}
}

func (a *account) handleUpdate(u connector.Update) {
	switch {
	case u.Event != nil:
		ev := *u.Event
		a.announce(a.apply([]model.Event{ev}), true)
		if a.unknown(ev.Actor, ev.Target) {
			signal(a.peopleCh)
		}
	case u.Read != nil:
		if err := a.write(func() error { return a.st.SetRead(u.Read.ConvID, u.Read.Seq) }); err != nil {
			a.log.Warn("read position for unknown conversation", "conv", u.Read.ConvID)
			return
		}
		a.emitConv(u.Read.ConvID)
	case u.Conv != nil:
		a.upsertConv(*u.Conv)
	default:
		signal(a.stateCh)
	}
}

// upsertConv stores the server's view of a conversation and announces it.
func (a *account) upsertConv(c model.Conversation) {
	if err := a.write(func() error { return a.st.UpsertConversation(c) }); err != nil {
		a.log.Error("store conversation", "conv", c.ID, "err", err)
		return
	}
	if me := a.st.Me(); me != "" && len(c.Members) > 0 && !slices.Contains(c.Members, me) {
		a.markGone(c.ID)
		return
	}
	a.emitConv(c.ID)
}

func (a *account) markGone(conv string) {
	if err := a.write(func() error { return a.st.MarkGone(conv) }); err != nil {
		a.log.Warn("mark conversation gone", "conv", conv, "err", err)
		return
	}
	a.emitRemoved(conv)
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
	for _, v := range local {
		localRead[v.ID] = v.ReadSeq
	}
	listed := map[string]bool{}
	for _, c := range mine {
		listed[c.ID] = true
		if err := a.write(func() error { return a.st.UpsertConversation(c) }); err != nil {
			return err
		}
	}
	a.mu.Lock()
	a.openRooms = open
	a.mu.Unlock()
	for _, v := range local {
		if !listed[v.ID] {
			a.markGone(v.ID)
		}
	}

	// A conversation with nothing held starts from its newest page; the
	// backfill fetches the rest.
	for _, c := range mine {
		if c.LastSeq == 0 || a.st.OldestSeq(c.ID) != 0 {
			continue
		}
		evs, err := a.conn.History(ctx, c.ID, 0, historyPage)
		if err != nil {
			return err
		}
		a.storeHistory(c.ID, evs)
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
	if err := a.syncCursors(ctx, cursors); err != nil {
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

// syncCursors calls Sync until the server has nothing more. Cursors advance
// past every event returned, stored or not, so a rejected event cannot make
// the loop ask for the same page forever.
func (a *account) syncCursors(ctx context.Context, cursors map[string]int64) error {
	for len(cursors) > 0 {
		evs, more, gone, err := a.conn.Sync(ctx, cursors)
		if err != nil {
			return err
		}
		a.announce(a.apply(evs), len(evs) <= maxMessageEvents)
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
	return a.syncCursors(ctx, map[string]int64{conv: cur})
}

// apply stores events and returns those the store accepted. It writes in
// chunks so a live update waits for one chunk at most. The store rejects a
// whole batch for one malformed event, so a rejected chunk is retried one
// event at a time and the bad ones recorded as skipped, which lets the
// cursor move past them.
func (a *account) apply(evs []model.Event) []model.Event {
	var ok []model.Event
	for chunk := range slices.Chunk(evs, applyChunk) {
		if err := a.write(func() error { return a.st.Apply(chunk...) }); err == nil {
			ok = append(ok, chunk...)
			continue
		}
		for _, ev := range chunk {
			if err := a.write(func() error { return a.st.Apply(ev) }); err != nil {
				a.log.Warn("skipping event the store rejected", "conv", ev.ConvID, "seq", ev.Seq, "kind", ev.Kind, "err", err)
				// Only a malformed event is recorded as skipped; a storage
				// failure leaves the gap so the next sync fetches it again.
				if errors.Is(err, store.ErrInvalid) && ev.ConvID != "" && ev.Seq >= 1 {
					if err := a.write(func() error { return a.st.Skip(ev.ConvID, ev.Seq) }); err != nil {
						a.log.Error("record skipped event", "conv", ev.ConvID, "seq", ev.Seq, "err", err)
					}
				}
				continue
			}
			ok = append(ok, ev)
		}
	}
	return ok
}

// announce tells subscribers about stored events: each message when
// perMessage, and every conversation touched.
func (a *account) announce(evs []model.Event, perMessage bool) {
	var convs []string
	type key struct{ conv, msgID string }
	seen := map[key]bool{}
	own := false
	me := a.st.Me()
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
			a.emitMessage(ev.ConvID, ev.MsgID)
		}
	}
	a.emitConvs(convs)
	if own {
		a.emitStatus()
	}
}
