package daemon

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/jpe0824/termchat/internal/config"
	"github.com/jpe0824/termchat/internal/ipc"
)

// errBrokenFile is what cfgFile.load returns while the file is broken.
var errBrokenFile = errors.New("config.toml: line 3: expected '=' after a key")

// cfgFile is an in-memory config.toml behind Options.LoadConfig and
// Options.SaveConfig.
type cfgFile struct {
	t       *testing.T
	mu      sync.Mutex
	data    []byte
	broken  bool
	saveErr error
	saves   int
}

func newCfgFile(t *testing.T, c *config.Config) *cfgFile {
	f := &cfgFile{t: t}
	f.data = encodeConfig(t, c)
	return f
}

func encodeConfig(t *testing.T, c *config.Config) []byte {
	var b bytes.Buffer
	if err := toml.NewEncoder(&b).Encode(c); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func (f *cfgFile) load() (*config.Config, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.broken {
		return nil, errBrokenFile
	}
	c := &config.Config{}
	if _, err := toml.Decode(string(f.data), c); err != nil {
		return nil, err
	}
	return c, nil
}

func (f *cfgFile) save(c *config.Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveErr != nil {
		return f.saveErr
	}
	f.data = encodeConfig(f.t, c)
	f.saves++
	return nil
}

// bytes returns the file's contents.
func (f *cfgFile) bytes() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.data)
}

// edit changes the file as a person editing it by hand would.
func (f *cfgFile) edit(key, value string) {
	f.t.Helper()
	c, err := f.load()
	if err != nil {
		f.t.Fatal(err)
	}
	if err := c.Set(key, value); err != nil {
		f.t.Fatal(err)
	}
	f.mu.Lock()
	f.data = encodeConfig(f.t, c)
	f.mu.Unlock()
}

// saved decodes the file.
func (f *cfgFile) saved() *config.Config {
	f.t.Helper()
	c, err := f.load()
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

func set(key, value string) func(*config.Config) error {
	return func(c *config.Config) error { return c.Set(key, value) }
}

func TestConfigWriteKeepsDiskEdits(t *testing.T) {
	e := newEnv(t)
	e.start()
	d := e.daemon()
	e.file.edit("splash", "false")
	got, err := d.updateConfig(set("notify.os", "always"))
	if err != nil {
		t.Fatalf("updateConfig: %v", err)
	}
	saved := e.file.saved()
	if saved.Splash || saved.Notify.OS != config.OSAlways {
		t.Fatalf("saved splash=%v os=%v, want false and always", saved.Splash, saved.Notify.OS)
	}
	if d.config() != got {
		t.Fatal("snapshot is not the config updateConfig returned")
	}
	if c := d.config(); c.Splash || c.Notify.OS != config.OSAlways {
		t.Fatalf("snapshot splash=%v os=%v, want false and always", c.Splash, c.Notify.OS)
	}
}

func TestConfigWriteRefusesBrokenFile(t *testing.T) {
	e := newEnv(t)
	e.start()
	d := e.daemon()
	before, snap := e.file.bytes(), d.config()
	e.file.mu.Lock()
	e.file.broken = true
	e.file.mu.Unlock()
	called := false
	_, err := d.updateConfig(func(c *config.Config) error { called = true; return c.Set("splash", "false") })
	if !errors.Is(err, errBrokenFile) || err.Error() != errBrokenFile.Error() {
		t.Fatalf("updateConfig = %v, want %v unchanged", err, errBrokenFile)
	}
	if called {
		t.Error("change ran on a file that could not be read")
	}
	if !bytes.Equal(e.file.bytes(), before) {
		t.Error("file changed")
	}
	if d.config() != snap {
		t.Error("snapshot replaced")
	}
}

func TestConfigSaveFailureKeepsSnapshot(t *testing.T) {
	e := newEnv(t)
	e.start()
	d := e.daemon()
	errDisk := errors.New("disk full")
	before, snap := e.file.bytes(), d.config()
	e.file.mu.Lock()
	e.file.saveErr = errDisk
	e.file.mu.Unlock()
	if _, err := d.updateConfig(set("splash", "false")); !errors.Is(err, errDisk) {
		t.Fatalf("updateConfig = %v, want %v", err, errDisk)
	}
	if !bytes.Equal(e.file.bytes(), before) {
		t.Error("file changed")
	}
	if d.config() != snap {
		t.Error("snapshot replaced")
	}
}

func TestConfigChangeErrorKeepsFileAndSnapshot(t *testing.T) {
	e := newEnv(t)
	e.start()
	d := e.daemon()
	before, snap := e.file.bytes(), d.config()
	if _, err := d.updateConfig(set("notify.os", "loud")); !errors.Is(err, config.ErrBadValue) {
		t.Fatalf("updateConfig = %v, want ErrBadValue", err)
	}
	if !bytes.Equal(e.file.bytes(), before) || d.config() != snap {
		t.Error("a refused change touched the file or the snapshot")
	}
}

func TestJoinKeepsDiskEdits(t *testing.T) {
	e := newEnv(t)
	e.cfg.Accounts = nil
	e.adjust = func(o *Options) {
		o.Join = func(_ context.Context, req ipc.JoinReq) (config.Account, error) {
			return config.Account{Name: "me", Server: req.Server, Handle: "me"}, nil
		}
	}
	c := e.start()
	e.file.edit("mark_read", "false")
	if _, err := c.Join(ipc.JoinReq{Server: "chat.test", Code: "invite-1"}); err != nil {
		t.Fatalf("Join: %v", err)
	}
	saved := e.file.saved()
	want := []config.Account{{Name: "me", Server: "chat.test", Handle: "me"}}
	if !slices.Equal(saved.Accounts, want) || saved.MarkRead {
		t.Fatalf("saved accounts=%+v mark_read=%v, want %+v and false", saved.Accounts, saved.MarkRead, want)
	}
	if got := e.daemon().config(); !slices.Equal(got.Accounts, want) || got.MarkRead {
		t.Fatalf("snapshot accounts=%+v mark_read=%v", got.Accounts, got.MarkRead)
	}
}

func TestJoinSaveFailureKeepsConfig(t *testing.T) {
	e := newEnv(t)
	e.cfg.Accounts = nil
	e.adjust = func(o *Options) {
		o.Join = func(_ context.Context, req ipc.JoinReq) (config.Account, error) {
			return config.Account{Name: "me", Server: req.Server, Handle: "me"}, nil
		}
	}
	c := e.start()
	errDisk := errors.New("disk full")
	e.file.mu.Lock()
	e.file.saveErr = errDisk
	e.file.mu.Unlock()
	_, err := c.Join(ipc.JoinReq{Server: "chat.test", Code: "invite-1"})
	if err == nil || err.Error() != "save config: disk full" {
		t.Fatalf("Join = %v, want save config: disk full", err)
	}
	if got := e.daemon().config().Accounts; len(got) != 0 {
		t.Fatalf("snapshot accounts = %+v, want none", got)
	}
}

func TestDrainDoesNotTakeWriterLock(t *testing.T) {
	e, rec := noteEnv(t)
	started(t, e, dm("d1", "dana"))
	d := e.daemon()
	d.cfgMu.Lock()
	defer d.cfgMu.Unlock()
	push(e, "d1", "dana", "m1", "while writing", 0)
	waitUntil(t, "the notification", func() bool { return slices.Contains(texts(rec.osCalls()), "while writing") })
	msgs, err := e.c.Messages("d1", 0, 10)
	if err != nil || len(msgs) != 1 || msgs[0].Text != "while writing" {
		t.Fatalf("Messages = %+v, %v", msgs, err)
	}
}
