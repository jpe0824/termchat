package notify

import (
	"strings"
	"testing"
)

// Every field a notification route receives is cleaned, the conversation id
// included, although the server chooses it.
func TestCleanCoversEveryTextField(t *testing.T) {
	dirty := "x\x1b]0;owned\x07\u009b31m\ny"
	got := clean(Notification{ConvID: dirty, ConvName: dirty, Sender: dirty, Text: dirty})
	for name, v := range map[string]string{"ConvID": got.ConvID, "ConvName": got.ConvName, "Sender": got.Sender, "Text": got.Text} {
		if strings.ContainsAny(v, "\x1b\x07\u009b") {
			t.Errorf("%s = %q still holds a control", name, v)
		}
	}
}
