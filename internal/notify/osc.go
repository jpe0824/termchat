package notify

import (
	"strings"
	"unicode"
)

// headline is the notification's title: the sender for a direct message,
// "sender in #room" for a room.
func headline(n Notification) string {
	if n.DM {
		return n.Sender
	}
	return n.Sender + " in #" + n.ConvName
}

// OSC returns the terminal escape sequence that raises n in a terminal that
// understands variant: "9" (iTerm2, WezTerm, Ghostty), "99" (kitty), "777"
// (VTE) or "7501" (Herdr). An unknown variant returns "". Every field is stripped of control
// characters and of the ';' that separates fields, so message text cannot end
// or split the sequence.
func OSC(variant string, n Notification) string {
	title, sender, text := oscField(headline(n)), oscField(n.Sender), oscField(n.Text)
	switch variant {
	case "9":
		return "\x1b]9;" + sender + ": " + text + "\x07"
	case "777":
		return "\x1b]777;notify;" + title + ";" + text + "\x07"
	case "99":
		return "\x1b]99;i=tc:d=0;" + title + "\x1b\\" +
			"\x1b]99;i=tc:d=1:p=body;" + text + "\x1b\\"
	case "7501":
		return herdrWorking
	}
	return ""
}

// herdrWorking and HerdrDone report the program's status to Herdr. Herdr
// raises its alert (sound, note, agent status) when a program reaches done
// while the person is not looking at it, and only when the status changes.
// OSC("7501") is the working report; HerdrDone must follow it in a separate,
// later write, or a pane still done from an earlier message shows no change.
// Neither carries message text. Herdr turns done back to idle itself once the
// person looks.
const herdrWorking = "\x1b]7501;app=termchat:state=working\x07"

// HerdrDone is the done report that follows OSC("7501").
const HerdrDone = "\x1b]7501;app=termchat:state=done\x07"

// oscField makes s safe inside an OSC payload. Line breaks and tabs become
// spaces, other control characters (C0, DEL and C1, which includes the 8-bit
// string terminator) are dropped, and ';' becomes ','.
func oscField(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case r == ';':
			return ','
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, s)
}

// DetectOSC picks the escape sequence variant the terminal named by env
// understands, or "" when it is unknown. env is os.Getenv in production.
func DetectOSC(env func(string) string) string {
	switch {
	case env("HC_TERMINAL_ID") != "":
		return "7501"
	case env("KITTY_WINDOW_ID") != "":
		return "99"
	}
	switch env("TERM_PROGRAM") {
	case "iTerm.app", "WezTerm", "ghostty":
		return "9"
	}
	if env("VTE_VERSION") != "" {
		return "777"
	}
	return ""
}

// ResolveOSC applies the configured notify.osc value: "auto" (or empty)
// detects, "off" and anything unrecognised disable, and "9", "99", "777" and
// "7501" are used as given.
func ResolveOSC(cfg string, env func(string) string) string {
	switch cfg {
	case "", "auto":
		return DetectOSC(env)
	case "9", "99", "777", "7501":
		return cfg
	}
	return ""
}
