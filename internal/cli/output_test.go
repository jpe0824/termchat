package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// JSON output is valid JSON with the C1 controls and DEL from another person's
// text escaped, so a terminal that honours 8-bit controls never receives one
// raw; decoding gives back the same value.
func TestJSONEscapesC1AndDEL(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().Bool("json", true, "")
	var out bytes.Buffer
	cmd.SetOut(&out)
	text := "a\u009b31mb\x7fc\u0085d\u0080\u009fé✓<&>"
	if err := emit(cmd, map[string]string{"text": text}, func(io.Writer) error { return nil }); err != nil {
		t.Fatal(err)
	}
	raw := out.Bytes()
	for _, r := range string(raw) {
		if r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			t.Fatalf("raw control U+%04X in %q", r, raw)
		}
	}
	want := `{"text":"a\u009b31mb\u007fc\u0085d\u0080\u009fé✓<&>"}` + "\n"
	if string(raw) != want {
		t.Fatalf("output %q, want %q", raw, want)
	}
	var back map[string]string
	if err := json.Unmarshal(raw, &back); err != nil || back["text"] != text {
		t.Fatalf("decoded %q, %v; want %q", back["text"], err, text)
	}
	if !strings.HasSuffix(string(raw), "\n") {
		t.Fatal("no trailing newline")
	}
}
