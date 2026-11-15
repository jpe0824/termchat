package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/sanitize"
)

// isTerminal reports whether w is a terminal. Tests replace it.
var isTerminal = func(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// wantJSON is true with --json and whenever stdout is not a terminal.
func wantJSON(cmd *cobra.Command) bool {
	if j, _ := cmd.Flags().GetBool("json"); j {
		return true
	}
	return !isTerminal(cmd.OutOrStdout())
}

// emit prints v as one line of JSON, or runs text in table mode.
func emit(cmd *cobra.Command, v any, text func(w io.Writer) error) error {
	out := cmd.OutOrStdout()
	if wantJSON(cmd) {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(v); err != nil {
			return err
		}
		_, err := out.Write(escapeC1(buf.Bytes()))
		return err
	}
	return text(out)
}

// escapeC1 rewrites DEL and the C1 controls (U+007F to U+009F), which
// encoding/json leaves raw, as \u00XX escapes. JSON's structure is ASCII
// below DEL, so these runes can only be inside strings, where the escape
// decodes to the same rune; a terminal that acts on 8-bit controls then never
// receives one from another person's text.
func escapeC1(b []byte) []byte {
	if !bytes.ContainsFunc(b, isC1) {
		return b
	}
	out := make([]byte, 0, len(b)+16)
	for _, r := range string(b) {
		if isC1(r) {
			out = fmt.Appendf(out, `\u%04x`, r)
			continue
		}
		out = utf8.AppendRune(out, r)
	}
	return out
}

func isC1(r rune) bool { return r >= 0x7f && r <= 0x9f }

func table(out io.Writer) *tabwriter.Writer { return tabwriter.NewWriter(out, 0, 0, 2, ' ', 0) }

// shown is a handle as the server stores it, safe to print.
func shown(handle string) string { return sanitize.Text(model.NormalizeName(handle)) }

func cell(s string) string {
	s = sanitize.Text(s)
	s = strings.NewReplacer("\n", " ", "\t", " ").Replace(s)
	if s == "" {
		return "-"
	}
	return s
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

type statusJSON struct {
	Account string `json:"account"`
	Handle  string `json:"handle"`
	State   string `json:"state"`
	Pending int    `json:"pending"`
	Syncing bool   `json:"syncing"`
	// ServerKey is the pinned server key's fingerprint, as serve prints it.
	ServerKey string `json:"server_key"`
	// SetAside is set by join only: where the replaced account's earlier
	// history was moved.
	SetAside string `json:"set_aside,omitempty"`
}

func statusOf(s ipc.Status) statusJSON {
	return statusJSON{Account: s.Account, Handle: s.Me.Handle, State: string(s.Conn), Pending: s.Pending, Syncing: s.Syncing,
		ServerKey: s.ServerKey}
}

type convJSON struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// convName is the conversation's name as scripts see it: the room name, or
// the other person's handle in a DM.
func convName(c model.Conversation, me string) string {
	if c.Kind == model.ConvRoom {
		return c.Name
	}
	return c.Peer(me)
}

// convLabel is convName with its sigil, for tables.
func convLabel(c model.Conversation, me string) string {
	if c.Kind == model.ConvRoom {
		return "#" + cell(c.Name)
	}
	return "@" + cell(convName(c, me))
}

func convOf(c model.Conversation, me string) convJSON {
	return convJSON{ID: c.ID, Kind: string(c.Kind), Name: convName(c, me)}
}

type codeJSON struct {
	Code string `json:"code"`
	Join string `json:"join"`
}

type chatJSON struct {
	ID     string  `json:"id"`
	Kind   string  `json:"kind"`
	Name   string  `json:"name"`
	Unread int     `json:"unread"`
	LastAt *string `json:"last_at"`
	Joined *bool   `json:"joined,omitempty"`
}

type msgJSON struct {
	Conv     string   `json:"conv"`
	Seq      int64    `json:"seq"`
	MsgID    string   `json:"msg_id"`
	Author   string   `json:"author"`
	Agent    bool     `json:"agent"`
	At       string   `json:"at"`
	Text     string   `json:"text"`
	Mentions []string `json:"mentions"`
	Edited   bool     `json:"edited"`
	Deleted  bool     `json:"deleted"`
	Status   string   `json:"status,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// msgsOf converts messages for output. failures maps the ids of the user's
// failed outbox items to the reason; it may be nil.
func msgsOf(ms []ipc.Message, failures map[string]string) []msgJSON {
	out := make([]msgJSON, 0, len(ms))
	for _, m := range ms {
		j := msgJSON{
			Conv: m.ConvID, Seq: m.Seq, MsgID: m.MsgID, Author: m.Author, Agent: m.Agent,
			At: rfc3339(m.At), Text: m.Text, Mentions: m.Mentions, Edited: m.Edited, Deleted: m.Deleted,
			Status: m.Status,
		}
		if j.Mentions == nil {
			j.Mentions = []string{}
		}
		if m.Status == ipc.MsgFailed {
			j.Error = failures[m.MsgID]
		}
		out = append(out, j)
	}
	return out
}

// writeMessage prints one message in table mode: a header line, then the
// text with continuation lines indented. Every piece of user or server data
// goes through cell or sanitize via these helpers.
func writeMessage(w io.Writer, m msgJSON, prefix string) {
	var tags []string
	if m.Agent {
		tags = append(tags, "[agent]")
	}
	if m.Edited {
		tags = append(tags, "(edited)")
	}
	switch m.Status {
	case ipc.MsgSending:
		tags = append(tags, "(sending)")
	case ipc.MsgFailed:
		tags = append(tags, "(failed: "+cell(m.Error)+")")
	}
	seq := "-"
	if m.Seq > 0 {
		seq = fmt.Sprint(m.Seq)
	}
	head := fmt.Sprintf("%s%s  %s  @%s", prefix, seq, m.At, cell(m.Author))
	if len(tags) > 0 {
		head += " " + strings.Join(tags, " ")
	}
	if m.Deleted {
		fmt.Fprintf(w, "%s  (deleted)\n", head)
		return
	}
	text := strings.ReplaceAll(sanitize.Text(m.Text), "\n", "\n    ")
	fmt.Fprintf(w, "%s  %s\n", head, text)
}
