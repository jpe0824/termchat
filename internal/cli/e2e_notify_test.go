package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// quietConfig is every simulated person's config.toml: test traffic must
// never reach the desktop through an OS notification or a hook. The
// background process reads the file once when it starts, so it has to exist
// before the person's first command.
const quietConfig = `[notify]
os = false
hook = ""
`

// checkQuietConfig reports why the config.toml at path would let a
// background process post an OS notification or run a hook. A missing os
// key counts, since it defaults to true. os is decoded loosely because the
// setting is not limited to a boolean; only false is quiet.
func checkQuietConfig(path string) error {
	var c struct {
		Notify struct {
			OS   any    `toml:"os"`
			Hook string `toml:"hook"`
		} `toml:"notify"`
	}
	if _, err := toml.DecodeFile(path, &c); err != nil {
		return err
	}
	if c.Notify.OS == nil {
		return errors.New("notify.os is missing, so the OS route is on")
	}
	if c.Notify.OS != false {
		return fmt.Errorf("notify.os = %#v, want false", c.Notify.OS)
	}
	if c.Notify.Hook != "" {
		return fmt.Errorf("notify.hook = %q, want none", c.Notify.Hook)
	}
	return nil
}

func TestCheckQuietConfig(t *testing.T) {
	account := "\n[[account]]\nname = \"a\"\nserver = \"127.0.0.1:7420\"\nhandle = \"alice\"\n"
	cases := []struct {
		name, file string
		write      bool
		want       string // substring of the error; "" means nil
		notExist   bool
	}{
		{name: "quiet", file: quietConfig, write: true},
		{name: "quiet after join", file: quietConfig + account, write: true},
		{name: "no file", notExist: true},
		{name: "no notify section", file: "[cli]\nsend = \"allow\"\n", write: true, want: "notify.os is missing"},
		{name: "os missing", file: "[notify]\nhook = \"\"\n", write: true, want: "notify.os is missing"},
		{name: "os true", file: "[notify]\nos = true\n", write: true, want: "notify.os = true"},
		{name: "os always", file: "[notify]\nos = \"always\"\n", write: true, want: `notify.os = "always"`},
		{name: "hook set", file: "[notify]\nos = false\nhook = \"/bin/echo\"\n", write: true, want: `notify.hook = "/bin/echo"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if c.write {
				if err := os.WriteFile(path, []byte(c.file), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := checkQuietConfig(path)
			switch {
			case c.notExist:
				if !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("err = %v, want fs.ErrNotExist", err)
				}
			case c.want == "":
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
			default:
				if err == nil || !strings.Contains(err.Error(), c.want) {
					t.Fatalf("err = %v, want it to contain %q", err, c.want)
				}
			}
		})
	}
}
