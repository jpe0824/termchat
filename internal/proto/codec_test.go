package proto

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/jpe0824/termchat/internal/model"
)

type buf struct{ bytes.Buffer }

func TestCodecRoundTrip(t *testing.T) {
	var b buf
	c := NewCodec(&b)
	want := Frame{ID: 7, Type: TypeHello, Payload: json.RawMessage(`{"version":1}`)}
	if err := c.Write(want); err != nil {
		t.Fatal(err)
	}
	got, err := c.Read()
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID || got.Type != want.Type || !bytes.Equal(got.Payload, want.Payload) || got.Error != nil {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestCodecErrorFrame(t *testing.T) {
	var b buf
	c := NewCodec(&b)
	if err := c.Write(Frame{ID: 1, Type: TypeMsgSend, Error: &Error{Code: CodeForbidden, Message: "no"}}); err != nil {
		t.Fatal(err)
	}
	got, err := c.Read()
	if err != nil {
		t.Fatal(err)
	}
	if got.Error == nil || got.Error.Code != CodeForbidden || got.Error.Message != "no" {
		t.Fatalf("got %+v", got)
	}
}

func TestCodecRejectsOversize(t *testing.T) {
	line := strings.Repeat("x", MaxFrame+1) + "\n"
	c := NewCodec(&buf{*bytes.NewBufferString(line)})
	if _, err := c.Read(); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err = %v, want ErrFrameTooLarge", err)
	}
}

func TestCodecOversizeWithoutNewline(t *testing.T) {
	c := NewCodec(&buf{*bytes.NewBufferString(strings.Repeat("x", MaxFrame*3))})
	if _, err := c.Read(); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err = %v, want ErrFrameTooLarge", err)
	}
}

func TestCodecMaxSizeFrameAccepted(t *testing.T) {
	pad := MaxFrame - len(`{"id":1,"type":"x","payload":""}`)
	line := `{"id":1,"type":"x","payload":"` + strings.Repeat("a", pad) + `"}`
	if len(line) != MaxFrame {
		t.Fatalf("setup: len %d", len(line))
	}
	c := NewCodec(&buf{*bytes.NewBufferString(line + "\n")})
	if _, err := c.Read(); err != nil {
		t.Fatalf("err = %v", err)
	}
}

func TestCodecReadsSeveralFramesAndEOF(t *testing.T) {
	var b buf
	c := NewCodec(&b)
	for i := uint64(1); i <= 3; i++ {
		if err := c.Write(Frame{ID: i, Type: TypePeopleList}); err != nil {
			t.Fatal(err)
		}
	}
	for i := uint64(1); i <= 3; i++ {
		f, err := c.Read()
		if err != nil || f.ID != i {
			t.Fatalf("frame %d: %+v, %v", i, f, err)
		}
	}
	if _, err := c.Read(); err == nil {
		t.Fatal("want EOF")
	}
}

func TestCodecWriteRejectsOversize(t *testing.T) {
	var b buf
	c := NewCodec(&b)
	big := json.RawMessage(`"` + strings.Repeat("a", MaxFrame) + `"`)
	if err := c.Write(Frame{ID: 1, Type: "x", Payload: big}); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err = %v, want ErrFrameTooLarge", err)
	}
	if b.Len() != 0 {
		t.Fatal("oversize frame was partially written")
	}
}

func TestCodecConcurrentWrite(t *testing.T) {
	var b buf
	c := NewCodec(&b)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := c.Write(Frame{ID: uint64(i + 1), Type: TypePeopleList}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	n := 0
	for {
		if _, err := c.Read(); err != nil {
			break
		}
		n++
	}
	if n != 50 {
		t.Fatalf("read %d frames, want 50", n)
	}
}

func TestBodyIsOpaque(t *testing.T) {
	data := []byte{0x00, 0xff, '\n'}
	req := MsgSendReq{Conv: "c1", MsgID: "m1", Body: model.Body{Format: "text/plain", Data: data}, Mentions: []string{"dana"}}
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var b buf
	c := NewCodec(&b)
	if err := c.Write(Frame{ID: 1, Type: TypeMsgSend, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	f, err := c.Read()
	if err != nil {
		t.Fatal(err)
	}
	var got MsgSendReq
	if err := json.Unmarshal(f.Payload, &got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Body.Data, data) || !reflect.DeepEqual(got.Mentions, req.Mentions) {
		t.Fatalf("got %+v", got)
	}
}

func TestPayloadTags(t *testing.T) {
	b, _ := json.Marshal(HelloRes{Version: Version})
	if string(b) != `{"version":1}` {
		t.Fatalf("HelloRes = %s", b)
	}
	b, _ = json.Marshal(ConvHistoryReq{Conv: "c", BeforeSeq: 5, Limit: 10})
	if string(b) != `{"conv":"c","before_seq":5,"limit":10}` {
		t.Fatalf("ConvHistoryReq = %s", b)
	}
	b, _ = json.Marshal(model.Event{ConvID: "c", Seq: 1, Kind: model.MessageDeleted})
	if strings.Contains(string(b), "body") {
		t.Fatalf("nil body must be omitted: %s", b)
	}
}
