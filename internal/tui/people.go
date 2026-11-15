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
	b, gen := m.b, 0
	if m.ov != nil {
		gen = m.ov.gen
	}
	return func() tea.Msg {
		people, err := b.People()
		return peopleMsg{b: b, gen: gen, people: people, err: err}
	}
}

// onPeople keeps a fresh list; a failed read leaves the cached one in use
// and says so in the overlay that asked for it.
func (m *Model) onPeople(msg peopleMsg) {
	m.peopleBusy = false
	switch {
	case msg.b != m.b:
	case msg.err == nil:
		m.setPeople(msg.people)
	case m.ov != nil && m.ov.gen == msg.gen:
		m.ov.info = "people list not refreshed: " + sanitize.Line(msg.err.Error())
	}
}

func (m *Model) setPeople(people []model.Person) {
	m.people, m.byHandle = people, make(map[string]model.Person, len(people))
	for _, p := range people {
		if _, ok := m.byHandle[p.Handle]; !ok {
			m.byHandle[p.Handle] = p
		}
	}
}

func (m *Model) person(handle string) (model.Person, bool) {
	p, ok := m.byHandle[handle]
	return p, ok
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
	return tagged(s, false, m.isAgent(handle))
}

// tagged is a person's label with the tags that apply: "(you)" for the
// user, "[agent]" for an agent.
func tagged(label string, you, agent bool) string {
	if you {
		label += " (you)"
	}
	if agent {
		label += " [agent]"
	}
	return label
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
// With nothing typed there is no one to act on.
func (m *Model) pickedHandle() (string, string) {
	q := m.query()
	if q == "" {
		return "", ""
	}
	if id, ok := strings.CutPrefix(m.ovSel().id, "p:"); ok {
		return id, ""
	}
	return "", "no one with handle " + sanitize.Line(q)
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
