package daemon

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/jpe0824/termchat/internal/connector"
	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/store"
)

// account is one configured account at work. Its goroutines:
//
//   - the connector's Run, which owns the network connection;
//   - drain, the only reader of conn.Updates(); it writes pushes to the store
//     and runs on the embedded ledger, which has no connector, so it cannot
//     make a connector request and hold the connector up waiting for it;
//   - supervise, which owns the catch-up after each connect and the people
//     refresh;
//   - backfill, which fetches older history at a bounded rate;
//   - one sender per conversation with queued messages, started on demand
//     and gone once that conversation's queue is empty.
type account struct {
	*ledger
	d        *daemon
	conn     connector.Connector
	ownStore bool

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	bfWake chan struct{} // backfill has new work
	rooms  *openRooms

	// The fields below are guarded by the ledger's mu.
	//
	// online is true once connected and caught up; senders and backfill
	// wait for it. changed is closed and replaced whenever it or state moves.
	online  bool
	changed chan struct{}
	// Outbox senders: workers holds conversations with a running sender;
	// dirty records a kick that arrived while it ran.
	workers map[string]bool
	dirty   map[string]bool
	// Backfill: floor is the oldest Seq known to be fetched per conversation,
	// which may be below the store's OldestSeq when an event was rejected.
	floor    map[string]int64
	priority string
}

// ledger is what the drain goroutine works with: the store, the cached
// connection state and identity, and announcing changes to subscribers. It
// holds no connector, so nothing drain reaches can make a connector request.
type ledger struct {
	name string
	st   *store.Store
	log  *slog.Logger
	srv  *ipc.Server
	// nt decides which stored messages notify the user.
	nt *notifications

	// wmu serialises store writes in this process, so a writer waits its
	// turn in order instead of in SQLite's busy handler, whose growing sleeps
	// would let a run of backfill writes hold up a live update. Live pushes
	// and sync pages are written in small chunks for the same reason.
	wmu sync.Mutex

	stateCh  chan struct{} // connection state may have changed
	peopleCh chan struct{} // an event named an unknown handle

	// mu guards the fields below and the account's that say so.
	mu       sync.Mutex
	state    connector.ConnState
	me       model.Person // as the connector last reported it
	syncing  bool
	backfill bool
	known    map[string]bool
}

func newAccount(d *daemon, name string, conn connector.Connector, st *store.Store, own bool) *account {
	ctx, cancel := context.WithCancel(context.Background())
	a := &account{
		ledger: &ledger{
			name: name, st: st, log: d.log.With("account", name), srv: d.srv,
			stateCh: make(chan struct{}, 1), peopleCh: make(chan struct{}, 1),
			state: connector.StateOffline, me: conn.Me(), known: map[string]bool{},
		},
		d: d, conn: conn, ownStore: own,
		ctx: ctx, cancel: cancel,
		bfWake: make(chan struct{}, 1), changed: make(chan struct{}),
		workers: map[string]bool{}, dirty: map[string]bool{}, floor: map[string]int64{},
	}
	a.nt = newNotifications(d.cfg, d.notifier, d.srv, st, a.log)
	a.rooms = &openRooms{clock: d.clock, fetch: func(ctx context.Context) ([]model.Conversation, error) {
		_, open, err := conn.Conversations(ctx)
		return open, err
	}}
	return a
}

func (a *account) start() {
	if people, err := a.st.People(); err == nil {
		a.learn(people)
	}
	a.goRun(func() { _ = a.conn.Run(a.ctx) })
	updates := a.conn.Updates()
	a.goRun(func() { a.drain(a.ctx, updates) })
	a.goRun(a.supervise)
	a.goRun(a.backfillLoop)
	// Queued items are checked for expiry at once, connected or not.
	a.kickAll()
}

func (a *account) goRun(fn func()) {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		fn()
	}()
}

// shutdown stops every goroutine, then closes the store if Run opened it.
func (a *account) shutdown() {
	a.cancel()
	a.wg.Wait()
	if a.ownStore {
		if err := a.st.Close(); err != nil {
			a.log.Error("close store", "err", err)
		}
	}
}

func (l *ledger) write(fn func() error) error {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	return fn()
}

func (l *ledger) emit(ev ipc.Event) { l.srv.Broadcast(ev) }

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// setState records the connection state and ends online whatever the state:
// a connected state seen here may follow a drop that was never observed, so
// senders and backfill wait for the catch-up that follows.
func (a *account) setState(s connector.ConnState) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state = s
	a.online = false
	close(a.changed)
	a.changed = make(chan struct{})
}

// setOnline marks the catch-up done, unless the connection dropped meanwhile
// or another state change is waiting, whose own catch-up comes first.
func (a *account) setOnline() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state != connector.StateConnected || len(a.stateCh) > 0 {
		return
	}
	a.online = true
	close(a.changed)
	a.changed = make(chan struct{})
}

func (a *account) isOnline() (bool, <-chan struct{}) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.online, a.changed
}

// waitOnline blocks until online. It returns false when ctx ends or
// deadline fires first.
func (a *account) waitOnline(deadline <-chan time.Time) bool {
	for {
		on, changed := a.isOnline()
		if on {
			return true
		}
		select {
		case <-changed:
		case <-deadline:
			return false
		case <-a.ctx.Done():
			return false
		}
	}
}

func (l *ledger) setSyncing(v bool) {
	l.mu.Lock()
	moved := l.syncing != v
	l.syncing = v
	l.mu.Unlock()
	if moved {
		l.emitStatus()
	}
}

func (l *ledger) setBackfilling(v bool) {
	l.mu.Lock()
	moved := l.backfill != v
	l.backfill = v
	l.mu.Unlock()
	if moved {
		l.emitStatus()
	}
}

func (l *ledger) status() ipc.Status {
	s := ipc.Status{Account: l.name}
	if items, err := l.st.Pending(); err == nil {
		s.Pending = len(items)
	}
	// Cached values only: the ledger has no connector to ask.
	l.mu.Lock()
	s.Me, s.Conn = l.me, l.state
	s.Syncing = l.syncing || l.backfill
	l.mu.Unlock()
	return s
}

func (l *ledger) emitStatus() {
	if !l.srv.HasSubscribers() {
		return
	}
	s := l.status()
	l.emit(ipc.Event{Kind: ipc.KindStatus, Status: &s})
}

// view returns the user's conversation conv as listed.
func (l *ledger) view(conv string) (store.ConvView, bool) {
	v, err := l.st.ConvView(conv)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			l.log.Error("list conversations", "err", err)
		}
		return store.ConvView{}, false
	}
	return v, true
}

func (l *ledger) emitConv(conv string) { l.emitConvs([]string{conv}) }

// emitConvs announces the current view of each listed conversation, in the
// order Conversations lists them. One is read on its own; several read the
// list once.
func (l *ledger) emitConvs(convs []string) {
	if len(convs) == 0 || !l.srv.HasSubscribers() {
		return
	}
	if len(convs) == 1 {
		if v, ok := l.view(convs[0]); ok {
			l.emit(ipc.Event{Kind: ipc.KindConv, Conv: &v})
		}
		return
	}
	views, err := l.st.Conversations()
	if err != nil {
		l.log.Error("list conversations", "err", err)
		return
	}
	for _, v := range views {
		if slices.Contains(convs, v.ID) {
			l.emit(ipc.Event{Kind: ipc.KindConv, Conv: &v})
		}
	}
}

func (l *ledger) emitRemoved(conv string) {
	l.emit(ipc.Event{Kind: ipc.KindConv, Conv: &store.ConvView{Conversation: model.Conversation{ID: conv}}, Removed: true})
}

func (l *ledger) emitMessage(conv, msgID string) {
	if !l.srv.HasSubscribers() {
		return
	}
	m, err := l.st.ByMsgID(conv, msgID)
	if err != nil {
		return
	}
	l.emit(ipc.Event{Kind: ipc.KindMessage, Message: &m})
}

// learn records handles the store knows a person for.
func (l *ledger) learn(people []model.Person) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, p := range people {
		l.known[p.Handle] = true
	}
}

func (l *ledger) unknown(handles ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, h := range handles {
		if h != "" && !l.known[h] {
			return true
		}
	}
	return false
}

// connected reports whether the connector is connected right now.
func (a *account) connected() bool { return a.conn.State() == connector.StateConnected }
