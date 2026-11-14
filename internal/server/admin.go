package server

import (
	"errors"
	"fmt"
	"time"

	"github.com/jpe0824/termchat/internal/model"
)

// InviteTTL is how long an invite code stays redeemable.
const InviteTTL = 24 * time.Hour

var (
	// ErrNotEmpty means the server already has people, so it cannot be initialised.
	ErrNotEmpty = errors.New("server already has people")
	// ErrLastAdmin means the operation would leave the server without an admin.
	ErrLastAdmin = errors.New("that is the last admin")
	// ErrAgent means the operation does not apply to agent accounts.
	ErrAgent = errors.New("agent accounts cannot be admins")
)

// InitFirstAdmin creates handle as an admin on an empty server and returns an
// invite for their first device.
func InitFirstAdmin(st *Store, handle, displayName string) (string, error) {
	n, err := st.PersonCount()
	if err != nil {
		return "", err
	}
	if n > 0 {
		return "", ErrNotEmpty
	}
	h, err := cleanName(handle)
	if err != nil {
		return "", err
	}
	if err := st.CreatePerson(model.Person{Handle: h, DisplayName: displayName, Admin: true}); err != nil {
		return "", err
	}
	return st.CreateInvite(h, displayName, InviteTTL)
}

// InviteHandle issues an invite for handle. A handle with no account gets one
// when the invite is redeemed; an existing account gains a device. existing
// reports which. A removed handle is refused with ErrExists.
func InviteHandle(st *Store, handle, displayName string) (code string, existing bool, err error) {
	h, err := cleanName(handle)
	if err != nil {
		return "", false, err
	}
	switch _, perr := st.Person(h); {
	case perr == nil:
		existing = true
	case !errors.Is(perr, ErrNotFound):
		return "", false, perr
	}
	code, err = st.CreateInvite(h, displayName, InviteTTL)
	return code, existing, err
}

func adminCount(st *Store) (int, error) {
	people, err := st.People()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range people {
		if p.Admin {
			n++
		}
	}
	return n, nil
}

// SetAdminRights grants or revokes admin rights. Agents cannot be admins and
// the last admin cannot be revoked.
func SetAdminRights(st *Store, handle string, admin bool) error {
	p, err := st.Person(handle)
	if err != nil {
		return err
	}
	if p.Agent {
		return fmt.Errorf("%w: %s", ErrAgent, p.Handle)
	}
	if !admin && p.Admin {
		n, err := adminCount(st)
		if err != nil {
			return err
		}
		if n <= 1 {
			return ErrLastAdmin
		}
	}
	return st.SetAdmin(p.Handle, admin)
}

// RemovalHooks lets a running server serialise and publish removals. Both
// fields are optional.
type RemovalHooks struct {
	// Lock is taken around each room's removal and Emit call and returns the
	// unlock function.
	Lock func(conv string) func()
	// Emit receives each member.left event while the room is still locked.
	Emit func(ev model.Event)
}

// Removal is what RemovePersonFully did.
type Removal struct {
	// Events are the member.left events appended, one per room left.
	Events []model.Event
	// Keys are the fingerprints that were deleted; sessions using them must be closed.
	Keys []string
}

// RemovePersonFully takes target out of every room as actor, appending
// member.left, then removes them. The last admin is refused, first by an early
// check that avoids leaving rooms for nothing and finally, atomically, by
// Store.RemovePerson. DM history stays.
func RemovePersonFully(st *Store, actor, target string, hooks RemovalHooks) (Removal, error) {
	p, err := st.Person(target)
	if err != nil {
		return Removal{}, err
	}
	if p.Admin {
		n, err := adminCount(st)
		if err != nil {
			return Removal{}, err
		}
		if n <= 1 {
			return Removal{}, ErrLastAdmin
		}
	}
	keys, err := st.Keys(p.Handle)
	if err != nil {
		return Removal{}, err
	}
	convs, err := st.ConversationsFor(p.Handle)
	if err != nil {
		return Removal{}, err
	}
	var out Removal
	for _, conv := range convs {
		if conv.Kind != model.ConvRoom {
			continue
		}
		ev, err := removeFromRoom(st, conv.ID, actor, p.Handle, hooks)
		if err != nil {
			return out, err
		}
		out.Events = append(out.Events, ev)
	}
	if err := st.RemovePerson(p.Handle); err != nil {
		return out, err
	}
	for _, k := range keys {
		out.Keys = append(out.Keys, k.Fingerprint)
	}
	return out, nil
}

func removeFromRoom(st *Store, conv, actor, target string, hooks RemovalHooks) (model.Event, error) {
	if hooks.Lock != nil {
		defer hooks.Lock(conv)()
	}
	ev, err := st.RemoveMember(conv, actor, target)
	if err != nil {
		return ev, err
	}
	if hooks.Emit != nil {
		hooks.Emit(ev)
	}
	return ev, nil
}

// Room is one row of a room listing.
type Room struct {
	Name    string
	Open    bool
	Members int
}

// ListRooms returns every room, open and private.
func ListRooms(st *Store) ([]Room, error) {
	convs, err := st.Rooms()
	if err != nil {
		return nil, err
	}
	out := make([]Room, 0, len(convs))
	for _, c := range convs {
		out = append(out, Room{Name: c.Name, Open: c.Open, Members: len(c.Members)})
	}
	return out, nil
}
