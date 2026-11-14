package store

import (
	"errors"
	"testing"
)

func TestCursorPassesSkippedSeq(t *testing.T) {
	s := openTest(t)
	must(t, s.Apply(posted("c1", 1, "m1", "ann", "a"), posted("c1", 2, "m2", "ann", "b"),
		posted("c1", 4, "m4", "ann", "d")))
	if got := s.Cursor("c1"); got != 2 {
		t.Fatalf("Cursor before skip = %d, want 2", got)
	}
	must(t, s.Skip("c1", 3))
	if got := s.Cursor("c1"); got != 4 {
		t.Fatalf("Cursor after skipping 3 = %d, want 4", got)
	}
	if got := len(msgs(t, s, "c1")); got != 3 {
		t.Fatalf("messages = %d, want 3 (a skip holds no message)", got)
	}
}

func TestSkippedSeqCountsForOldestSeq(t *testing.T) {
	s := openTest(t)
	must(t, s.Apply(posted("c1", 2, "m2", "ann", "b")))
	must(t, s.Skip("c1", 1))
	if s.OldestSeq("c1") != 1 || s.Cursor("c1") != 2 {
		t.Fatalf("oldest %d cursor %d, want 1 / 2", s.OldestSeq("c1"), s.Cursor("c1"))
	}
	if s.OldestSeq("c2") != 0 || s.Cursor("c2") != 0 {
		t.Fatal("skip leaked across conversations")
	}
}

func TestValidEventReplacesSkip(t *testing.T) {
	s := openTest(t)
	must(t, s.Apply(posted("c1", 1, "m1", "ann", "a")))
	must(t, s.Skip("c1", 2))
	must(t, s.Apply(posted("c1", 2, "m2", "ann", "now valid")))
	if n := count(t, s, "skipped"); n != 0 {
		t.Fatalf("skipped rows after the real event arrived = %d, want 0", n)
	}
	if got := msgs(t, s, "c1"); len(got) != 2 || got[1].Text != "now valid" {
		t.Fatalf("messages = %+v", got)
	}
	if s.Cursor("c1") != 2 {
		t.Fatalf("Cursor = %d, want 2", s.Cursor("c1"))
	}
}

func TestSkipOfHeldSeqIsNoOp(t *testing.T) {
	s := openTest(t)
	must(t, s.Apply(posted("c1", 1, "m1", "ann", "a")))
	must(t, s.Skip("c1", 1))
	if n := count(t, s, "skipped"); n != 0 {
		t.Fatalf("skipped rows = %d, want 0 for a held seq", n)
	}
	for _, bad := range []struct {
		conv string
		seq  int64
	}{{"", 1}, {"c1", 0}} {
		if err := s.Skip(bad.conv, bad.seq); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Skip(%q, %d) = %v, want ErrInvalid", bad.conv, bad.seq, err)
		}
	}
}

func TestSkipSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, "acct")
	must(t, err)
	must(t, s.Apply(posted("c1", 1, "m1", "ann", "a"), posted("c1", 3, "m3", "ann", "c")))
	must(t, s.Skip("c1", 2))
	must(t, s.Close())
	s, err = Open(dir, "acct")
	must(t, err)
	defer s.Close()
	if got := s.Cursor("c1"); got != 3 {
		t.Fatalf("Cursor after reopen = %d, want 3", got)
	}
}
