package notify

import (
	"strings"
	"testing"
)

func TestOSCSequences(t *testing.T) {
	dm := Notification{Sender: "dana", Text: "hi", DM: true, ConvName: "dana"}
	room := Notification{Sender: "dana", Text: "hi", ConvName: "ops"}
	tests := []struct {
		variant string
		n       Notification
		want    string
	}{
		{"9", dm, "\x1b]9;dana: hi\x07"},
		{"9", room, "\x1b]9;dana: hi\x07"},
		{"777", dm, "\x1b]777;notify;dana;hi\x07"},
		{"777", room, "\x1b]777;notify;dana in #ops;hi\x07"},
		{"99", dm, "\x1b]99;i=tc:d=0;dana\x1b\\\x1b]99;i=tc:d=1:p=body;hi\x1b\\"},
		{"99", room, "\x1b]99;i=tc:d=0;dana in #ops\x1b\\\x1b]99;i=tc:d=1:p=body;hi\x1b\\"},
		{"7501", dm, "\x1b]7501;app=termchat:state=working\x07"},
		{"7501", room, "\x1b]7501;app=termchat:state=working\x07"},
		{"1", dm, ""},
		{"", dm, ""},
		{"off", dm, ""},
	}
	for _, tt := range tests {
		if got := OSC(tt.variant, tt.n); got != tt.want {
			t.Errorf("OSC(%q, %+v) = %q, want %q", tt.variant, tt.n, got, tt.want)
		}
	}
}

func TestOSCStripsControlChars(t *testing.T) {
	hostile := Notification{
		Sender:   "da\x1bna;x",
		ConvName: "o\x07ps",
		Text:     "a\x07b\x1b]9;pwned\x07c\x1b\\d;e\nf\r\x00g\u009cz\x7f",
	}
	for _, variant := range []string{"9", "99", "777"} {
		for _, dm := range []bool{true, false} {
			n := hostile
			n.DM = dm
			got := OSC(variant, n)
			// Exactly the terminators the builder wrote may remain.
			wantESC, wantBEL := 0, 0
			switch variant {
			case "9", "777":
				wantBEL = 1
			case "99":
				wantESC = 4 // two sequences, each ESC ] ... ESC \
			}
			if wantESC == 0 {
				wantESC = 1
			}
			if c := strings.Count(got, "\x1b"); c != wantESC {
				t.Errorf("variant %s dm=%v: %d ESC bytes in %q, want %d", variant, dm, c, got, wantESC)
			}
			if c := strings.Count(got, "\x07"); c != wantBEL {
				t.Errorf("variant %s dm=%v: %d BEL bytes in %q, want %d", variant, dm, c, got, wantBEL)
			}
			for _, r := range got {
				if r < 0x20 && r != 0x1b && r != 0x07 || r == 0x7f || r >= 0x80 && r <= 0x9f {
					t.Errorf("variant %s: control %U survived in %q", variant, r, got)
				}
			}
			if strings.Contains(got, "pwned\x07") || strings.Contains(got, "\x1b]9;pwned") {
				t.Errorf("variant %s: injected sequence survived in %q", variant, got)
			}
		}
	}
	// Field separators inside a field cannot split it.
	if got, want := OSC("777", Notification{Sender: "a;b", Text: "c;d", DM: true}), "\x1b]777;notify;a,b;c,d\x07"; got != want {
		t.Errorf("OSC 777 with separators = %q, want %q", got, want)
	}
	if got, want := OSC("9", Notification{Sender: "a\x1b\\b", Text: "x\ny"}), "\x1b]9;a\\b: x y\x07"; got != want {
		t.Errorf("OSC 9 = %q, want %q", got, want)
	}
}

func TestDetectOSC(t *testing.T) {
	envOf := func(kv ...string) func(string) string {
		m := map[string]string{}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return func(k string) string { return m[k] }
	}
	tests := []struct {
		name string
		env  func(string) string
		want string
	}{
		{"kitty", envOf("KITTY_WINDOW_ID", "3"), "99"},
		{"herdr", envOf("HC_TERMINAL_ID", "t1"), "7501"},
		{"herdr beats kitty", envOf("HC_TERMINAL_ID", "t1", "KITTY_WINDOW_ID", "3"), "7501"},
		{"kitty beats term program", envOf("KITTY_WINDOW_ID", "3", "TERM_PROGRAM", "iTerm.app"), "99"},
		{"iterm", envOf("TERM_PROGRAM", "iTerm.app"), "9"},
		{"wezterm", envOf("TERM_PROGRAM", "WezTerm"), "9"},
		{"ghostty", envOf("TERM_PROGRAM", "ghostty"), "9"},
		{"vte", envOf("VTE_VERSION", "7003"), "777"},
		{"apple terminal", envOf("TERM_PROGRAM", "Apple_Terminal"), ""},
		{"nothing", envOf(), ""},
	}
	for _, tt := range tests {
		if got := DetectOSC(tt.env); got != tt.want {
			t.Errorf("%s: DetectOSC = %q, want %q", tt.name, got, tt.want)
		}
	}
	vte := envOf("VTE_VERSION", "1")
	for cfg, want := range map[string]string{"auto": "777", "off": "", "9": "9", "99": "99", "777": "777", "7501": "7501", "bogus": "", "": "777"} {
		if got := ResolveOSC(cfg, vte); got != want {
			t.Errorf("ResolveOSC(%q) = %q, want %q", cfg, got, want)
		}
	}
	if got := ResolveOSC("9", envOf()); got != "9" {
		t.Errorf("explicit variant ignored: %q", got)
	}
}

func TestOSC7501CarriesNoMessageText(t *testing.T) {
	n := Notification{Sender: "da\x1bna", ConvName: "ops", Text: "secret\x07\x1b]9;x"}
	got := OSC("7501", n) + HerdrDone
	for _, leak := range []string{"secret", "dana", "da\x1bna", "ops"} {
		if strings.Contains(got, leak) {
			t.Errorf("OSC 7501 = %q, carries %q", got, leak)
		}
	}
}

func TestHerdrDone(t *testing.T) {
	if want := "\x1b]7501;app=termchat:state=done\x07"; HerdrDone != want {
		t.Errorf("HerdrDone = %q, want %q", HerdrDone, want)
	}
}
