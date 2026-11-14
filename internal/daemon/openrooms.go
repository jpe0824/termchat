package daemon

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/store"
)

const (
	// chatsRefreshAfter is how long a successful open-rooms refresh answers
	// Chats requests before the next one asks the server again.
	chatsRefreshAfter = 5 * time.Second
	// chatsRefreshTimeout bounds one refresh, so a slow server cannot hold
	// up a request that has a cached answer.
	chatsRefreshTimeout = 3 * time.Second
)

// openRooms caches the open rooms the user has not joined. The server does
// not announce a new open room to non-members, so requests that need the
// list ask for it. Concurrent refreshes share one server request, and a
// failed one keeps the cached list and its age. A refresh calls the
// connector, so it must not run on the drain goroutine.
type openRooms struct {
	clock Clock
	fetch func(ctx context.Context) ([]model.Conversation, error)

	mu    sync.Mutex
	rooms []model.Conversation
	at    time.Time // when rooms was last read from the server
	// inflight is the refresh in progress, if any.
	inflight *listing
}

// listing is one refresh of the list that concurrent requests share.
type listing struct {
	done chan struct{}
	err  error
}

// set records rooms as just read from the server.
func (o *openRooms) set(rooms []model.Conversation) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.rooms, o.at = rooms, o.clock.Now()
}

// list returns the cached rooms.
func (o *openRooms) list() []model.Conversation {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.rooms)
}

// refreshOlder refreshes the list when it was read maxAge ago or more. A
// failed refresh leaves the cached list as the answer.
func (o *openRooms) refreshOlder(ctx context.Context, maxAge time.Duration) {
	o.mu.Lock()
	stale := o.clock.Now().Sub(o.at) >= maxAge
	o.mu.Unlock()
	if stale {
		_ = o.refresh(ctx)
	}
}

// notMine returns the cached rooms that are not among mine.
func (o *openRooms) notMine(mine []store.ConvView) []model.Conversation {
	var out []model.Conversation
	for _, c := range o.list() {
		if !slices.ContainsFunc(mine, func(v store.ConvView) bool { return v.ID == c.ID }) {
			out = append(out, c)
		}
	}
	return out
}

// refresh reads the list from the server, or waits for the read already in
// progress.
func (o *openRooms) refresh(ctx context.Context) error {
	o.mu.Lock()
	if l := o.inflight; l != nil {
		o.mu.Unlock()
		select {
		case <-l.done:
			return l.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	l := &listing{done: make(chan struct{})}
	o.inflight = l
	o.mu.Unlock()

	lctx, cancel := context.WithTimeout(ctx, chatsRefreshTimeout)
	rooms, err := o.fetch(lctx)
	cancel()

	o.mu.Lock()
	if err == nil {
		o.rooms, o.at = rooms, o.clock.Now()
	}
	o.inflight = nil
	o.mu.Unlock()
	l.err = err
	close(l.done)
	return err
}
