package cli

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"slices"

	"github.com/spf13/cobra"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/store"
)

// waitPage is how many messages one read of the store asks for while
// looking for what arrived since a position.
const waitPage = 200

// waitSource is the part of ipc.Client that waiting uses.
type waitSource interface {
	Subscribe(ctx context.Context) (<-chan ipc.Event, error)
	SubscriptionErr() error
	Messages(conv string, beforeSeq int64, limit int) ([]store.Message, error)
	Chats() ([]store.ConvView, []model.Conversation, error)
}

// waitFilter selects the messages wait returns: incoming ones, never the
// user's own, in conv when set, and only those mentioning the user when
// mentions is set.
type waitFilter struct {
	me, conv string
	mentions bool
}

func (f waitFilter) incoming(m store.Message) bool {
	return m.Status == "" && !m.Deleted && m.Author != f.me &&
		(f.conv == "" || m.ConvID == f.conv) &&
		(!f.mentions || slices.Contains(m.Mentions, f.me))
}

// waitNext returns the next message that passes f. With since set it first
// returns the earliest stored message of f.conv after that sequence number.
//
// It subscribes before it reads the store, so a message arriving in between
// reaches the subscription. A subscription that ends because its reader fell
// behind is replaced and the store is read again from the last position seen
// in each conversation. An edit does not count as a new message; a
// conversation that first appears after waiting began is not rescanned.
func waitNext(ctx context.Context, src waitSource, f waitFilter, since *int64) (store.Message, error) {
	events, err := src.Subscribe(ctx)
	if err != nil {
		return store.Message{}, err
	}
	baseline := map[string]int64{}
	if err := snapshot(src, f, baseline); err != nil {
		return store.Message{}, err
	}
	if since != nil {
		baseline[f.conv] = *since
		if m, ok, err := earliestAfter(src, f, f.conv, *since); err != nil || ok {
			return m, err
		}
	}
	for {
		select {
		case <-ctx.Done():
			return store.Message{}, ctx.Err()
		case ev, open := <-events:
			if open {
				if m := ev.Message; ev.Kind == ipc.KindMessage && m != nil && !m.Edited && f.incoming(*m) {
					return *m, nil
				}
				continue
			}
			if err := src.SubscriptionErr(); !errors.Is(err, ipc.ErrSubscriptionOverflow) {
				if err == nil {
					err = ctx.Err()
				}
				if err == nil {
					err = ipc.ErrClosed
				}
				return store.Message{}, err
			}
			if events, err = src.Subscribe(ctx); err != nil {
				return store.Message{}, err
			}
			if m, ok, err := recheck(src, f, baseline); err != nil || ok {
				return m, err
			}
		}
	}
}

// snapshot records the newest sequence number of each conversation f covers.
func snapshot(src waitSource, f waitFilter, baseline map[string]int64) error {
	mine, _, err := src.Chats()
	if err != nil {
		return err
	}
	for _, v := range mine {
		if f.conv == "" || v.ID == f.conv {
			baseline[v.ID] = v.LastSeq
		}
	}
	return nil
}

// recheck looks for matching messages in the conversations that have moved
// past their baseline since it was taken.
func recheck(src waitSource, f waitFilter, baseline map[string]int64) (store.Message, bool, error) {
	mine, _, err := src.Chats()
	if err != nil {
		return store.Message{}, false, err
	}
	for _, v := range mine {
		if f.conv != "" && v.ID != f.conv {
			continue
		}
		b, known := baseline[v.ID]
		if !known {
			baseline[v.ID] = v.LastSeq
			continue
		}
		if v.LastSeq <= b {
			continue
		}
		if m, ok, err := earliestAfter(src, f, v.ID, b); err != nil || ok {
			return m, ok, err
		}
		baseline[v.ID] = v.LastSeq
	}
	return store.Message{}, false, nil
}

// earliestAfter returns the lowest-numbered message of conv after since that
// passes f.
func earliestAfter(src waitSource, f waitFilter, conv string, since int64) (store.Message, bool, error) {
	var held []store.Message
	var before int64
	for {
		page, err := src.Messages(conv, before, waitPage)
		if err != nil {
			return store.Message{}, false, err
		}
		stored := slices.DeleteFunc(slices.Clone(page), func(m store.Message) bool { return m.Status != "" })
		if len(stored) == 0 {
			break
		}
		held = append(slices.Clone(stored), held...)
		oldest := stored[0].Seq
		if oldest <= since+1 || len(stored) < waitPage {
			break
		}
		before = oldest
	}
	for _, m := range held {
		if m.Seq > since && f.incoming(m) {
			return m, true, nil
		}
	}
	return store.Message{}, false, nil
}

func newWaitCmd() *cobra.Command {
	cmd := clientCmd(&cobra.Command{
		Use:   "wait [<chat>]",
		Short: "Print the next incoming message and exit",
		Long: `Print the next message from someone else, in one chat or in any, and exit 0.
With --timeout, exit 5 if none arrives in time. With --since <seq>, a message
already stored with a higher sequence number is printed at once, so no message
is missed between two calls.`,
		Args: argRange(0, 1),
		RunE: runWait,
	})
	cmd.Flags().Duration("timeout", 0, "give up after this long (0: wait forever)")
	cmd.Flags().Int64("since", 0, "with a chat: print at once the earliest message after this sequence number")
	cmd.Flags().Bool("mentions", false, "only messages that mention you")
	return cmd
}

func runWait(cmd *cobra.Command, args []string) error {
	timeout, _ := cmd.Flags().GetDuration("timeout")
	sinceN, _ := cmd.Flags().GetInt64("since")
	mentions, _ := cmd.Flags().GetBool("mentions")
	hasSince := cmd.Flags().Changed("since")
	switch {
	case timeout < 0:
		return usagef("--timeout must not be negative")
	case hasSince && len(args) == 0:
		return usagef("--since needs a chat")
	case hasSince && sinceN < 0:
		return usagef("--since must not be negative")
	}

	sigCtx, stop := signal.NotifyContext(cmdContext(cmd), os.Interrupt)
	defer stop()
	ctx := sigCtx
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(sigCtx, timeout)
		defer cancel()
	}

	err := withClientCtx(cmd, ctx, func(c *ipc.Client) error {
		me, err := whoAmI(c)
		if err != nil {
			return err
		}
		f := waitFilter{me: me, mentions: mentions}
		if len(args) == 1 {
			conv, err := resolveChat(c, args[0])
			if err != nil {
				return err
			}
			f.conv = conv.ID
		}
		var since *int64
		if hasSince {
			since = &sinceN
		}
		m, err := waitNext(ctx, c, f, since)
		if err != nil {
			return err
		}
		labels, err := convLabels(c, me)
		if err != nil {
			return err
		}
		return printMessages(cmd, []store.Message{m}, nil, labels, true)
	})
	var x *exitError
	if errors.As(err, &x) && x.code == exitOther {
		switch {
		case sigCtx.Err() != nil:
			return exitf(exitInterrupt, "interrupted")
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return exitf(exitTimeout, "timed out waiting for a message")
		}
	}
	return err
}
