package notify

import (
	"testing"

	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/store"
)

func TestPolicyTable(t *testing.T) {
	roomConv := model.Conversation{ID: "r1", Kind: model.ConvRoom, Name: "ops"}
	dmConv := model.Conversation{ID: "d1", Kind: model.ConvDM}
	msg := func(conv string, mut func(*store.Message)) store.Message {
		m := store.Message{ConvID: conv, MsgID: "m1", Author: "dana", Seq: 5, Text: "hi"}
		if mut != nil {
			mut(&m)
		}
		return m
	}
	mention := func(m *store.Message) { m.Mentions = []string{"me"} }
	otherMention := func(m *store.Message) { m.Mentions = []string{"sam"} }
	own := func(m *store.Message) { m.Author = "me" }
	deleted := func(m *store.Message) { m.Deleted = true }
	ctx := func(mode string, mut func(*Context)) Context {
		c := Context{Me: "me", RoomMode: mode, Unread: true}
		if mut != nil {
			mut(&c)
		}
		return c
	}
	read := func(c *Context) { c.Unread = false }
	focusedR := func(c *Context) { c.FocusedConv = "r1"; c.TUIAttached = true }
	focusedD := func(c *Context) { c.FocusedConv = "d1"; c.TUIAttached = true }
	focusedElsewhere := func(c *Context) { c.FocusedConv = "other"; c.TUIAttached = true }

	tests := []struct {
		name string
		m    store.Message
		conv model.Conversation
		c    Context
		want bool
	}{
		{"room default plain", msg("r1", nil), roomConv, ctx("", nil), false},
		{"room default mention", msg("r1", mention), roomConv, ctx("", nil), true},
		{"room default mention of someone else", msg("r1", otherMention), roomConv, ctx("", nil), false},
		{"room mentions plain", msg("r1", nil), roomConv, ctx("mentions", nil), false},
		{"room mentions mention", msg("r1", mention), roomConv, ctx("mentions", nil), true},
		{"room all plain", msg("r1", nil), roomConv, ctx("all", nil), true},
		{"room all mention", msg("r1", mention), roomConv, ctx("all", nil), true},
		{"room muted plain", msg("r1", nil), roomConv, ctx("muted", nil), false},
		{"room muted mention", msg("r1", mention), roomConv, ctx("muted", nil), false},
		{"dm plain", msg("d1", nil), dmConv, ctx("", nil), true},
		{"dm ignores muted", msg("d1", nil), dmConv, ctx("muted", nil), true},
		{"dm ignores mentions mode", msg("d1", nil), dmConv, ctx("mentions", nil), true},
		{"own room message", msg("r1", func(m *store.Message) { own(m); mention(m) }), roomConv, ctx("all", nil), false},
		{"own dm", msg("d1", own), dmConv, ctx("", nil), false},
		{"already read room", msg("r1", mention), roomConv, ctx("all", read), false},
		{"already read dm", msg("d1", nil), dmConv, ctx("", read), false},
		{"focused room", msg("r1", mention), roomConv, ctx("all", focusedR), false},
		{"focused dm", msg("d1", nil), dmConv, ctx("", focusedD), false},
		{"ui focused on another conversation", msg("d1", nil), dmConv, ctx("", focusedElsewhere), true},
		{"deleted room message", msg("r1", func(m *store.Message) { mention(m); deleted(m) }), roomConv, ctx("all", nil), false},
		{"deleted dm", msg("d1", deleted), dmConv, ctx("", nil), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShouldNotify(tt.m, tt.conv, tt.c); got != tt.want {
				t.Errorf("ShouldNotify = %v, want %v", got, tt.want)
			}
		})
	}
}
