package daemon

import (
	"errors"
	"slices"
	"time"

	"github.com/jpe0824/termchat/internal/connector"
	"github.com/jpe0824/termchat/internal/model"
)

const (
	// backfillInterval spaces History requests: at most two a second across
	// all conversations.
	backfillInterval = 500 * time.Millisecond
	// backfillMaxBackoff caps the doubling after rate limiting or errors.
	backfillMaxBackoff = 5
)

// backfillLoop fetches older history, one page at a time, round-robin across
// conversations, oldest-first, while online. A conversation the user is
// reading goes first. It runs on its own goroutine and only competes with
// live updates for the store.
func (a *account) backfillLoop() {
	next := ""
	backoff := 0
	for {
		if !a.waitOnline(nil) {
			return
		}
		conv, floor, ok := a.nextBackfill(next)
		a.setBackfilling(ok)
		if !ok {
			select {
			case <-a.bfWake:
				continue
			case <-a.ctx.Done():
				return
			}
		}
		select {
		case <-a.d.clock.After(backfillInterval << backoff):
		case <-a.ctx.Done():
			return
		}
		if on, _ := a.isOnline(); !on {
			continue
		}
		evs, err := a.conn.History(a.ctx, conv, floor, historyPage)
		switch {
		case err == nil:
			backoff = 0
			a.storeHistory(conv, evs)
			a.emitConv(conv)
			next = conv
		case errors.Is(err, connector.ErrOffline):
		case errors.Is(err, connector.ErrNotFound), errors.Is(err, connector.ErrForbidden):
			a.setFloor(conv, 1) // no longer readable; nothing more to fetch
		default:
			if !errors.Is(err, connector.ErrRateLimited) {
				a.log.Warn("history request failed", "conv", conv, "err", err)
			}
			backoff = min(backoff+1, backfillMaxBackoff)
			next = conv
		}
	}
}

// nextBackfill picks the conversation to fetch next: the one being read, if
// it still has older history, else the first after prev in id order.
func (a *account) nextBackfill(prev string) (conv string, floor int64, ok bool) {
	a.mu.Lock()
	prio := a.priority
	a.mu.Unlock()
	if prio != "" {
		if f := a.floorOf(prio); f > 1 {
			return prio, f, true
		}
		a.mu.Lock()
		if a.priority == prio {
			a.priority = ""
		}
		a.mu.Unlock()
	}
	ids, err := a.st.ConvIDs()
	if err != nil {
		a.log.Error("list conversations", "err", err)
		return "", 0, false
	}
	start, _ := slices.BinarySearch(ids, prev)
	if start < len(ids) && ids[start] == prev {
		start++
	}
	for i := range ids {
		id := ids[(start+i)%len(ids)]
		if f := a.floorOf(id); f > 1 {
			return id, f, true
		}
	}
	return "", 0, false
}

// floorOf is the oldest Seq fetched for conv; 0 when nothing is held.
func (a *account) floorOf(conv string) int64 {
	oldest := a.st.OldestSeq(conv)
	a.mu.Lock()
	defer a.mu.Unlock()
	if f, ok := a.floor[conv]; ok && (oldest == 0 || f < oldest) {
		return f
	}
	return oldest
}

func (a *account) setFloor(conv string, seq int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if f, ok := a.floor[conv]; !ok || seq < f {
		a.floor[conv] = seq
	}
}

// storeHistory applies a History page for conv and records how far back it
// reached. An empty page means there is nothing older.
func (a *account) storeHistory(conv string, evs []model.Event) {
	if len(evs) == 0 {
		a.setFloor(conv, 1)
		return
	}
	lowest := evs[0].Seq
	for _, ev := range evs {
		lowest = min(lowest, ev.Seq)
	}
	prior := a.floorOf(conv)
	a.applyIn(evs, historyPage)
	if prior > 0 && lowest >= prior {
		lowest = 1 // the page reached no further back; stop rather than repeat it
	}
	a.setFloor(conv, max(lowest, 1))
}

// prioritize moves conv to the front of the backfill.
func (a *account) prioritize(conv string) {
	a.mu.Lock()
	a.priority = conv
	a.mu.Unlock()
	signal(a.bfWake)
}
