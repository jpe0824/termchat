// Package paths resolves the directories termchat stores state in.
package paths

import (
	"os"
	"path/filepath"
)

const appDir = "termchat"

// Config returns the directory holding config.toml.
func Config() (string, error) {
	return ensure(resolve("TERMCHAT_CONFIG_DIR", func() string {
		base, err := os.UserConfigDir()
		if err != nil {
			base = filepath.Join(home(), ".config")
		}
		return filepath.Join(base, appDir)
	}))
}

// Data returns the directory holding the local message store and keys.
func Data() (string, error) {
	return ensure(dataDir())
}

// Runtime returns the directory holding sockets and pid files. It falls back
// to a subdirectory of Data when XDG_RUNTIME_DIR is unset.
func Runtime() (string, error) {
	return ensure(resolve("TERMCHAT_RUNTIME_DIR", func() string {
		if x := os.Getenv("XDG_RUNTIME_DIR"); x != "" {
			return filepath.Join(x, appDir)
		}
		return filepath.Join(dataDir(), "run")
	}))
}

func dataDir() string {
	return resolve("TERMCHAT_DATA_DIR", func() string {
		if x := os.Getenv("XDG_DATA_HOME"); x != "" {
			return filepath.Join(x, appDir)
		}
		return filepath.Join(home(), ".local", "share", appDir)
	})
}

func resolve(env string, def func() string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	return def()
}

func home() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "."
}

// ensure creates dir with mode 0700 and returns it. A directory that cannot
// be created is an error, never a path to a directory that does not exist.
func ensure(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}
