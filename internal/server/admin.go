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
	return st.CreateDeviceInvite(h, InviteTTL)
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
	if existing {
		code, err = st.CreateDeviceInvite(h, InviteTTL)
	} else {
		code, err = st.CreateInvite(h, displayName, InviteTTL)
	}
	return code, existing, err
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
	return st.SetAdmin(p.Handle, admin)
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
//
// emit, when not nil, runs each room's removal: it is given the room and op,
// which writes the member.left event, so a running server can hold the room's
// lock around op and publish the event before releasing it. A nil emit just
// runs op.
func RemovePersonFully(st *Store, actor, target string, emit func(conv string, op func() (model.Event, error)) error) (Removal, error) {
	p, err := st.Person(target)
	if err != nil {
		return Removal{}, err
	}
	last, err := st.IsLastAdmin(p.Handle)
	if err != nil {
		return Removal{}, err
	}
	if last {
		return Removal{}, ErrLastAdmin
	}
	keys, err := st.Keys(p.Handle)
	if err != nil {
		return Removal{}, err
	}
	convs, err := st.ConversationsFor(p.Handle)
	if err != nil {
		return Removal{}, err
	}
	if emit == nil {
		emit = func(_ string, op func() (model.Event, error)) error {
			_, err := op()
			return err
		}
	}
	var out Removal
	for _, conv := range convs {
		if conv.Kind != model.ConvRoom {
			continue
		}
		var ev model.Event
		err := emit(conv.ID, func() (model.Event, error) {
			var err error
			ev, err = st.RemoveMember(conv.ID, actor, p.Handle)
			return ev, err
		})
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
