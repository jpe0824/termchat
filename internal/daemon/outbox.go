package daemon

import (
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jpe0824/termchat/internal/connector"
	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/store"
)

const (
	// outboxExpiry: an item not sent this long after it was queued (or
	// requeued) is failed with reason "expired" rather than sent late.
	outboxExpiry = 10 * time.Minute
	maxTextBytes = 8192
	maxBackoff   = 30 * time.Second
	maxReasonLen = 200
	formatText   = "text/plain"
	reasonExpire = "expired"
)

// newMsgID returns a random (version 4) UUID.
func newMsgID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

var mentionRE = regexp.MustCompile(`(?:^|[^A-Za-z0-9_@.])@([A-Za-z0-9][A-Za-z0-9_-]*)`)

// mentions returns the members named by @handle tokens in text, matched
// case-insensitively, in order of first mention, without repeats.
func mentions(text string, members []string) []string {
	var out []string
	for _, m := range mentionRE.FindAllStringSubmatch(text, -1) {
		i := slices.IndexFunc(members, func(h string) bool { return strings.EqualFold(h, m[1]) })
		if i >= 0 && !slices.Contains(out, members[i]) {
			out = append(out, members[i])
		}
	}
	return out
}

func checkText(text string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("%w: the message is empty", ipc.ErrInvalid)
	}
	if len(text) > maxTextBytes {
		return fmt.Errorf("%w: the message is %d bytes, over the limit of %d", ipc.ErrInvalid, len(text), maxTextBytes)
	}
	return nil
}

func textBody(text string) model.Body { return model.Body{Format: formatText, Data: []byte(text)} }

// enqueue writes the outbox row, announces it and wakes the conversation's
// sender. The row is durable before the caller hears of the message id.
func (a *account) enqueue(conv, text, source string) (string, error) {
	v, ok := a.view(conv)
	if !ok {
		return "", fmt.Errorf("%w: conversation %s", ipc.ErrNotFound, conv)
	}
	it := store.OutboxItem{
		MsgID: newMsgID(), ConvID: conv, Text: text, Mentions: mentions(text, v.Members),
		Source: source, CreatedAt: a.d.clock.Now(),
	}
	if err := a.write(func() error { return a.st.Enqueue(it) }); err != nil {
		return "", err
	}
	it.State = store.StatusSending
	a.emitOutbox(it)
	a.kick(conv)
	return it.MsgID, nil
}

func (a *account) emitOutbox(it store.OutboxItem) {
	m, err := a.st.ByMsgID(it.ConvID, it.MsgID)
	if err != nil {
		// Discarded: show the item as gone.
		m = store.Message{ConvID: it.ConvID, MsgID: it.MsgID, Deleted: true}
	}
	a.d.emit(ipc.Event{Kind: ipc.KindOutbox, Message: &m, Outbox: &it})
	a.emitStatus()
}

// kick makes sure conv's sender runs and looks at its queue again.
func (a *account) kick(conv string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ctx.Err() != nil {
		return
	}
	if a.workers[conv] {
		a.dirty[conv] = true
		return
	}
	a.workers[conv] = true
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.sendLoop(conv)
	}()
}

func (a *account) kickAll() {
	items, err := a.st.Pending()
	if err != nil {
		a.log.Error("read outbox", "err", err)
		return
	}
	var convs []string
	for _, it := range items {
		if !slices.Contains(convs, it.ConvID) {
			convs = append(convs, it.ConvID)
		}
	}
	for _, c := range convs {
		a.kick(c)
	}
}

// nextItem is conv's oldest item still to be sent.
func (a *account) nextItem(conv string) (store.OutboxItem, bool, error) {
	items, err := a.st.Pending()
	if err != nil {
		return store.OutboxItem{}, false, err
	}
	for _, it := range items {
		if it.ConvID == conv {
			return it, true, nil
		}
	}
	return store.OutboxItem{}, false, nil
}

// expiresAt is when it stops being worth sending.
func (a *account) expiresAt(it store.OutboxItem) time.Time {
	base := it.CreatedAt
	a.mu.Lock()
	if t, ok := a.requeued[it.MsgID]; ok && t.After(base) {
		base = t
	}
	a.mu.Unlock()
	return base.Add(outboxExpiry)
}

// finish ends conv's sender unless a kick arrived since it last looked.
func (a *account) finish(conv string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.dirty[conv] {
		return false
	}
	delete(a.workers, conv)
	return true
}

// sendLoop delivers conv's queue in creation order. Each conversation has
// its own, so one stuck conversation never holds up another.
func (a *account) sendLoop(conv string) {
	failures := 0
	for a.ctx.Err() == nil {
		a.mu.Lock()
		a.dirty[conv] = false
		a.mu.Unlock()
		it, ok, err := a.nextItem(conv)
		if err != nil {
			a.log.Error("read outbox", "err", err)
			failures++
			a.pause(backoff(failures))
			continue
		}
		if !ok {
			if a.finish(conv) {
				return
			}
			continue
		}
		expiry := a.expiresAt(it)
		if a.d.clock.Now().After(expiry) {
			a.fail(it, reasonExpire)
			failures = 0
			continue
		}
		if !a.waitOnline(a.d.clock.After(expiry.Sub(a.d.clock.Now()) + time.Millisecond)) {
			continue // ended or expired; the loop top decides
		}
		ev, err := a.conn.Send(a.ctx, conv, it.MsgID, textBody(it.Text), it.Mentions)
		switch {
		case err == nil:
			failures = 0
			a.delivered(it, ev)
		case errors.Is(err, connector.ErrOffline):
			// Never sent, or sent before the connection dropped: either way
			// the same id is resent after reconnecting and cannot duplicate.
			a.pause(time.Second)
		case errors.Is(err, connector.ErrUnknownOutcome):
			failures++
			a.log.Warn("send outcome unknown; resending the same message id", "conv", conv, "msg", it.MsgID)
			a.pause(backoff(failures - 1))
		case permanent(err):
			a.fail(it, reason(err))
		default:
			failures++
			if !errors.Is(err, connector.ErrRateLimited) {
				a.log.Warn("send failed; will retry", "conv", conv, "msg", it.MsgID, "err", err)
			}
			a.pause(backoff(failures))
		}
	}
}

// backoff is 0 for n <= 0, then 1s doubling up to maxBackoff.
func backoff(n int) time.Duration {
	if n <= 0 {
		return 0
	}
	return min(time.Second<<min(n-1, 10), maxBackoff)
}

// pause waits d on the clock, cut short by a connection change or shutdown.
func (a *account) pause(d time.Duration) {
	_, changed := a.isOnline()
	select {
	case <-a.d.clock.After(d):
	case <-changed:
	case <-a.ctx.Done():
	}
}

func permanent(err error) bool {
	for _, e := range []error{connector.ErrForbidden, connector.ErrNotFound, connector.ErrInvalid,
		connector.ErrTooLarge, connector.ErrNotAuthorised} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

func reason(err error) string {
	r := err.Error()
	if len(r) > maxReasonLen {
		r = strings.ToValidUTF8(r[:maxReasonLen], "")
	}
	return r
}

// delivered stores the server's event for a sent item. The outbox row goes
// whether or not the event could be stored: the server has the message, and
// a later sync brings it.
func (a *account) delivered(it store.OutboxItem, ev model.Event) {
	if len(a.apply([]model.Event{ev})) == 0 {
		a.log.Warn("sent message's event was rejected by the store", "conv", it.ConvID, "msg", it.MsgID)
	}
	if err := a.write(func() error { return a.st.Discard(it.MsgID) }); err != nil && !errors.Is(err, store.ErrNotFound) {
		a.log.Error("clear sent outbox item", "msg", it.MsgID, "err", err)
	}
	a.mu.Lock()
	delete(a.requeued, it.MsgID)
	a.mu.Unlock()
	a.announce([]model.Event{ev}, true)
	a.emitStatus()
}

func (a *account) fail(it store.OutboxItem, why string) {
	if err := a.write(func() error { return a.st.MarkFailed(it.MsgID, why) }); err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			a.log.Error("mark outbox item failed", "msg", it.MsgID, "err", err)
		}
		return
	}
	a.log.Info("message not sent", "conv", it.ConvID, "msg", it.MsgID, "reason", why)
	it.State, it.Err = store.StatusFailed, why
	a.emitOutbox(it)
}

// retry requeues a failed item.
func (a *account) retry(msgID string) error {
	if err := a.write(func() error { return a.st.Requeue(msgID) }); err != nil {
		return err
	}
	a.mu.Lock()
	a.requeued[msgID] = a.d.clock.Now()
	a.mu.Unlock()
	it, ok := a.findItem(msgID)
	if !ok {
		return nil // sent or discarded meanwhile
	}
	a.emitOutbox(it)
	a.kick(it.ConvID)
	return nil
}

func (a *account) discard(msgID string) error {
	it, found := a.findItem(msgID)
	if err := a.write(func() error { return a.st.Discard(msgID) }); err != nil {
		return err
	}
	a.mu.Lock()
	delete(a.requeued, msgID)
	a.mu.Unlock()
	if found {
		a.emitOutbox(it)
	}
	return nil
}

// findItem looks msgID up among every conversation's outbox.
func (a *account) findItem(msgID string) (store.OutboxItem, bool) {
	views, err := a.st.Conversations()
	if err != nil {
		return store.OutboxItem{}, false
	}
	for _, v := range views {
		items, err := a.st.Outbox(v.ID)
		if err != nil {
			continue
		}
		for _, it := range items {
			if it.MsgID == msgID {
				return it, true
			}
		}
	}
	return store.OutboxItem{}, false
}
