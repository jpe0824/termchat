package server_test

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"golang.org/x/crypto/ssh"

	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/proto"
	"github.com/jpe0824/termchat/internal/server"
	"github.com/jpe0824/termchat/internal/testutil"
)

const pass = "correct horse"

func adminStore(t *testing.T) (*server.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := server.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, dir
}

func mustPerson(t *testing.T, st *server.Store, p model.Person) {
	t.Helper()
	if err := st.CreatePerson(p); err != nil {
		t.Fatalf("CreatePerson(%s): %v", p.Handle, err)
	}
}

func TestInitOnlyOnce(t *testing.T) {
	st, _ := adminStore(t)
	code, err := server.InitFirstAdmin(st, "Ann", "Ann A")
	if err != nil || code == "" {
		t.Fatalf("first init: %q, %v", code, err)
	}
	p, err := st.Person("ann")
	if err != nil || !p.Admin || p.DisplayName != "Ann A" {
		t.Fatalf("person = %+v, %v", p, err)
	}
	if _, err := server.InitFirstAdmin(st, "bob", ""); !errors.Is(err, server.ErrNotEmpty) {
		t.Fatalf("second init error = %v, want ErrNotEmpty", err)
	}
	if _, err := st.Person("bob"); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("bob created despite refusal: %v", err)
	}
}

func TestInitRejectsInvalidHandle(t *testing.T) {
	st, _ := adminStore(t)
	if _, err := server.InitFirstAdmin(st, "Not Valid!", ""); !errors.Is(err, server.ErrInvalidName) {
		t.Fatalf("error = %v, want ErrInvalidName", err)
	}
	if n, _ := st.PersonCount(); n != 0 {
		t.Fatalf("PersonCount = %d after refusal", n)
	}
}

func TestInitThenRedeemGivesAdmin(t *testing.T) {
	h := testutil.StartServer(t)
	code, err := server.InitFirstAdmin(h.Store, "ann", "Ann")
	if err != nil {
		t.Fatal(err)
	}
	c := h.Dial(t, testutil.NewKey(t))
	testutil.Hello(t, c)
	f := testutil.Request(t, c, proto.TypeInviteRedeem, proto.InviteRedeemReq{Code: code})
	if f.Error != nil {
		t.Fatalf("redeem: %v", f.Error)
	}
	var res proto.InviteRedeemRes
	if err := json.Unmarshal(f.Payload, &res); err != nil {
		t.Fatal(err)
	}
	if res.Person.Handle != "ann" || !res.Person.Admin {
		t.Fatalf("redeemed %+v, want admin ann", res.Person)
	}
}

func TestInviteHandle(t *testing.T) {
	st, _ := adminStore(t)
	mustPerson(t, st, model.Person{Handle: "ann", Admin: true})

	code, existing, err := server.InviteHandle(st, "newbie", "New")
	if err != nil || existing || code == "" {
		t.Fatalf("new handle: %q existing=%v err=%v", code, existing, err)
	}
	p, err := st.RedeemInvite(code, "fp-new", nil)
	if err != nil || p.Handle != "newbie" || p.DisplayName != "New" || p.Admin {
		t.Fatalf("redeemed %+v, %v", p, err)
	}

	code, existing, err = server.InviteHandle(st, "ann", "")
	if err != nil || !existing {
		t.Fatalf("existing handle: existing=%v err=%v", existing, err)
	}
	p, err = st.RedeemInvite(code, "fp-ann2", nil)
	if err != nil || p.Handle != "ann" || !p.Admin {
		t.Fatalf("device invite redeemed %+v, %v", p, err)
	}
	if keys, _ := st.Keys("ann"); len(keys) != 1 {
		t.Fatalf("ann has %d keys, want 1", len(keys))
	}
}

func TestInviteForRemovedHandleRefused(t *testing.T) {
	st, _ := adminStore(t)
	mustPerson(t, st, model.Person{Handle: "ann", Admin: true})
	mustPerson(t, st, model.Person{Handle: "bob"})
	if _, err := server.RemovePersonFully(st, "ann", "bob", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := server.InviteHandle(st, "bob", ""); !errors.Is(err, server.ErrExists) {
		t.Fatalf("error = %v, want ErrExists", err)
	}
}

func TestRemovePersonFully(t *testing.T) {
	st, _ := adminStore(t)
	mustPerson(t, st, model.Person{Handle: "ann", Admin: true})
	mustPerson(t, st, model.Person{Handle: "bob"})
	if err := st.AddKey("bob", "fp-bob-1", []byte("k1")); err != nil {
		t.Fatal(err)
	}
	if err := st.AddKey("bob", "fp-bob-2", []byte("k2")); err != nil {
		t.Fatal(err)
	}
	rA, err := st.CreateRoom("general", true, "ann")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddMember(rA.ID, "ann", "bob"); err != nil {
		t.Fatal(err)
	}
	rB, err := st.CreateRoom("bobs", false, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.OpenDM("ann", "bob"); err != nil {
		t.Fatal(err)
	}

	var emitted []model.Event
	locks := 0
	res, err := server.RemovePersonFully(st, "ann", "bob", func(_ string, op func() (model.Event, error)) error {
		locks++
		ev, err := op()
		if err == nil {
			emitted = append(emitted, ev)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Events) != 2 || len(emitted) != 2 || locks != 2 {
		t.Fatalf("events=%d emitted=%d locks=%d, want 2 each", len(res.Events), len(emitted), locks)
	}
	for _, ev := range res.Events {
		if ev.Kind != model.MemberLeft || ev.Target != "bob" || ev.Actor != "ann" {
			t.Fatalf("event = %+v", ev)
		}
	}
	if len(res.Keys) != 2 || res.Keys[0] != "fp-bob-1" || res.Keys[1] != "fp-bob-2" {
		t.Fatalf("keys = %v", res.Keys)
	}
	if _, err := st.Person("bob"); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("bob still present: %v", err)
	}
	evs, err := st.After(rA.ID, 0)
	if err != nil || evs[len(evs)-1].Kind != model.MemberLeft {
		t.Fatalf("general log = %+v, %v", evs, err)
	}
	if evs, _ := st.After(rB.ID, 0); evs[len(evs)-1].Kind != model.MemberLeft {
		t.Fatalf("bobs log last = %+v", evs[len(evs)-1])
	}
}

func TestRemoveLastAdminRefused(t *testing.T) {
	st, _ := adminStore(t)
	mustPerson(t, st, model.Person{Handle: "ann", Admin: true})
	mustPerson(t, st, model.Person{Handle: "bob"})
	room, err := st.CreateRoom("general", true, "ann")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.RemovePersonFully(st, "ann", "ann", nil); !errors.Is(err, server.ErrLastAdmin) {
		t.Fatalf("error = %v, want ErrLastAdmin", err)
	}
	if p, err := st.Person("ann"); err != nil || !p.Admin {
		t.Fatalf("ann after refusal = %+v, %v", p, err)
	}
	if ok, _ := st.IsMember(room.ID, "ann"); !ok {
		t.Fatal("ann left the room despite the refusal")
	}
	mustPerson(t, st, model.Person{Handle: "cat", Admin: true})
	if _, err := server.RemovePersonFully(st, "cat", "ann", nil); err != nil {
		t.Fatalf("removing one of two admins: %v", err)
	}
}

func TestSetAdminRights(t *testing.T) {
	st, _ := adminStore(t)
	mustPerson(t, st, model.Person{Handle: "ann", Admin: true})
	mustPerson(t, st, model.Person{Handle: "bob"})
	mustPerson(t, st, model.Person{Handle: "bot", Agent: true, Owner: "ann"})

	if err := server.SetAdminRights(st, "bob", true); err != nil {
		t.Fatal(err)
	}
	if p, _ := st.Person("bob"); !p.Admin {
		t.Fatal("bob not admin after grant")
	}
	if err := server.SetAdminRights(st, "bot", true); !errors.Is(err, server.ErrAgent) {
		t.Fatalf("grant to agent: %v, want ErrAgent", err)
	}
	if p, _ := st.Person("bot"); p.Admin {
		t.Fatal("agent was made admin")
	}
	if err := server.SetAdminRights(st, "bob", false); err != nil {
		t.Fatalf("revoke with two admins: %v", err)
	}
	if err := server.SetAdminRights(st, "ann", false); !errors.Is(err, server.ErrLastAdmin) {
		t.Fatalf("revoke last admin: %v, want ErrLastAdmin", err)
	}
	if p, _ := st.Person("ann"); !p.Admin {
		t.Fatal("last admin was revoked")
	}
	if err := server.SetAdminRights(st, "nobody", true); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("unknown handle: %v, want ErrNotFound", err)
	}
}

func TestListRooms(t *testing.T) {
	st, _ := adminStore(t)
	mustPerson(t, st, model.Person{Handle: "ann"})
	mustPerson(t, st, model.Person{Handle: "bob"})
	r, _ := st.CreateRoom("general", true, "ann")
	if _, err := st.AddMember(r.ID, "ann", "bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateRoom("secret", false, "ann"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.OpenDM("ann", "bob"); err != nil {
		t.Fatal(err)
	}
	rooms, err := server.ListRooms(st)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]server.Room{}
	for _, r := range rooms {
		got[r.Name] = r
	}
	if len(rooms) != 2 || got["general"] != (server.Room{Name: "general", Open: true, Members: 2}) ||
		got["secret"] != (server.Room{Name: "secret", Open: false, Members: 1}) {
		t.Fatalf("rooms = %+v", rooms)
	}
}

// ---- backup ----

// seeded returns a data directory holding a server with people, a room and
// messages, plus its host key bytes. The store is closed.
func seeded(t *testing.T) (dir string, hostKey []byte) {
	t.Helper()
	dir = t.TempDir()
	srv, err := server.New(server.Options{Addr: "127.0.0.1:0", DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	st := srv.Store()
	mustPerson(t, st, model.Person{Handle: "ann", DisplayName: "Ann", Admin: true})
	mustPerson(t, st, model.Person{Handle: "bob", DisplayName: "Bob"})
	r, err := st.CreateRoom("general", true, "ann")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddMember(r.ID, "ann", "bob"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PostMessage(r.ID, "ann", "m1", model.Body{Format: "text/plain", Data: []byte("zebra-quartz-7731")}, nil); err != nil {
		t.Fatal(err)
	}
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	hostKey, err = os.ReadFile(filepath.Join(dir, "host_key"))
	if err != nil {
		t.Fatal(err)
	}
	return dir, hostKey
}

func TestBackupRestoreRoundTrip(t *testing.T) {
	src, hostKey := seeded(t)
	out := filepath.Join(t.TempDir(), "b.age")
	if err := server.Backup(src, out, pass); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %v, want 0600", fi.Mode().Perm())
	}

	dst := filepath.Join(t.TempDir(), "restored")
	if err := server.Restore(out, dst, pass); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "host_key"))
	if err != nil || !bytes.Equal(got, hostKey) {
		t.Fatalf("restored host key differs (err %v)", err)
	}
	for _, name := range []string{"server.db", "host_key"} {
		fi, err := os.Stat(filepath.Join(dst, name))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v, %v", name, fi, err)
		}
	}

	st, err := server.OpenStore(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	people, err := st.People()
	if err != nil || len(people) != 2 || people[0].Handle != "ann" || !people[0].Admin || people[1].DisplayName != "Bob" {
		t.Fatalf("people = %+v, %v", people, err)
	}
	rooms, _ := st.Rooms()
	if len(rooms) != 1 || rooms[0].Name != "general" || len(rooms[0].Members) != 2 {
		t.Fatalf("rooms = %+v", rooms)
	}
	evs, err := st.After(rooms[0].ID, 0)
	if err != nil || len(evs) != 3 || string(evs[2].Body.Data) != "zebra-quartz-7731" {
		t.Fatalf("events = %+v, %v", evs, err)
	}
	entries, _ := os.ReadDir(dst)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "backup-") {
			t.Fatalf("temp entry %s in restored dir", e.Name())
		}
	}
}

func TestBackupLeavesNoTempSnapshot(t *testing.T) {
	src, _ := seeded(t)
	list := func() []string {
		es, _ := os.ReadDir(src)
		var names []string
		for _, e := range es {
			names = append(names, e.Name())
		}
		return names
	}
	before := list()
	out := filepath.Join(t.TempDir(), "b.age")
	if err := server.Backup(src, out, pass); err != nil {
		t.Fatal(err)
	}
	if err := server.Backup(src, out, pass); err == nil {
		t.Fatal("second backup over the same file succeeded")
	}
	// Failure after the snapshot: the output path is a directory component that cannot be created.
	bad := filepath.Join(t.TempDir(), "missing", "b.age")
	if err := server.Backup(src, bad, pass); err == nil {
		t.Fatal("backup to an unwritable path succeeded")
	}
	for _, n := range list() {
		if strings.HasPrefix(n, "backup-") {
			t.Fatalf("temp snapshot %s left in data dir", n)
		}
	}
	if len(list()) < len(before) {
		t.Fatalf("data dir lost files: %v -> %v", before, list())
	}
}

func TestBackupRefusesToOverwrite(t *testing.T) {
	src, _ := seeded(t)
	out := filepath.Join(t.TempDir(), "b.age")
	if err := os.WriteFile(out, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := server.Backup(src, out, pass)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %v, want already exists", err)
	}
	if b, _ := os.ReadFile(out); string(b) != "precious" {
		t.Fatalf("existing file was changed: %q", b)
	}
}

func TestBackupIsEncrypted(t *testing.T) {
	src, _ := seeded(t)
	out := filepath.Join(t.TempDir(), "b.age")
	if err := server.Backup(src, out, pass); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"zebra-quartz-7731", "SQLite format 3", "OPENSSH PRIVATE KEY", "server.db"} {
		if bytes.Contains(b, []byte(needle)) {
			t.Fatalf("backup contains plaintext %q", needle)
		}
	}
	if !bytes.HasPrefix(b, []byte("age-encryption.org/")) {
		t.Fatalf("backup does not start with the age header: %q", b[:20])
	}
}

func TestEmptyPassphraseRefused(t *testing.T) {
	src, _ := seeded(t)
	out := filepath.Join(t.TempDir(), "b.age")
	if err := server.Backup(src, out, ""); !errors.Is(err, server.ErrEmptyPassphrase) {
		t.Fatalf("backup error = %v", err)
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("output file created for an empty passphrase")
	}
	if err := server.Restore(out, filepath.Join(t.TempDir(), "r"), ""); !errors.Is(err, server.ErrEmptyPassphrase) {
		t.Fatalf("restore error = %v", err)
	}
}

func TestRestoreWrongPassphraseFails(t *testing.T) {
	src, _ := seeded(t)
	out := filepath.Join(t.TempDir(), "b.age")
	if err := server.Backup(src, out, pass); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "restored")
	if err := server.Restore(out, dst, "not it"); !errors.Is(err, server.ErrWrongPassphrase) {
		t.Fatalf("error = %v, want ErrWrongPassphrase", err)
	}
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("restore created the directory despite a wrong passphrase")
	}
}

func TestRestoreRefusesNonEmptyDir(t *testing.T) {
	src, _ := seeded(t)
	out := filepath.Join(t.TempDir(), "b.age")
	if err := server.Backup(src, out, pass); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	keep := filepath.Join(dst, "keep.txt")
	if err := os.WriteFile(keep, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := server.Restore(out, dst, pass)
	if err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("error = %v, want not empty", err)
	}
	if b, _ := os.ReadFile(keep); string(b) != "mine" {
		t.Fatal("existing file touched")
	}
	if es, _ := os.ReadDir(dst); len(es) != 1 {
		t.Fatalf("directory has %d entries after refusal", len(es))
	}

	empty := t.TempDir()
	if err := server.Restore(out, empty, pass); err != nil {
		t.Fatalf("restore into an existing empty directory: %v", err)
	}
}

type tarEntry struct {
	name string
	typ  byte
	data string
	size int64 // header size when it differs from len(data)
}

// craftBackup encrypts a tar of entries the way Backup does.
func craftBackup(t *testing.T, entries []tarEntry) string {
	t.Helper()
	rcp, err := age.NewScryptRecipient(pass)
	if err != nil {
		t.Fatal(err)
	}
	rcp.SetWorkFactor(10)
	path := filepath.Join(t.TempDir(), "crafted.age")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := age.Encrypt(f, rcp)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(enc)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		size := int64(len(e.data))
		if e.size != 0 {
			size = e.size
		}
		h := &tar.Header{Name: e.name, Mode: 0o644, Typeflag: typ, Size: size}
		if typ == tar.TypeSymlink {
			h.Linkname, h.Size = "/etc/passwd", 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg && e.size == 0 {
			if _, err := tw.Write([]byte(e.data)); err != nil {
				t.Fatal(err)
			}
		}
		if e.size != 0 {
			break // header only; the tar writer cannot be closed cleanly
		}
	}
	if len(entries) == 0 || entries[len(entries)-1].size == 0 {
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRestoreRejectsBadEntries(t *testing.T) {
	good := tarEntry{name: "server.db", data: "db-bytes"}
	tests := []struct {
		name    string
		entries []tarEntry
		want    string
	}{
		{"unexpected name", []tarEntry{good, {name: "extra.txt", data: "x"}}, `unexpected entry "extra.txt"`},
		{"parent traversal", []tarEntry{{name: "../escape", data: "x"}}, `unexpected entry "../escape"`},
		{"nested traversal", []tarEntry{{name: "sub/../../escape", data: "x"}}, "unexpected entry"},
		{"absolute path", []tarEntry{{name: "/etc/passwd", data: "x"}}, `unexpected entry "/etc/passwd"`},
		{"subdirectory", []tarEntry{{name: "sub/server.db", data: "x"}}, "unexpected entry"},
		{"windows separator", []tarEntry{{name: `sub\server.db`, data: "x"}}, "unexpected entry"},
		{"symlink", []tarEntry{{name: "host_key", typ: tar.TypeSymlink}}, "not a regular file"},
		{"directory", []tarEntry{{name: "server.db", typ: tar.TypeDir}}, "not a regular file"},
		{"duplicate", []tarEntry{good, good}, `duplicate entry "server.db"`},
		{"oversized host key", []tarEntry{{name: "host_key", size: 1 << 20}}, `"host_key" is too large`},
		{"missing host key", []tarEntry{good}, "missing host_key"},
		{"missing database", []tarEntry{{name: "host_key", data: "k"}}, "missing server.db"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			file := craftBackup(t, tc.entries)
			parent := t.TempDir()
			dst := filepath.Join(parent, "restored")
			err := server.Restore(file, dst, pass)
			if !errors.Is(err, server.ErrBadBackup) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want ErrBadBackup containing %q", err, tc.want)
			}
			if es, rerr := os.ReadDir(dst); rerr != nil || len(es) != 0 {
				t.Fatalf("restored dir after failure: %v entries, err %v; want empty directory", len(es), rerr)
			}
			if es, _ := os.ReadDir(parent); len(es) != 1 {
				t.Fatalf("files escaped into %s: %d entries", parent, len(es))
			}
		})
	}
}

func TestRestoreAcceptsCraftedValidBackup(t *testing.T) {
	file := craftBackup(t, []tarEntry{{name: "server.db", data: "db"}, {name: "host_key", data: "hk"}})
	dst := filepath.Join(t.TempDir(), "r")
	if err := server.Restore(file, dst, pass); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "host_key")); string(b) != "hk" {
		t.Fatalf("host_key = %q", b)
	}
}

func TestRestoredServerKeepsHostKeyAndPeople(t *testing.T) {
	src, _ := seeded(t)
	orig, err := server.New(server.Options{Addr: "127.0.0.1:0", DataDir: src})
	if err != nil {
		t.Fatal(err)
	}
	want := ssh.FingerprintSHA256(orig.HostKey())
	orig.Close()

	out := filepath.Join(t.TempDir(), "b.age")
	if err := server.Backup(src, out, pass); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "r")
	if err := server.Restore(out, dst, pass); err != nil {
		t.Fatal(err)
	}
	srv, err := server.New(server.Options{Addr: "127.0.0.1:0", DataDir: dst})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if got := ssh.FingerprintSHA256(srv.HostKey()); got != want {
		t.Fatalf("restored host key %s, want %s", got, want)
	}
	if n, _ := srv.Store().PersonCount(); n != 2 {
		t.Fatalf("PersonCount = %d", n)
	}
}

// twoAdminsTwoHandles seeds a file with admins a and b and returns two
// independent store handles on it, as two processes would hold.
func twoAdminsTwoHandles(t *testing.T) (*server.Store, *server.Store) {
	t.Helper()
	s1, dir := adminStore(t)
	mustPerson(t, s1, model.Person{Handle: "a", Admin: true})
	mustPerson(t, s1, model.Person{Handle: "b", Admin: true})
	s2, err := server.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	return s1, s2
}

// raceBoth runs f1 and f2 at the same moment and returns their errors.
func raceBoth(f1, f2 func() error) (error, error) {
	start := make(chan struct{})
	var e1, e2 error
	done := make(chan struct{}, 2)
	go func() { <-start; e1 = f1(); done <- struct{}{} }()
	go func() { <-start; e2 = f2(); done <- struct{}{} }()
	close(start)
	<-done
	<-done
	return e1, e2
}

func requireOneWinner(t *testing.T, st *server.Store, e1, e2 error) {
	t.Helper()
	ok := 0
	for _, e := range []error{e1, e2} {
		switch {
		case e == nil:
			ok++
		case !errors.Is(e, server.ErrLastAdmin):
			t.Fatalf("loser error = %v, want ErrLastAdmin", e)
		}
	}
	if ok != 1 {
		t.Fatalf("%d of 2 concurrent operations succeeded (%v, %v), want exactly 1", ok, e1, e2)
	}
	if n := adminsLeft(t, st); n != 1 {
		t.Fatalf("%d admins left, want 1", n)
	}
}

func adminsLeft(t *testing.T, st *server.Store) int {
	t.Helper()
	people, err := st.People()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, p := range people {
		if p.Admin {
			n++
		}
	}
	return n
}

func TestConcurrentRevokeKeepsAnAdmin(t *testing.T) {
	s1, s2 := twoAdminsTwoHandles(t)
	e1, e2 := raceBoth(
		func() error { return server.SetAdminRights(s1, "a", false) },
		func() error { return server.SetAdminRights(s2, "b", false) },
	)
	requireOneWinner(t, s1, e1, e2)
}

func TestConcurrentRemoveKeepsAnAdmin(t *testing.T) {
	s1, s2 := twoAdminsTwoHandles(t)
	e1, e2 := raceBoth(
		func() error { _, err := server.RemovePersonFully(s1, "a", "a", nil); return err },
		func() error { _, err := server.RemovePersonFully(s2, "b", "b", nil); return err },
	)
	requireOneWinner(t, s1, e1, e2)
}

func TestStoreGuardsLastAdminDirectly(t *testing.T) {
	st, _ := adminStore(t)
	mustPerson(t, st, model.Person{Handle: "a", Admin: true})
	if err := st.SetAdmin("a", false); !errors.Is(err, server.ErrLastAdmin) {
		t.Fatalf("SetAdmin revoke last: %v", err)
	}
	if err := st.RemovePerson("a"); !errors.Is(err, server.ErrLastAdmin) {
		t.Fatalf("RemovePerson last admin: %v", err)
	}
	if p, err := st.Person("a"); err != nil || !p.Admin {
		t.Fatalf("a = %+v, %v", p, err)
	}
}
