package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jpe0824/termchat/internal/connector"
	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/store"
)

const (
	defaultLimit = 50
	maxLimit     = 500
	// maxHistoryPages bounds the server round trips one Messages call makes.
	maxHistoryPages = 5
)

var _ ipc.Backend = (*daemon)(nil)

// errMaybe reports a change request whose outcome could not be confirmed.
func errMaybe(what string) error {
	return fmt.Errorf("%w: the server did not answer, so it is not known whether the %s took effect", ipc.ErrTimeout, what)
}

// errNotMade reports a change request that went unanswered and, as a
// follow-up read showed, did not take effect.
func errNotMade(what string) error {
	return fmt.Errorf("%w: the server did not answer and the %s did not take effect", ipc.ErrTimeout, what)
}

func (d *daemon) Status(context.Context) (ipc.Status, error) {
	d.mu.Lock()
	a := d.acct
	d.mu.Unlock()
	if a == nil {
		return ipc.Status{Conn: connector.StateOffline}, nil
	}
	return a.status(), nil
}

func (d *daemon) Chats(ctx context.Context) (ipc.Chats, error) {
	a, err := d.account()
	if err != nil {
		return ipc.Chats{}, err
	}
	if a.connected() {
		// A failure leaves the cached list, which is still the best answer.
		_ = a.refreshOpenRooms(ctx)
	}
	mine, err := a.st.Conversations()
	if err != nil {
		return ipc.Chats{}, err
	}
	out := ipc.Chats{Mine: mine}
	a.mu.Lock()
	open := slices.Clone(a.openRooms)
	a.mu.Unlock()
	for _, c := range open {
		if !slices.ContainsFunc(mine, func(v store.ConvView) bool { return v.ID == c.ID }) {
			out.Open = append(out.Open, c)
		}
	}
	return out, nil
}

func (d *daemon) Messages(ctx context.Context, conv string, beforeSeq int64, limit int) ([]store.Message, error) {
	a, err := d.account()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = defaultLimit
	}
	limit = min(limit, maxLimit)
	for range maxHistoryPages {
		msgs, err := a.st.Messages(conv, beforeSeq, limit)
		if err != nil {
			return nil, err
		}
		stored := 0
		for _, m := range msgs {
			if m.Status == "" {
				stored++
			}
		}
		floor := a.floorOf(conv)
		if stored >= limit || floor == 1 || a.conn.State() != connector.StateConnected {
			break
		}
		a.prioritize(conv)
		evs, err := a.conn.History(ctx, conv, floor, historyPage)
		if err != nil {
			a.log.Warn("history for reader failed", "conv", conv, "err", err)
			break
		}
		a.storeHistory(conv, evs)
		a.emitConv(conv)
	}
	return a.st.Messages(conv, beforeSeq, limit)
}

func (d *daemon) Outbox(_ context.Context, conv string) ([]store.OutboxItem, error) {
	a, err := d.account()
	if err != nil {
		return nil, err
	}
	return a.st.Outbox(conv)
}

func (d *daemon) Send(_ context.Context, conv, text, source string) (string, error) {
	a, err := d.account()
	if err != nil {
		return "", err
	}
	switch source {
	case "cli":
		d.mu.Lock()
		deny := d.cfg.CLI.Send == "deny"
		d.mu.Unlock()
		if deny {
			return "", ipc.ErrSendDenied
		}
	case "tui":
	default:
		return "", fmt.Errorf("%w: unknown source %q", ipc.ErrInvalid, source)
	}
	if err := checkText(text); err != nil {
		return "", err
	}
	return a.enqueue(conv, text, source)
}

func (d *daemon) Edit(ctx context.Context, conv, msgID, text string) error {
	a, err := d.account()
	if err != nil {
		return err
	}
	if err := checkText(text); err != nil {
		return err
	}
	v, ok := a.view(conv)
	if !ok {
		return fmt.Errorf("%w: conversation %s", ipc.ErrNotFound, conv)
	}
	ev, err := a.conn.Edit(ctx, conv, msgID, textBody(text), mentions(text, v.Members))
	if errors.Is(err, connector.ErrUnknownOutcome) {
		return a.confirm(ctx, conv, "edit", func(m store.Message) bool { return m.Edited && m.Text == text }, msgID)
	}
	if err != nil {
		return err
	}
	a.announce(a.apply([]model.Event{ev}), true)
	return nil
}

func (d *daemon) Delete(ctx context.Context, conv, msgID string) error {
	a, err := d.account()
	if err != nil {
		return err
	}
	ev, err := a.conn.Delete(ctx, conv, msgID)
	if errors.Is(err, connector.ErrUnknownOutcome) {
		return a.confirm(ctx, conv, "delete", func(m store.Message) bool { return m.Deleted }, msgID)
	}
	if err != nil {
		return err
	}
	a.announce(a.apply([]model.Event{ev}), true)
	return nil
}

// confirm syncs conv after an unanswered change to msgID and reports
// whether done shows it took effect.
func (a *account) confirm(ctx context.Context, conv, what string, done func(store.Message) bool, msgID string) error {
	if err := a.syncConv(ctx, conv); err != nil {
		return errMaybe(what)
	}
	m, err := a.st.ByMsgID(conv, msgID)
	if err == nil && done(m) {
		return nil
	}
	return errNotMade(what)
}

func (d *daemon) Retry(_ context.Context, msgID string) error {
	a, err := d.account()
	if err != nil {
		return err
	}
	return a.retry(msgID)
}

func (d *daemon) Discard(_ context.Context, msgID string) error {
	a, err := d.account()
	if err != nil {
		return err
	}
	return a.discard(msgID)
}

func (d *daemon) MarkRead(ctx context.Context, conv string) error {
	a, err := d.account()
	if err != nil {
		return err
	}
	if _, ok := a.view(conv); !ok {
		return fmt.Errorf("%w: conversation %s", ipc.ErrNotFound, conv)
	}
	seq := a.st.Cursor(conv)
	if newest, err := a.st.Messages(conv, 0, 1); err == nil {
		for _, m := range newest {
			if m.Status == "" {
				seq = max(seq, m.Seq)
			}
		}
	}
	if seq == 0 {
		return nil
	}
	if err := a.write(func() error { return a.st.SetRead(conv, seq) }); err != nil {
		return err
	}
	a.emitConv(conv)
	// Offline, the local position is enough: the next catch-up pushes it.
	if a.conn.State() != connector.StateConnected {
		return nil
	}
	err = a.conn.MarkRead(ctx, conv, seq)
	if errors.Is(err, connector.ErrUnknownOutcome) {
		// The marker only moves forward, so asking again is safe.
		if err = a.conn.MarkRead(ctx, conv, seq); errors.Is(err, connector.ErrUnknownOutcome) {
			return errMaybe("read position update")
		}
	}
	if errors.Is(err, connector.ErrOffline) {
		return nil
	}
	return err
}

func (d *daemon) Search(_ context.Context, q string, limit int) ([]store.Message, error) {
	a, err := d.account()
	if err != nil {
		return nil, err
	}
	return a.st.Search(q, min(limit, maxLimit))
}

func (d *daemon) Resolve(ctx context.Context, ref string) (model.Conversation, error) {
	a, err := d.account()
	if err != nil {
		return model.Conversation{}, err
	}
	c, err := a.resolve(ref)
	// A room may have been opened since the list was read; look once more
	// after refreshing it. A DM reference is never an open room.
	if errors.Is(err, ipc.ErrNotFound) && !strings.HasPrefix(strings.TrimSpace(ref), "@") && a.connected() {
		if a.refreshOpenRooms(ctx) == nil {
			return a.resolve(ref)
		}
	}
	return c, err
}

func (d *daemon) OpenDM(ctx context.Context, handle string) (model.Conversation, error) {
	a, err := d.account()
	if err != nil {
		return model.Conversation{}, err
	}
	handle = model.NormalizeName(handle)
	c, err := a.conn.OpenDM(ctx, handle)
	if errors.Is(err, connector.ErrUnknownOutcome) {
		c, err = a.verifyConv(ctx, "conversation", func(c model.Conversation) bool {
			return c.Kind == model.ConvDM && slices.Contains(c.Members, handle)
		})
	}
	if err != nil {
		return model.Conversation{}, err
	}
	a.joined(ctx, c)
	return c, nil
}

func (d *daemon) Room(ctx context.Context, op connector.RoomOp) (model.Conversation, error) {
	a, err := d.account()
	if err != nil {
		return model.Conversation{}, err
	}
	c, err := a.conn.Room(ctx, op)
	if errors.Is(err, connector.ErrUnknownOutcome) {
		c, err = a.verifyRoom(ctx, op)
	}
	if err != nil {
		return model.Conversation{}, err
	}
	if op.Kind == connector.RoomLeave {
		a.markGone(op.Conv)
		return c, nil
	}
	a.joined(ctx, c)
	return c, nil
}

// joined stores a conversation the user is now in and fetches its newest
// page when nothing of it is held yet; the backfill does the rest.
func (a *account) joined(ctx context.Context, c model.Conversation) {
	a.upsertConv(c)
	if c.LastSeq > 0 && a.st.OldestSeq(c.ID) == 0 {
		if evs, err := a.conn.History(ctx, c.ID, 0, historyPage); err == nil {
			a.storeHistory(c.ID, evs)
			a.emitConv(c.ID)
		}
	}
	signal(a.bfWake)
}

// listed re-reads the conversation list after an unanswered request and
// returns the conversation match accepts, if any.
func (a *account) listed(ctx context.Context, match func(model.Conversation) bool) (model.Conversation, bool, error) {
	mine, _, err := a.conn.Conversations(ctx)
	if err != nil {
		return model.Conversation{}, false, err
	}
	if i := slices.IndexFunc(mine, match); i >= 0 {
		return mine[i], true, nil
	}
	return model.Conversation{}, false, nil
}

// verifyConv reports what an unanswered request that should have produced a
// conversation match accepts actually did.
func (a *account) verifyConv(ctx context.Context, what string, match func(model.Conversation) bool) (model.Conversation, error) {
	c, found, err := a.listed(ctx, match)
	switch {
	case err != nil:
		return model.Conversation{}, errMaybe(what)
	case !found:
		return model.Conversation{}, errNotMade(what)
	}
	return c, nil
}

func (a *account) verifyRoom(ctx context.Context, op connector.RoomOp) (model.Conversation, error) {
	const what = "room change"
	if op.Kind == connector.RoomLeave {
		_, found, err := a.listed(ctx, func(c model.Conversation) bool { return c.ID == op.Conv })
		switch {
		case err != nil:
			return model.Conversation{}, errMaybe(what)
		case found:
			return model.Conversation{}, errNotMade(what)
		}
		return model.Conversation{ID: op.Conv}, nil
	}
	me := a.conn.Me().Handle
	return a.verifyConv(ctx, what, func(c model.Conversation) bool {
		switch op.Kind {
		case connector.RoomCreate:
			return c.Kind == model.ConvRoom && c.Creator == me && model.NormalizeName(c.Name) == model.NormalizeName(op.Name)
		case connector.RoomJoin:
			return c.ID == op.Conv
		case connector.RoomInvite:
			return c.ID == op.Conv && slices.Contains(c.Members, op.Handle)
		case connector.RoomRemove:
			return c.ID == op.Conv && !slices.Contains(c.Members, op.Handle)
		case connector.RoomRename:
			return c.ID == op.Conv && c.Name == op.Name
		}
		return false
	})
}

func (d *daemon) DeviceInvite(ctx context.Context) (string, error) {
	a, err := d.account()
	if err != nil {
		return "", err
	}
	return invite(a.conn.DeviceInvite(ctx))
}

func (d *daemon) CreateAgent(ctx context.Context, handle, name string) (string, error) {
	a, err := d.account()
	if err != nil {
		return "", err
	}
	return invite(a.conn.CreateAgent(ctx, handle, name))
}

func (d *daemon) AdminInvite(ctx context.Context, handle, name string) (string, error) {
	a, err := d.account()
	if err != nil {
		return "", err
	}
	return invite(a.conn.AdminInvite(ctx, handle, name))
}

// invite passes a code through; an unanswered request cannot be checked.
func invite(code string, err error) (string, error) {
	if errors.Is(err, connector.ErrUnknownOutcome) {
		return "", errMaybe("invite")
	}
	return code, err
}
