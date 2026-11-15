package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/server"
	"github.com/jpe0824/termchat/internal/testutil"
)

// binDir holds the one termchat binary every end-to-end test runs. Each
// simulated person gets a private copy so that a process listing tells their
// background processes apart.
var binDir string

// runRoot is this test binary's own directory under /tmp. Every simulated
// person lives under it, so the stray-process check sees only this run's
// processes and never another run's.
var runRoot string

func TestMain(m *testing.M) {
	code := 0
	if runtime.GOOS != "windows" {
		dir, err := os.MkdirTemp("", "termchat-bin")
		if err != nil {
			fmt.Fprintln(os.Stderr, "mkdir:", err)
			os.Exit(1)
		}
		binDir = dir
		// Unix socket paths are limited to about 100 bytes.
		root, err := os.MkdirTemp("/tmp", "tcr")
		if err != nil {
			fmt.Fprintln(os.Stderr, "mkdir:", err)
			os.Exit(1)
		}
		runRoot = root
		build := exec.Command("go", "build", "-o", filepath.Join(dir, "termchat"), "../../cmd/termchat")
		build.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := build.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "build termchat: %v\n%s", err, out)
			os.Exit(1)
		}
	}
	code = m.Run()
	if binDir != "" {
		if pids := strayDaemons(); len(pids) > 0 {
			fmt.Fprintf(os.Stderr, "background processes outlived the tests: %v\n", pids)
			for _, pid := range pids {
				if p, err := os.FindProcess(pid); err == nil {
					_ = p.Kill()
				}
			}
			code = 1
		}
		_ = os.RemoveAll(binDir)
		_ = os.RemoveAll(runRoot)
	}
	os.Exit(code)
}

// strayDaemons lists the background processes of any person of this run.
func strayDaemons() []int {
	return daemonPIDs(regexp.QuoteMeta(runRoot) + "/[^/ ]+")
}

// daemonPIDs lists live "daemon run" processes whose binary is
// <person dir>/b/termchat, where the person directory matches dirRE. The match
// is not anchored because the process may report /tmp as /private/tmp.
func daemonPIDs(dirRE string) []int {
	out, _ := exec.Command("pgrep", "-f", dirRE+"/b/termchat daemon run").Output()
	var pids []int
	for _, f := range strings.Fields(string(out)) {
		var pid int
		if _, err := fmt.Sscan(f, &pid); err == nil && pid != os.Getpid() {
			pids = append(pids, pid)
		}
	}
	return pids
}

func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the background process is not supported on Windows")
	}
}

// testServer is an in-process server that can be stopped and brought back on
// the same address with the same data.
type testServer struct {
	*testutil.Harness
	dir, addr string
}

func startServer(t *testing.T) *testServer {
	t.Helper()
	skipOnWindows(t)
	dir := t.TempDir()
	h := testutil.StartServerWith(t, func(o *server.Options) { o.DataDir = dir })
	return &testServer{Harness: h, dir: dir, addr: h.Server.Addr().String()}
}

// restart stops the server and starts another on the same address and data.
func (s *testServer) restart(t *testing.T) {
	t.Helper()
	if err := s.StopServer(); err != nil {
		t.Fatalf("stop server: %v", err)
	}
	s.start(t)
}

func (s *testServer) start(t *testing.T) {
	t.Helper()
	var srv *server.Server
	var err error
	for range 50 {
		if srv, err = server.New(server.Options{Addr: s.addr, DataDir: s.dir}); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("restart server: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = srv.Serve(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

// person is one simulated user: private config, data and runtime
// directories, and their own copy of the binary.
type person struct {
	t    *testing.T
	bin  string
	dir  string // the person's directory, under runRoot
	env  []string
	run  string
	cfg  string // config directory
	data string // data directory
}

func newPerson(t *testing.T) *person {
	t.Helper()
	skipOnWindows(t)
	dir, err := os.MkdirTemp(runRoot, "p")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"c", "d", "r", "b"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(dir, "b", "termchat")
	src, err := os.ReadFile(filepath.Join(binDir, "termchat"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, src, 0o700); err != nil {
		t.Fatal(err)
	}
	p := &person{t: t, bin: bin, dir: dir, run: filepath.Join(dir, "r"),
		cfg: filepath.Join(dir, "c"), data: filepath.Join(dir, "d")}
	p.env = append(os.Environ(),
		"TERMCHAT_CONFIG_DIR="+filepath.Join(dir, "c"),
		"TERMCHAT_DATA_DIR="+filepath.Join(dir, "d"),
		"TERMCHAT_RUNTIME_DIR="+p.run,
	)
	t.Cleanup(func() {
		p.exec("", "daemon", "stop")
		deadline := time.Now().Add(5 * time.Second)
		for len(p.daemons()) > 0 && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if pids := p.daemons(); len(pids) > 0 {
			for _, pid := range pids {
				if pr, err := os.FindProcess(pid); err == nil {
					_ = pr.Kill()
				}
			}
			t.Errorf("background process still alive after daemon stop: %v", pids)
		}
		_ = os.RemoveAll(dir)
	})
	return p
}

// daemons returns the pids of this person's background processes.
func (p *person) daemons() []int { return daemonPIDs(regexp.QuoteMeta(p.dir)) }

type result struct {
	out, err string
	code     int
}

func (p *person) command(stdin string, args ...string) *exec.Cmd {
	cmd := exec.Command(p.bin, args...)
	cmd.Env = p.env
	cmd.Stdin = strings.NewReader(stdin)
	return cmd
}

func (p *person) exec(stdin string, args ...string) result {
	p.t.Helper()
	cmd := p.command(stdin, args...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Start(); err != nil {
		p.t.Fatalf("start termchat %v: %v", args, err)
	}
	timer := time.AfterFunc(60*time.Second, func() { _ = cmd.Process.Kill() })
	defer timer.Stop()
	err := cmd.Wait()
	res := result{out: out.String(), err: errOut.String()}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		res.code = ee.ExitCode()
	default:
		p.t.Fatalf("termchat %v: %v", args, err)
	}
	return res
}

// do runs termchat with args and returns the result.
func (p *person) do(args ...string) result {
	p.t.Helper()
	return p.exec("", args...)
}

// ok runs termchat, requires exit 0 and an empty stderr, and returns stdout.
func (p *person) ok(args ...string) string {
	p.t.Helper()
	r := p.exec("", args...)
	if r.code != 0 || r.err != "" {
		p.t.Fatalf("termchat %s: exit %d, stderr %q, stdout %q; want exit 0 and no stderr", strings.Join(args, " "), r.code, r.err, r.out)
	}
	return r.out
}

// fail requires exit code and the exact one-line stderr "termchat: <msg>",
// and an empty stdout.
func (r result) fail(t *testing.T, code int, msg string) {
	t.Helper()
	if r.code != code || r.err != "termchat: "+msg+"\n" || r.out != "" {
		t.Fatalf("exit %d, stderr %q, stdout %q; want exit %d, stderr %q, empty stdout", r.code, r.err, r.out, code, "termchat: "+msg+"\n")
	}
}

// failContains is fail with a stderr that only has to contain want.
func (r result) failContains(t *testing.T, code int, want string) {
	t.Helper()
	if r.code != code || !strings.HasPrefix(r.err, "termchat: ") || strings.Count(r.err, "\n") != 1 ||
		!strings.Contains(r.err, want) || r.out != "" {
		t.Fatalf("exit %d, stderr %q, stdout %q; want exit %d, one stderr line containing %q, empty stdout", r.code, r.err, r.out, code, want)
	}
}

// table runs a command in this process with stdout reported as a terminal,
// against this person's background process (which must already be running),
// so that the table output can be checked. It returns stdout, stderr and the
// error from Execute.
func (p *person) table(args ...string) (string, string, error) {
	p.t.Helper()
	for _, kv := range p.env {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "TERMCHAT_") {
			p.t.Setenv(k, v)
		}
	}
	oldTerm, oldStart := isTerminal, ipc.StartDaemon
	isTerminal = func(io.Writer) bool { return true }
	ipc.StartDaemon = func(context.Context) error {
		p.t.Error("a table-mode command tried to start a background process")
		return errors.New("not allowed")
	}
	defer func() { isTerminal, ipc.StartDaemon = oldTerm, oldStart }()
	var out, errOut bytes.Buffer
	root := Root()
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), errOut.String(), err
}

// tableOK is table that requires success and returns stdout.
func (p *person) tableOK(args ...string) string {
	p.t.Helper()
	out, errOut, err := p.table(args...)
	if err != nil || errOut != "" {
		p.t.Fatalf("termchat %s: %v, stderr %q", strings.Join(args, " "), err, errOut)
	}
	return out
}

func decode(t *testing.T, s string, into any) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		t.Fatalf("more than one JSON value in %q", s)
	}
}

// jsonOf runs termchat --json and decodes stdout into a generic value.
func (p *person) jsonOf(args ...string) any {
	p.t.Helper()
	var v any
	out := p.ok(append(args, "--json")...)
	decode(p.t, out, &v)
	return v
}

// msgView is the stable JSON shape of one message.
type msgView struct {
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
	Status   string   `json:"status"`
	Error    string   `json:"error"`
}

func (p *person) messages(args ...string) []msgView {
	p.t.Helper()
	var ms []msgView
	decode(p.t, p.ok(append(args, "--json")...), &ms)
	return ms
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// joinAs registers p on s as handle: the first person on a server becomes
// its admin, later ones are invited.
func (s *testServer) joinAs(t *testing.T, p *person, handle, name string) {
	t.Helper()
	var code string
	var err error
	if n, _ := s.Store.PersonCount(); n == 0 {
		code, err = server.InitFirstAdmin(s.Store, handle, name)
	} else {
		code, _, err = server.InviteHandle(s.Store, handle, name)
	}
	if err != nil {
		t.Fatal(err)
	}
	p.ok("join", s.addr, "--invite", code)
	eventually(t, handle+" to connect", func() bool {
		var st struct{ State string }
		r := p.do("status", "--json")
		return r.code == 0 && json.Unmarshal([]byte(r.out), &st) == nil && st.State == "connected"
	})
}

// pair returns a server with alice (admin) and bob joined and connected.
func pair(t *testing.T) (*testServer, *person, *person) {
	t.Helper()
	s := startServer(t)
	a, b := newPerson(t), newPerson(t)
	s.joinAs(t, a, "alice", "Alice")
	s.joinAs(t, b, "bob", "Bob")
	return s, a, b
}

// chat opens alice's DM with bob, returning its id.
func chatID(t *testing.T, p *person, other string) string {
	t.Helper()
	var c struct{ ID string }
	out := p.ok("dm", other, "--json")
	if err := json.Unmarshal([]byte(out), &c); err != nil || c.ID == "" {
		t.Fatalf("dm %s: %q: %v", other, out, err)
	}
	return c.ID
}

// ipcDo runs fn on a connection to this person's running background
// process, made from this process.
func (p *person) ipcDo(fn func(c *ipc.Client) error) {
	p.t.Helper()
	for _, kv := range p.env {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "TERMCHAT_") {
			p.t.Setenv(k, v)
		}
	}
	old := ipc.StartDaemon
	ipc.StartDaemon = func(context.Context) error { return errors.New("not allowed") }
	defer func() { ipc.StartDaemon = old }()
	c, err := ipc.Dial(context.Background())
	if err != nil {
		p.t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if err := fn(c); err != nil {
		p.t.Fatal(err)
	}
}

func (p *person) edit(conv, msgID, text string) {
	p.t.Helper()
	p.ipcDo(func(c *ipc.Client) error { return c.Edit(conv, msgID, text) })
}

func (p *person) delete(conv, msgID string) {
	p.t.Helper()
	p.ipcDo(func(c *ipc.Client) error { return c.Delete(conv, msgID) })
}
