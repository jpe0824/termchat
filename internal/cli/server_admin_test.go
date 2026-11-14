package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/proto"
	"github.com/jpe0824/termchat/internal/server"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// run executes the command tree and returns stdout, stderr and the error.
func run(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut syncBuf
	root := Root()
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), errOut.String(), err
}

func mustRun(t *testing.T, args ...string) string {
	t.Helper()
	out, _, err := run(t, args...)
	if err != nil {
		t.Fatalf("termchat %s: %v", strings.Join(args, " "), err)
	}
	return out
}

func wantFail(t *testing.T, code int, contains string, args ...string) {
	t.Helper()
	_, _, err := run(t, args...)
	if err == nil {
		t.Fatalf("termchat %s succeeded; want exit %d", strings.Join(args, " "), code)
	}
	if got := ExitCode(err); got != code || !strings.Contains(err.Error(), contains) {
		t.Fatalf("termchat %s: exit %d, %q; want exit %d containing %q", strings.Join(args, " "), got, err, code, contains)
	}
	if strings.Contains(err.Error(), "\n") {
		t.Fatalf("error is not one line: %q", err)
	}
}

func clearEnv(t *testing.T) {
	t.Helper()
	t.Setenv(dataEnv, "")
	t.Setenv(passphraseEnv, "")
}

func initServer(t *testing.T) string {
	t.Helper()
	clearEnv(t)
	dir := filepath.Join(t.TempDir(), "data")
	mustRun(t, "server", "--data", dir, "init", "--handle", "ann", "--name", "Ann A")
	return dir
}

func withStore(t *testing.T, dir string, fn func(*server.Store)) {
	t.Helper()
	st, err := server.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fn(st)
}

func TestDataFlagRequired(t *testing.T) {
	clearEnv(t)
	for _, args := range [][]string{
		{"serve"},
		{"server", "init", "--handle", "ann"},
		{"server", "invite", "ann"},
		{"server", "user", "list"},
		{"server", "user", "remove", "ann"},
		{"server", "user", "admin", "ann"},
		{"server", "key", "list", "ann"},
		{"server", "key", "remove", "SHA256:x"},
		{"server", "room", "list"},
		{"server", "backup", "b.age"},
		{"server", "restore", "b.age"},
	} {
		wantFail(t, 2, "--data <dir> is required", args...)
	}
}

func TestDataFromEnvironment(t *testing.T) {
	dir := initServer(t)
	t.Setenv(dataEnv, dir)
	out := mustRun(t, "server", "user", "list")
	if !strings.HasPrefix(out, "ann") {
		t.Fatalf("list via env = %q", out)
	}
	other := filepath.Join(t.TempDir(), "other")
	wantFail(t, 1, "no server database in "+other, "server", "--data", other, "user", "list")
}

func TestUsageErrors(t *testing.T) {
	clearEnv(t)
	d := t.TempDir()
	wantFail(t, 2, "--handle is required", "server", "--data", d, "init")
	wantFail(t, 2, "takes 1 argument(s), got 0", "server", "--data", d, "user", "remove")
	wantFail(t, 2, "takes 1 argument(s), got 2", "server", "--data", d, "invite", "a", "b")
	wantFail(t, 2, `unknown command "bogus"`, "server", "--data", d, "user", "bogus")
	wantFail(t, 2, "unknown flag: --nope", "server", "--nope")
	wantFail(t, 2, "--retention must not be negative", "serve", "--data", d, "--retention", "-1h")
	wantFail(t, 2, "invalid handle", "server", "--data", d, "init", "--handle", "Bad Handle!")
}

func TestInitPrintsInviteAndJoinCommand(t *testing.T) {
	clearEnv(t)
	dir := filepath.Join(t.TempDir(), "data")
	out := mustRun(t, "server", "init", "--data", dir, "--handle", "Ann", "--name", "Ann A")
	m := regexp.MustCompile(`invite code: ([A-Z2-7]{10}) \(valid for 24h0m0s\)`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no invite line in %q", out)
	}
	if !strings.Contains(out, "created admin ann\n") {
		t.Fatalf("no creation line in %q", out)
	}
	if !strings.Contains(out, "  termchat join <host:port> --invite "+m[1]+"\n") {
		t.Fatalf("no join command in %q", out)
	}
	withStore(t, dir, func(st *server.Store) {
		p, err := st.Person("ann")
		if err != nil || !p.Admin || p.DisplayName != "Ann A" {
			t.Fatalf("person = %+v, %v", p, err)
		}
	})
}

func TestInitOnlyOnceViaCLI(t *testing.T) {
	dir := initServer(t)
	wantFail(t, 1, "already has people", "server", "--data", dir, "init", "--handle", "bob")
	withStore(t, dir, func(st *server.Store) {
		if n, _ := st.PersonCount(); n != 1 {
			t.Fatalf("PersonCount = %d", n)
		}
	})
}

func TestInviteMessages(t *testing.T) {
	dir := initServer(t)
	out := mustRun(t, "server", "--data", dir, "invite", "bob", "--name", "Bob")
	if !strings.Contains(out, "invite creates the new person bob\n") || !strings.Contains(out, "termchat join <host:port> --invite ") {
		t.Fatalf("new-person invite = %q", out)
	}
	out = mustRun(t, "server", "--data", dir, "invite", "ann")
	if !strings.Contains(out, "invite adds another device to ann\n") {
		t.Fatalf("device invite = %q", out)
	}
}

func TestInviteForRemovedHandleRefused(t *testing.T) {
	dir := initServer(t)
	withStore(t, dir, func(st *server.Store) {
		if err := st.CreatePerson(model.Person{Handle: "bob"}); err != nil {
			t.Fatal(err)
		}
	})
	mustRun(t, "server", "--data", dir, "user", "remove", "bob")
	wantFail(t, 1, "handle bob was removed and cannot be invited again", "server", "--data", dir, "invite", "bob")
}

func TestUserListKeyListRoomList(t *testing.T) {
	dir := initServer(t)
	withStore(t, dir, func(st *server.Store) {
		for _, p := range []model.Person{
			{Handle: "bob", DisplayName: "Bob"},
			{Handle: "bot", Agent: true, Owner: "ann"},
			{Handle: "eve", DisplayName: "Eve\x1b[31m\x07 red"},
		} {
			if err := st.CreatePerson(p); err != nil {
				t.Fatal(err)
			}
		}
		if err := st.AddKey("bob", "SHA256:bobkey", []byte("k")); err != nil {
			t.Fatal(err)
		}
		r, err := st.CreateRoom("general", true, "ann")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.AddMember(r.ID, "ann", "bob"); err != nil {
			t.Fatal(err)
		}
		if _, err := st.CreateRoom("secret", false, "ann"); err != nil {
			t.Fatal(err)
		}
	})

	out := mustRun(t, "server", "--data", dir, "user", "list")
	want := []string{
		"ann  Ann A    admin",
		"bob  Bob      -",
		"bot  -        agent (owner ann)",
		"eve  Eve red  -",
	}
	if got := strings.Split(strings.TrimSpace(out), "\n"); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("user list =\n%s\nwant\n%s", out, strings.Join(want, "\n"))
	}
	if strings.ContainsAny(out, "\x1b\x07") {
		t.Fatalf("control characters in %q", out)
	}

	out = mustRun(t, "server", "--data", dir, "key", "list", "bob")
	if !regexp.MustCompile(`^SHA256:bobkey  \d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ\n$`).MatchString(out) {
		t.Fatalf("key list = %q", out)
	}

	out = mustRun(t, "server", "--data", dir, "room", "list")
	if want := "general  open     2\nsecret   private  1\n"; out != want {
		t.Fatalf("room list = %q, want %q", out, want)
	}
	wantFail(t, 1, "not found", "server", "--data", dir, "key", "list", "nobody")
}

func TestKeyRemove(t *testing.T) {
	dir := initServer(t)
	withStore(t, dir, func(st *server.Store) {
		if err := st.AddKey("ann", "SHA256:annkey", []byte("k")); err != nil {
			t.Fatal(err)
		}
	})
	if out := mustRun(t, "server", "--data", dir, "key", "remove", "SHA256:annkey"); out != "removed key SHA256:annkey\n" {
		t.Fatalf("output = %q", out)
	}
	wantFail(t, 1, "not found", "server", "--data", dir, "key", "remove", "SHA256:annkey")
}

func TestUserRemove(t *testing.T) {
	dir := initServer(t)
	withStore(t, dir, func(st *server.Store) {
		if err := st.CreatePerson(model.Person{Handle: "bob"}); err != nil {
			t.Fatal(err)
		}
		if err := st.AddKey("bob", "SHA256:b1", []byte("k")); err != nil {
			t.Fatal(err)
		}
		r, _ := st.CreateRoom("general", true, "ann")
		if _, err := st.AddMember(r.ID, "ann", "bob"); err != nil {
			t.Fatal(err)
		}
	})
	out := mustRun(t, "server", "--data", dir, "user", "remove", "bob")
	if out != "removed bob: left 1 room(s), revoked 1 key(s)\n" {
		t.Fatalf("output = %q", out)
	}
	withStore(t, dir, func(st *server.Store) {
		if _, err := st.Person("bob"); !errors.Is(err, server.ErrNotFound) {
			t.Fatalf("bob still present: %v", err)
		}
	})
}

func TestUserRemoveLastAdminRefused(t *testing.T) {
	dir := initServer(t)
	wantFail(t, 1, "last admin", "server", "--data", dir, "user", "remove", "ann")
	withStore(t, dir, func(st *server.Store) {
		if p, err := st.Person("ann"); err != nil || !p.Admin {
			t.Fatalf("ann = %+v, %v", p, err)
		}
	})
}

func TestUserAdmin(t *testing.T) {
	dir := initServer(t)
	withStore(t, dir, func(st *server.Store) {
		for _, p := range []model.Person{{Handle: "bob"}, {Handle: "bot", Agent: true, Owner: "ann"}} {
			if err := st.CreatePerson(p); err != nil {
				t.Fatal(err)
			}
		}
	})
	if out := mustRun(t, "server", "--data", dir, "user", "admin", "bob"); out != "granted admin to bob\n" {
		t.Fatalf("grant output = %q", out)
	}
	wantFail(t, 1, "bot is an agent account; agents cannot be admins", "server", "--data", dir, "user", "admin", "bot")
	if out := mustRun(t, "server", "--data", dir, "user", "admin", "bob", "--revoke"); out != "revoked admin from bob\n" {
		t.Fatalf("revoke output = %q", out)
	}
	wantFail(t, 1, "last admin", "server", "--data", dir, "user", "admin", "ann", "--revoke")
	withStore(t, dir, func(st *server.Store) {
		if p, _ := st.Person("ann"); !p.Admin {
			t.Fatal("last admin was revoked")
		}
	})
}

func TestPassphraseSources(t *testing.T) {
	clearEnv(t)
	cmd := Root()
	cmd.SetErr(io.Discard)
	prompts := []string{}
	oldTerm, oldRead := stdinIsTerminal, readPassword
	t.Cleanup(func() { stdinIsTerminal, readPassword = oldTerm, oldRead })

	stdinIsTerminal = func() bool { return false }
	if _, err := passphrase(cmd, true); err == nil || !strings.Contains(err.Error(), passphraseEnv) {
		t.Fatalf("non-terminal without env: %v", err)
	}
	t.Setenv(passphraseEnv, "from-env")
	if p, err := passphrase(cmd, true); err != nil || p != "from-env" {
		t.Fatalf("env passphrase = %q, %v", p, err)
	}
	t.Setenv(passphraseEnv, "")

	stdinIsTerminal = func() bool { return true }
	answers := []string{"s3cret", "s3cret"}
	readPassword = func() (string, error) {
		prompts = append(prompts, "asked")
		a := answers[0]
		answers = answers[1:]
		return a, nil
	}
	if p, err := passphrase(cmd, true); err != nil || p != "s3cret" || len(prompts) != 2 {
		t.Fatalf("prompted passphrase = %q, %v after %d prompts", p, err, len(prompts))
	}
	prompts, answers = nil, []string{"one", "two"}
	if _, err := passphrase(cmd, true); err == nil || err.Error() != "passphrases do not match" {
		t.Fatalf("mismatch error = %v", err)
	}
	prompts, answers = nil, []string{"once"}
	if p, err := passphrase(cmd, false); err != nil || p != "once" || len(prompts) != 1 {
		t.Fatalf("single prompt = %q, %v after %d prompts", p, err, len(prompts))
	}
	answers = []string{""}
	if _, err := passphrase(cmd, false); !errors.Is(err, server.ErrEmptyPassphrase) {
		t.Fatalf("empty prompt error = %v", err)
	}
}

func TestBackupRestoreCommands(t *testing.T) {
	dir := initServer(t)
	t.Setenv(passphraseEnv, "hunter2 hunter2")
	file := filepath.Join(t.TempDir(), "b.age")
	if out := mustRun(t, "server", "--data", dir, "backup", file); out != "backup written to "+file+"\n" {
		t.Fatalf("backup output = %q", out)
	}
	wantFail(t, 1, "already exists", "server", "--data", dir, "backup", file)

	dst := filepath.Join(t.TempDir(), "restored")
	if out := mustRun(t, "server", "--data", dst, "restore", file); out != "restored into "+dst+"\n" {
		t.Fatalf("restore output = %q", out)
	}
	if out := mustRun(t, "server", "--data", dst, "user", "list"); !strings.HasPrefix(out, "ann") {
		t.Fatalf("restored list = %q", out)
	}
	wantFail(t, 1, "not empty", "server", "--data", dst, "restore", file)

	t.Setenv(passphraseEnv, "wrong")
	wantFail(t, 1, "wrong passphrase", "server", "--data", filepath.Join(t.TempDir(), "x"), "restore", file)

	t.Setenv(passphraseEnv, "")
	oldTerm := stdinIsTerminal
	t.Cleanup(func() { stdinIsTerminal = oldTerm })
	stdinIsTerminal = func() bool { return false }
	wantFail(t, 1, "no passphrase", "server", "--data", dir, "backup", filepath.Join(t.TempDir(), "n.age"))
}

// startServe runs `termchat serve` on a free loopback port and returns the
// address and host key it printed, and a stop function that returns Serve's error.
func startServe(t *testing.T, dir string, extra ...string) (addr, hostKey string, stop func() error) {
	t.Helper()
	var out, errOut syncBuf
	root := Root()
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(append([]string{"serve", "--data", dir, "--addr", "127.0.0.1:0"}, extra...))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()
	var stopped bool
	stop = func() error {
		cancel()
		select {
		case err := <-done:
			stopped = true
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("serve did not stop")
			return nil
		}
	}
	t.Cleanup(func() {
		if !stopped {
			_ = stop()
		}
	})
	re := regexp.MustCompile(`listening on (\S+)\nhost key (SHA256:\S+)\n`)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if m := re.FindStringSubmatch(out.String()); m != nil {
			return m[1], m[2], stop
		}
		select {
		case err := <-done:
			t.Fatalf("serve exited early: %v\n%s", err, errOut.String())
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatalf("serve printed nothing useful: %q", out.String())
	return "", "", nil
}

func TestInitThenServeThenRedeem(t *testing.T) {
	clearEnv(t)
	dir := filepath.Join(t.TempDir(), "data")
	out := mustRun(t, "server", "init", "--data", dir, "--handle", "ann")
	code := regexp.MustCompile(`--invite ([A-Z2-7]{10})`).FindStringSubmatch(out)[1]

	addr, hostKey, stop := startServe(t, dir)
	_, key, err := ed25519Signer()
	if err != nil {
		t.Fatal(err)
	}
	client, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User: "termchat",
		Auth: []ssh.AuthMethod{ssh.PublicKeys(key)},
		HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
			if got := ssh.FingerprintSHA256(k); got != hostKey {
				return errors.New("host key " + got + " differs from the printed " + hostKey)
			}
			return nil
		},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ch, reqs, err := client.OpenChannel("termchat", nil)
	if err != nil {
		t.Fatal(err)
	}
	go ssh.DiscardRequests(reqs)
	c := proto.NewCodec(ch)
	roundTrip := func(id uint64, typ string, payload any) proto.Frame {
		b, _ := json.Marshal(payload)
		if err := c.Write(proto.Frame{ID: id, Type: typ, Payload: b}); err != nil {
			t.Fatal(err)
		}
		f, err := c.Read()
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	if f := roundTrip(1, proto.TypeHello, proto.HelloReq{Version: proto.Version}); f.Error != nil {
		t.Fatalf("hello: %v", f.Error)
	}
	f := roundTrip(2, proto.TypeInviteRedeem, proto.InviteRedeemReq{Code: code})
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

	// Admin commands work against the running server's directory.
	if out := mustRun(t, "server", "--data", dir, "key", "list", "ann"); !strings.HasPrefix(out, "SHA256:") {
		t.Fatalf("key list while serving = %q", out)
	}
	if err := stop(); err != nil {
		t.Fatalf("Serve returned %v after cancel, want nil", err)
	}
}

func TestServeRetentionPurgesAtStart(t *testing.T) {
	dir := initServer(t)
	var convID string
	withStore(t, dir, func(st *server.Store) {
		r, err := st.CreateRoom("general", true, "ann")
		if err != nil {
			t.Fatal(err)
		}
		convID = r.ID
		if _, _, err := st.PostMessage(r.ID, "ann", "m1", model.Body{Format: "text/plain", Data: []byte("old news")}, nil); err != nil {
			t.Fatal(err)
		}
	})
	time.Sleep(5 * time.Millisecond)
	_, _, stop := startServe(t, dir, "--retention", "1ms")
	deadline := time.Now().Add(5 * time.Second)
	for {
		var blanked bool
		withStore(t, dir, func(st *server.Store) {
			evs, err := st.After(convID, 0)
			if err != nil {
				t.Fatal(err)
			}
			blanked = len(evs) == 2 && evs[1].Body == nil
		})
		if blanked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("message body was not purged at start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}

func TestServeKeepsEverythingWithoutRetention(t *testing.T) {
	dir := initServer(t)
	var convID string
	withStore(t, dir, func(st *server.Store) {
		r, _ := st.CreateRoom("general", true, "ann")
		convID = r.ID
		if _, _, err := st.PostMessage(r.ID, "ann", "m1", model.Body{Format: "text/plain", Data: []byte("kept")}, nil); err != nil {
			t.Fatal(err)
		}
	})
	_, _, stop := startServe(t, dir)
	time.Sleep(200 * time.Millisecond)
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	withStore(t, dir, func(st *server.Store) {
		evs, _ := st.After(convID, 0)
		if len(evs) != 2 || evs[1].Body == nil || string(evs[1].Body.Data) != "kept" {
			t.Fatalf("events = %+v", evs)
		}
	})
}

func TestServeBindFailureIsAnError(t *testing.T) {
	dir := initServer(t)
	addr, _, stop := startServe(t, dir)
	defer stop()
	dir2 := filepath.Join(t.TempDir(), "d2")
	_, _, err := run(t, "serve", "--data", dir2, "--addr", addr)
	if err == nil || !strings.Contains(err.Error(), "listen") || ExitCode(err) != 1 {
		t.Fatalf("second serve on %s: exit %d, %v", addr, ExitCode(err), err)
	}
	_ = os.Remove(dir2)
}

func ed25519Signer() (ed25519.PublicKey, ssh.Signer, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	s, err := ssh.NewSignerFromKey(priv)
	return pub, s, err
}
