package tui

import (
	"context"
	"errors"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/jpe0824/termchat/internal/ipc"
)

// supervisor owns the subscription. It turns events into messages for the
// update loop, subscribes again after an overflow, and when the background
// process goes away reports it and dials again every retryEvery until it
// is back. Each new subscription is reported with subscribedMsg, on which
// the interface re-reads everything it shows.
type supervisor struct {
	b     Backend
	opts  Options
	adopt <-chan Backend
	// canNotify says the interface writes notifications to the terminal;
	// renotify carries a new value from the interface, which needs a new
	// subscription to reach the background process.
	canNotify bool
	renotify  <-chan bool
}

// How forward ended.
const (
	fwdStopped  = iota // ctx ended
	fwdEnded           // the subscription ended
	fwdRenotify        // canNotify changed
)

func (s *supervisor) run(ctx context.Context) {
	defer func() {
		if s.b != nil {
			_ = s.b.Close()
		}
	}()
	for ctx.Err() == nil {
		if s.b == nil {
			select {
			case s.b = <-s.adopt:
			default:
				if !s.dial(ctx) {
					continue
				}
			}
		}
		s.takeRenotify()
		subCtx, end := context.WithCancel(ctx)
		ch, err := s.b.SubscribeUI(subCtx, s.canNotify)
		if err != nil {
			end()
			s.drop(ctx, err)
			continue
		}
		s.send(ctx, subscribedMsg{b: s.b})
		how := s.forward(ctx, ch)
		end()
		switch how {
		case fwdStopped:
			return
		case fwdRenotify:
			if !s.finish(ctx, ch) {
				return
			}
			continue
		}
		why := s.b.SubscriptionErr()
		if errors.Is(why, ipc.ErrSubscriptionOverflow) {
			continue
		}
		if why == nil {
			why = ipc.ErrClosed
		}
		s.drop(ctx, why)
	}
}

// forward passes events on until the channel closes, ctx ends or
// canNotify changes. Events already waiting behind one go with it as one
// batch, so a busy conversation costs one update, not one per event.
func (s *supervisor) forward(ctx context.Context, ch <-chan ipc.Event) int {
	ended := func() int {
		if ctx.Err() != nil {
			return fwdStopped
		}
		return fwdEnded
	}
	for {
		select {
		case <-ctx.Done():
			return fwdStopped
		case b := <-s.adopt:
			// Started by the user after this connection came back.
			_ = b.Close()
		case v := <-s.renotify:
			if v != s.canNotify {
				s.canNotify = v
				return fwdRenotify
			}
		case ev, ok := <-ch:
			if !ok {
				return ended()
			}
			evs, open := drain(ch, ev)
			if len(evs) == 1 {
				s.send(ctx, eventMsg{ev: ev})
			} else {
				s.send(ctx, eventsMsg{evs: evs})
			}
			if !open {
				return ended()
			}
		}
	}
}

// takeRenotify takes a canNotify sent while no subscription was being
// forwarded.
func (s *supervisor) takeRenotify() {
	select {
	case v := <-s.renotify:
		s.canNotify = v
	default:
	}
}

// finish waits for an ended subscription's channel to close, passing on
// the events still in it, since a connection takes one subscription at a
// time; it reports false when ctx ended first.
func (s *supervisor) finish(ctx context.Context, ch <-chan ipc.Event) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case ev, ok := <-ch:
			if !ok {
				return true
			}
			s.send(ctx, eventMsg{ev: ev})
		}
	}
}

// maxBatch bounds one batch, so a long run of waiting events reaches the
// update loop as several messages rather than one of any size.
const maxBatch = 256

// drain is first and up to maxBatch-1 events waiting behind it on ch,
// without waiting for more; open is false when ch turned out closed.
func drain(ch <-chan ipc.Event, first ipc.Event) (evs []ipc.Event, open bool) {
	evs = []ipc.Event{first}
	for len(evs) < maxBatch {
		select {
		case ev, ok := <-ch:
			if !ok {
				return evs, false
			}
			evs = append(evs, ev)
		default:
			return evs, true
		}
	}
	return evs, true
}

// dial connects again; on failure it reports the process down and waits.
func (s *supervisor) dial(ctx context.Context) bool {
	if s.opts.Dial == nil {
		s.send(ctx, backendDownMsg{err: errors.New("no way to reach the background process")})
		s.wait(ctx)
		return false
	}
	b, err := s.opts.Dial(ctx)
	if err != nil || b == nil {
		if err == nil {
			err = fmt.Errorf("dial: %w", ipc.ErrClosed)
		}
		if ctx.Err() == nil {
			s.send(ctx, backendDownMsg{err: err})
			s.wait(ctx)
		}
		return false
	}
	s.b = b
	return true
}

// drop closes the current connection, reports it and waits before dialling.
func (s *supervisor) drop(ctx context.Context, why error) {
	_ = s.b.Close()
	s.b = nil
	if ctx.Err() != nil {
		return
	}
	s.send(ctx, backendDownMsg{err: why})
	s.wait(ctx)
}

// wait returns after retryEvery, or at once when the user started the
// process and its connection is handed over.
func (s *supervisor) wait(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-s.opts.After(retryEvery):
	case b := <-s.adopt:
		s.b = b
	}
}

func (s *supervisor) send(ctx context.Context, msg tea.Msg) {
	if ctx.Err() == nil {
		s.opts.Send(msg)
	}
}
