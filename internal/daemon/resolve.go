package daemon

import (
	"fmt"
	"slices"
	"strings"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
)

// resolve finds a conversation by reference: "@handle" is the DM with that
// person, "#room" a room by name, anything else an exact id or else a bare
// name tried as a room and as a handle. Rooms include open rooms the user
// has not joined.
func (a *account) resolve(ref string) (model.Conversation, error) {
	ref = strings.TrimSpace(ref)
	views, err := a.st.Conversations()
	if err != nil {
		return model.Conversation{}, err
	}
	var mine []model.Conversation
	for _, v := range views {
		mine = append(mine, v.Conversation)
	}
	all := append(slices.Clone(mine), a.rooms.list()...)
	me := a.st.Me()
	name := model.NormalizeName(ref)

	dms := func() []model.Conversation {
		var out []model.Conversation
		for _, c := range mine {
			if c.Kind != model.ConvDM {
				continue
			}
			if slices.ContainsFunc(c.Members, func(h string) bool {
				return strings.EqualFold(h, name) && (h != me || isSelfDM(c, me))
			}) {
				out = append(out, c)
			}
		}
		return out
	}
	rooms := func() []model.Conversation {
		var out []model.Conversation
		seen := map[string]bool{}
		for _, c := range all {
			if c.Kind == model.ConvRoom && !seen[c.ID] && model.NormalizeName(c.Name) == name {
				seen[c.ID] = true
				out = append(out, c)
			}
		}
		return out
	}

	switch {
	case strings.HasPrefix(ref, "@"):
		return one(dms(), ref, "no conversation with "+ref+" yet; open one first")
	case strings.HasPrefix(ref, "#"):
		return one(rooms(), ref, "no room "+ref)
	}
	if i := slices.IndexFunc(all, func(c model.Conversation) bool { return c.ID == ref }); i >= 0 {
		return all[i], nil
	}
	return one(append(rooms(), dms()...), ref, "no room or conversation "+ref)
}

func isSelfDM(c model.Conversation, me string) bool {
	return len(c.Members) == 1 && c.Members[0] == me
}

func one(found []model.Conversation, ref, missing string) (model.Conversation, error) {
	switch len(found) {
	case 0:
		return model.Conversation{}, fmt.Errorf("%w: %s", ipc.ErrNotFound, missing)
	case 1:
		return found[0], nil
	}
	ids := make([]string, len(found))
	for i, c := range found {
		ids[i] = c.ID
	}
	return model.Conversation{}, fmt.Errorf("%w: %q matches %s", ipc.ErrAmbiguous, ref, strings.Join(ids, ", "))
}
