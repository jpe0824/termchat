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
	b    Backend
	opts Options
}

func (s *supervisor) run(ctx context.Context) {
	defer func() {
		if s.b != nil {
			_ = s.b.Close()
		}
	}()
	for ctx.Err() == nil {
		if s.b == nil && !s.dial(ctx) {
			continue
		}
		// The interface does not draw notifications itself yet.
		ch, err := s.b.SubscribeUI(ctx, false)
		if err != nil {
			s.drop(ctx, err)
			continue
		}
		s.send(ctx, subscribedMsg{b: s.b})
		if !s.forward(ctx, ch) {
			return
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

// forward passes events on until the channel closes, and reports false
// when ctx ended first.
func (s *supervisor) forward(ctx context.Context, ch <-chan ipc.Event) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case ev, ok := <-ch:
			if !ok {
				return ctx.Err() == nil
			}
			s.send(ctx, eventMsg{ev: ev})
		}
	}
}

// dial connects again; on failure it reports the process down and waits.
func (s *supervisor) dial(ctx context.Context) bool {
	if s.opts.Dial == nil {
		s.send(ctx, backendDownMsg{err: errors.New("no way to reach the background process")})
		<-ctx.Done()
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

func (s *supervisor) wait(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-s.opts.After(retryEvery):
	}
}

func (s *supervisor) send(ctx context.Context, msg tea.Msg) {
	if ctx.Err() == nil {
		s.opts.Send(msg)
	}
}
