package termchat

import (
	"testing"

	"github.com/jpe0824/termchat/internal/config"
)

func accountFor(server string) config.Account {
	return config.Account{Name: "x", Server: server, Handle: "x"}
}

func TestNormalizeAddr(t *testing.T) {
	for in, want := range map[string]string{
		"chat.example.com":      "chat.example.com:7420",
		"chat.example.com:2222": "chat.example.com:2222",
		"127.0.0.1":             "127.0.0.1:7420",
		"[::1]":                 "[::1]:7420",
		"::1":                   "[::1]:7420",
		"[::1]:9":               "[::1]:9",
		" host ":                "host:7420",
	} {
		if got := normalizeAddr(in); got != want {
			t.Errorf("normalizeAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFileSafe(t *testing.T) {
	if got := fileSafe("[::1]:7420"); got != "[__1]_7420" {
		t.Fatalf("fileSafe = %q", got)
	}
	if got := fileSafe(`a:b/c\d`); got != "a_b_c_d" {
		t.Fatalf("fileSafe = %q", got)
	}
}
