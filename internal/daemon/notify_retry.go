package daemon

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/jpe0824/termchat/internal/model"
)

// maxPending bounds the messages waiting for a retry.
const maxPending = 64

// retryDelays are the waits before the first, second and third retry of a
// message whose policy could not be evaluated.
var retryDelays = []time.Duration{time.Second, 5 * time.Second, 30 * time.Second}

// pendingMsg is a message whose policy could not be evaluated. It holds
// identifiers only, never text; a retry reads the message again.
type pendingMsg struct {
	ev      model.Event
	batch   *catchUp // the catch-up it arrived in, if any
	retries int      // retries already made
	due     time.Time
}

// retryQueue holds the messages waiting for a retry, at most maxPending and
// each message once, in memory only. One worker drains it on the injected
// clock.
type retryQueue struct {
	clock Clock
	log   *slog.Logger
	wake  chan struct{} // items changed

	mu    sync.Mutex
	items []pendingMsg // oldest first
}

func newRetryQueue(clock Clock, log *slog.Logger) *retryQueue {
	return &retryQueue{clock: clock, log: log, wake: make(chan struct{}, 1)}
}

// failed records ev, whose policy could not be evaluated after retries
// retries, for the next retry, or gives it up after the last. The oldest
// record goes when the queue is full.
func (q *retryQueue) failed(ev model.Event, b *catchUp, retries int, err error) {
	if retries >= len(retryDelays) {
		q.log.Warn("gave up evaluating notification policy", "conv", ev.ConvID)
		return
	}
	q.log.Warn("cannot evaluate notification policy", "conv", ev.ConvID, "err", err)
	p := pendingMsg{
		ev:    model.Event{ConvID: ev.ConvID, MsgID: ev.MsgID, Seq: ev.Seq, Kind: ev.Kind, Actor: ev.Actor},
		batch: b, retries: retries, due: q.clock.Now().Add(retryDelays[retries]),
	}
	q.mu.Lock()
	if slices.ContainsFunc(q.items, func(o pendingMsg) bool { return o.ev.ConvID == p.ev.ConvID && o.ev.MsgID == p.ev.MsgID }) {
		q.mu.Unlock()
		return
	}
	var dropped *pendingMsg
	if len(q.items) >= maxPending {
		d := q.items[0]
		dropped = &d
		q.items = q.items[1:]
	}
	q.items = append(q.items, p)
	q.mu.Unlock()
	if dropped != nil {
		q.log.Warn("dropped pending notification: too many waiting", "conv", dropped.ev.ConvID)
	}
	signal(q.wake)
}

// untilNext is how long until the earliest record falls due; false when
// there is none.
func (q *retryQueue) untilNext() (time.Duration, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return 0, false
	}
	next := q.items[0].due
	for _, p := range q.items {
		if p.due.Before(next) {
			next = p.due
		}
	}
	return max(next.Sub(q.clock.Now()), 0), true
}

// takeDue removes and returns the records that are due.
func (q *retryQueue) takeDue() []pendingMsg {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.clock.Now()
	var due []pendingMsg
	q.items = slices.DeleteFunc(q.items, func(p pendingMsg) bool {
		if p.due.After(now) {
			return false
		}
		due = append(due, p)
		return true
	})
	return due
}

// run retries pending messages when they fall due, on one goroutine, until
// ctx ends. Pending records end with the process.
func (n *notifications) run(ctx context.Context) {
	for {
		var timer <-chan time.Time
		if wait, ok := n.retries.untilNext(); ok {
			timer = n.clock.After(wait)
		}
		select {
		case <-ctx.Done():
			return
		case <-n.retries.wake:
		case <-timer:
			n.retryDue(ctx)
		}
	}
}

// retryDue evaluates every pending message that is due. Each evaluation
// reads the message and its conversation afresh, so a message read, deleted
// or shown in a focused UI meanwhile is not notified.
func (n *notifications) retryDue(ctx context.Context) {
	for _, p := range n.retries.takeDue() {
		if ctx.Err() != nil {
			return
		}
		n.evaluateAll([]model.Event{p.ev}, p.batch, p.retries+1)
	}
}
