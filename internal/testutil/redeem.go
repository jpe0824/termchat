package testutil

import (
	"encoding/json"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/proto"
)

// RedeemAt connects to the server at addr with key, accepting only a host key
// that hostKey approves, says hello and redeems code. It returns the person the
// key now belongs to. It is for tests of a server this package did not start.
func RedeemAt(t *testing.T, addr string, hostKey ssh.HostKeyCallback, key ssh.Signer, code string) model.Person {
	t.Helper()
	client, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            "termchat",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(key)},
		HostKeyCallback: hostKey,
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ch, reqs, err := client.OpenChannel("termchat", nil)
	if err != nil {
		t.Fatalf("open channel: %v", err)
	}
	go ssh.DiscardRequests(reqs)
	c := proto.NewCodec(ch)
	Hello(t, c)
	f := Request(t, c, proto.TypeInviteRedeem, proto.InviteRedeemReq{Code: code})
	if f.Error != nil {
		t.Fatalf("redeem: %v", f.Error)
	}
	var res proto.InviteRedeemRes
	if err := json.Unmarshal(f.Payload, &res); err != nil {
		t.Fatalf("decode redeem: %v", err)
	}
	return res.Person
}
