package daemon

import (
	"errors"

	"github.com/jpe0824/termchat/internal/connector"
)

// A gap is a conversation whose contiguous run of held events stops below an
// event the daemon knows the server has: a live event arrived past events this
// device was never sent, the server's answer to a change named a later Seq
// than is held, or the store failed to write an event. Each is recorded here
// and repaired by the supervise goroutine with a sync, never by drain.

// checkGap queues a repair when conv is not held contiguously up to seq. It
// reads only the store and never blocks, so drain may call it.
func (l *ledger) checkGap(conv string, seq int64) {
	if conv == "" || seq < 1 || l.st.Cursor(conv) >= seq {
		return
	}
	l.mu.Lock()
	if seq > l.gaps[conv] {
		l.gaps[conv] = seq
	}
	l.mu.Unlock()
	signal(l.gapCh)
}

// repairGaps syncs every queued conversation. It reports true when some are
// still incomplete, because the store or the server failed, and should be
// tried again later. While not online it drops the queue: the catch-up after
// the next connect syncs every conversation from its cursor.
func (a *account) repairGaps() (retry bool) {
	a.mu.Lock()
	gaps, online := a.gaps, a.online
	a.gaps = map[string]int64{}
	a.mu.Unlock()
	if !online {
		return false
	}
	for conv, want := range gaps {
		if a.st.Cursor(conv) >= want {
			continue
		}
		done, err := a.repairConv(conv, want)
		if errors.Is(err, connector.ErrOffline) || a.ctx.Err() != nil {
			return false
		}
		if err != nil {
			a.log.Warn("gap repair", "conv", conv, "err", err)
		}
		if !done {
			a.mu.Lock()
			a.gaps[conv] = max(a.gaps[conv], want)
			a.mu.Unlock()
			retry = true
		}
	}
	return retry
}

// repairConv fetches what conv is missing and reports whether it is now held
// up to want, or no longer the user's. A conversation with nothing held gets
// its newest page, as on first sync.
func (a *account) repairConv(conv string, want int64) (bool, error) {
	if a.st.OldestSeq(conv) == 0 {
		evs, err := a.conn.History(a.ctx, conv, 0, historyPage)
		if errors.Is(err, connector.ErrNotFound) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		batch := newCatchUp()
		a.nt.consider(a.storeHistory(conv, evs), mode{batch: batch})
		a.nt.endCatchUp(batch)
		a.emitConv(conv)
		signal(a.bfWake)
	} else if err := a.syncConv(a.ctx, conv); err != nil {
		return false, err
	}
	if _, listed := a.view(conv); !listed {
		return true, nil
	}
	return a.st.Cursor(conv) >= want, nil
}
