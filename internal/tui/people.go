package tui

import (
	"cmp"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/sanitize"
)

// requestPeople re-reads the people list of the current connection, once
// at a time.
func (m *Model) requestPeople() tea.Cmd {
	if m.b == nil || m.peopleBusy {
		return nil
	}
	m.peopleBusy = true
	b := m.b
	return func() tea.Msg {
		people, err := b.People()
		return peopleMsg{b: b, people: people, err: err}
	}
}

func (m *Model) onPeople(msg peopleMsg) {
	m.peopleBusy = false
	if msg.b == m.b && msg.err == nil {
		m.people = msg.people
	}
}

func (m *Model) person(handle string) (model.Person, bool) {
	i := slices.IndexFunc(m.people, func(p model.Person) bool { return p.Handle == handle })
	if i < 0 {
		return model.Person{}, false
	}
	return m.people[i], true
}

func (m *Model) isAgent(handle string) bool {
	p, ok := m.person(handle)
	return ok && p.Agent || handle == m.me() && m.status.Me.Agent
}

// personLabel is "@handle  Display Name", tagged when an agent.
func (m *Model) personLabel(handle string) string {
	s := "@" + sanitize.Line(handle)
	if p, ok := m.person(handle); ok && p.DisplayName != "" && p.DisplayName != handle {
		s += "  " + sanitize.Line(p.DisplayName)
	}
	if m.isAgent(handle) {
		s += " [agent]"
	}
	return s
}

// query is the field's text as a filter: lower case, without a leading @
// or #.
func (m *Model) query() string { return strings.TrimSpace(model.NormalizeName(m.ov.field.text)) }

// personMatches reports whether q is in the handle or display name.
func (m *Model) personMatches(handle, q string) bool {
	if q == "" || strings.Contains(strings.ToLower(handle), q) {
		return true
	}
	p, _ := m.person(handle)
	return strings.Contains(strings.ToLower(sanitize.Line(p.DisplayName)), q)
}

// pickPeople are the people other than the user, not in skip, matching the
// field: an exact handle first, then handles starting with it, then the
// rest by handle.
func (m *Model) pickPeople(skip []string) []ovRow {
	q := m.query()
	var hs []string
	for _, p := range m.people {
		if p.Handle != m.me() && !slices.Contains(skip, p.Handle) && m.personMatches(p.Handle, q) {
			hs = append(hs, p.Handle)
		}
	}
	rank := func(h string) int {
		switch {
		case h == q:
			return 0
		case strings.HasPrefix(h, q):
			return 1
		}
		return 2
	}
	slices.SortStableFunc(hs, func(a, b string) int { return cmp.Or(cmp.Compare(rank(a), rank(b)), cmp.Compare(a, b)) })
	rows := make([]ovRow, 0, len(hs))
	for _, h := range hs {
		rows = append(rows, ovRow{id: "p:" + h, text: m.personLabel(h)})
	}
	return rows
}

// pickedHandle is the person an enter in a handle field means: the
// selected match, or "" with the error to show.
func (m *Model) pickedHandle() (string, string) {
	if id, ok := strings.CutPrefix(m.ovSel().id, "p:"); ok {
		return id, ""
	}
	if q := m.query(); q != "" {
		return "", "no one with handle " + sanitize.Line(q)
	}
	return "", ""
}

// dmWith is the user's DM with handle, if any.
func (m *Model) dmWith(handle string) string {
	for _, c := range m.convs {
		if c.Kind == model.ConvDM && c.Peer(m.me()) == handle {
			return c.ID
		}
	}
	return ""
}

// openDM opens the DM with handle, starting it through the background
// process when there is none yet.
func (m *Model) openDM(handle string) tea.Cmd {
	if id := m.dmWith(handle); id != "" {
		m.closeOverlay()
		return m.open(id)
	}
	return m.ovCall(opOpenDM, "", func(b Backend) (string, error) {
		c, err := b.OpenDM(handle)
		return c.ID, err
	})
}
