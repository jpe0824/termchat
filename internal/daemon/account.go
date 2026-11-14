package daemon

import (
	"context"
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
//     and never calls a connector request method, so the connector is never
//     held up waiting for it;
//   - supervise, which owns the catch-up after each connect and the people
//     refresh;
//   - backfill, which fetches older history at a bounded rate;
//   - one sender per conversation with queued messages, started on demand
//     and gone once that conversation's queue is empty.
type account struct {
	d        *daemon
	name     string
	conn     connector.Connector
	st       *store.Store
	ownStore bool
	log      *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// wmu serialises store writes in this process, so a writer waits its
	// turn in order instead of in SQLite's busy handler, whose growing sleeps
	// would let a run of backfill writes hold up a live update. Batches are
	// written in small chunks for the same reason.
	wmu sync.Mutex

	stateCh  chan struct{} // connection state may have changed
	peopleCh chan struct{} // an event named an unknown handle
	bfWake   chan struct{} // backfill has new work

	mu sync.Mutex
	// online is true once connected and caught up; senders and backfill
	// wait for it. changed is closed and replaced whenever it or state moves.
	state     connector.ConnState
	me        model.Person // as the connector last reported it
	online    bool
	changed   chan struct{}
	syncing   bool
	backfill  bool
	openRooms []model.Conversation
	// listing is the open-rooms refresh in flight, if any.
	listing *listing
	// listedAt is when openRooms was last read from the server.
	listedAt time.Time
	known    map[string]bool
	// Outbox senders: workers holds conversations with a running sender;
	// dirty records a kick that arrived while it ran.
	workers  map[string]bool
	dirty    map[string]bool
	requeued map[string]time.Time
	// Backfill: floor is the oldest Seq known to be fetched per conversation,
	// which may be below the store's OldestSeq when an event was rejected.
	floor    map[string]int64
	priority string
}

func newAccount(d *daemon, name string, conn connector.Connector, st *store.Store, own bool) *account {
	ctx, cancel := context.WithCancel(context.Background())
	return &account{
		d: d, name: name, conn: conn, st: st, ownStore: own, log: d.log.With("account", name),
		ctx: ctx, cancel: cancel,
		stateCh: make(chan struct{}, 1), peopleCh: make(chan struct{}, 1), bfWake: make(chan struct{}, 1),
		state: connector.StateOffline, me: conn.Me(), changed: make(chan struct{}),
		known: map[string]bool{}, workers: map[string]bool{}, dirty: map[string]bool{},
		requeued: map[string]time.Time{}, floor: map[string]int64{},
	}
}

func (a *account) start() {
	if people, err := a.st.People(); err == nil {
		a.learn(people)
	}
	a.goRun(func() { _ = a.conn.Run(a.ctx) })
	updates := a.conn.Updates()
	a.goRun(func() { a.drain(updates) })
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

func (a *account) write(fn func() error) error {
	a.wmu.Lock()
	defer a.wmu.Unlock()
	return fn()
}

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

func (a *account) setSyncing(v bool) {
	a.mu.Lock()
	moved := a.syncing != v
	a.syncing = v
	a.mu.Unlock()
	if moved {
		a.emitStatus()
	}
}

func (a *account) setBackfilling(v bool) {
	a.mu.Lock()
	moved := a.backfill != v
	a.backfill = v
	a.mu.Unlock()
	if moved {
		a.emitStatus()
	}
}

func (a *account) status() ipc.Status {
	s := ipc.Status{Account: a.name}
	if items, err := a.st.Pending(); err == nil {
		s.Pending = len(items)
	}
	// Cached values only: status is reached from the drain goroutine, which
	// must not call into the connector.
	a.mu.Lock()
	s.Me, s.Conn = a.me, a.state
	s.Syncing = a.syncing || a.backfill
	a.mu.Unlock()
	return s
}

func (a *account) emitStatus() {
	s := a.status()
	a.d.emit(ipc.Event{Kind: ipc.KindStatus, Status: &s})
}

// view returns the user's conversation conv as listed.
func (a *account) view(conv string) (store.ConvView, bool) {
	views, err := a.st.Conversations()
	if err != nil {
		a.log.Error("list conversations", "err", err)
		return store.ConvView{}, false
	}
	i := slices.IndexFunc(views, func(v store.ConvView) bool { return v.ID == conv })
	if i < 0 {
		return store.ConvView{}, false
	}
	return views[i], true
}

func (a *account) emitConv(conv string) { a.emitConvs([]string{conv}) }

// emitConvs announces the current view of each listed conversation, reading
// the list once.
func (a *account) emitConvs(convs []string) {
	if len(convs) == 0 {
		return
	}
	views, err := a.st.Conversations()
	if err != nil {
		a.log.Error("list conversations", "err", err)
		return
	}
	for _, v := range views {
		if slices.Contains(convs, v.ID) {
			a.d.emit(ipc.Event{Kind: ipc.KindConv, Conv: &v})
		}
	}
}

func (a *account) emitRemoved(conv string) {
	a.d.emit(ipc.Event{Kind: ipc.KindConv, Conv: &store.ConvView{Conversation: model.Conversation{ID: conv}}, Removed: true})
}

func (a *account) emitMessage(conv, msgID string) {
	m, err := a.st.ByMsgID(conv, msgID)
	if err != nil {
		return
	}
	a.d.emit(ipc.Event{Kind: ipc.KindMessage, Message: &m})
}

// learn records handles the store knows a person for.
func (a *account) learn(people []model.Person) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, p := range people {
		a.known[p.Handle] = true
	}
}

func (a *account) unknown(handles ...string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, h := range handles {
		if h != "" && !a.known[h] {
			return true
		}
	}
	return false
}

// listing is one refresh of the open-rooms list that concurrent requests share.
type listing struct {
	done chan struct{}
	err  error
}

// refreshOpenRooms re-reads the open rooms the user has not joined from the
// server. The server does not announce a new open room to non-members, so
// requests that need the list ask for it. Concurrent calls share one server
// request. On failure the cached list stays. It must not run on the goroutine
// that drains the connector's updates. Each call asks the server; callers
// that can use a slightly old list check openRoomsStale first.
func (a *account) refreshOpenRooms(ctx context.Context) error {
	a.mu.Lock()
	if l := a.listing; l != nil {
		a.mu.Unlock()
		select {
		case <-l.done:
			return l.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	l := &listing{done: make(chan struct{})}
	a.listing = l
	a.mu.Unlock()

	lctx, cancel := context.WithTimeout(ctx, chatsRefreshTimeout)
	_, open, err := a.conn.Conversations(lctx)
	cancel()

	a.mu.Lock()
	if err == nil {
		a.openRooms, a.listedAt = open, a.d.clock.Now()
	}
	a.listing = nil
	a.mu.Unlock()
	l.err = err
	close(l.done)
	return err
}

// openRoomsStale reports whether the cached list is older than the window.
func (a *account) openRoomsStale() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.d.clock.Now().Sub(a.listedAt) >= chatsRefreshAfter
}

// connected reports whether the connector is connected right now.
func (a *account) connected() bool { return a.conn.State() == connector.StateConnected }

const (
	// chatsRefreshAfter is how long a successful open-rooms refresh answers
	// Chats requests before the next one asks the server again.
	chatsRefreshAfter = 5 * time.Second
	// chatsRefreshTimeout bounds one refresh, so a slow server cannot hold
	// up a request that has a cached answer.
	chatsRefreshTimeout = 3 * time.Second
)
