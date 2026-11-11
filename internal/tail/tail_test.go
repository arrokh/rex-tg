package tail

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendTo(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func next(t *testing.T, f *Follower) Batch {
	t.Helper()
	select {
	case b, ok := <-f.Batches():
		if !ok {
			t.Fatalf("batches closed: %v", f.Err())
		}
		return b
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for batch")
	}
	return Batch{}
}

func noBatch(t *testing.T, f *Follower) {
	t.Helper()
	select {
	case b := <-f.Batches():
		t.Fatalf("unexpected batch %+v", b)
	case <-time.After(150 * time.Millisecond):
	}
}

func texts(b Batch) []string {
	var s []string
	for _, l := range b.Lines {
		s = append(s, string(l.Data))
	}
	return s
}

func eq(a, b []string) bool {
	return strings.Join(a, "\x00") == strings.Join(b, "\x00") && len(a) == len(b)
}

var fast = Options{Poll: 20 * time.Millisecond}

func TestReadFromZeroAndOffset(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.jsonl")
	write(t, p, "one\ntwo\nthree\n")
	f := Follow(context.Background(), p, Position{}, fast)
	defer f.Close()
	b := next(t, f)
	if !eq(texts(b), []string{"one", "two", "three"}) || b.End.Offset != 14 || b.Reset || b.End.DevIno == "" {
		t.Fatalf("%+v", b)
	}
	if b.Lines[1].Offset != 4 {
		t.Fatalf("offset %d", b.Lines[1].Offset)
	}
	noBatch(t, f)

	g := Follow(context.Background(), p, Position{Offset: 4, DevIno: b.End.DevIno}, fast)
	defer g.Close()
	b = next(t, g)
	if !eq(texts(b), []string{"two", "three"}) || b.Reset {
		t.Fatalf("%+v", b)
	}
}

func TestAppendsAndPartialLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.jsonl")
	write(t, p, "a\n")
	f := Follow(context.Background(), p, Position{}, fast)
	defer f.Close()
	next(t, f)
	appendTo(t, p, "bb")
	noBatch(t, f)
	appendTo(t, p, "b\ncc")
	b := next(t, f)
	if !eq(texts(b), []string{"bbb"}) || b.End.Offset != 6 || b.Lines[0].Offset != 2 {
		t.Fatalf("%+v", b)
	}
	appendTo(t, p, "c\n")
	b = next(t, f)
	if !eq(texts(b), []string{"ccc"}) || b.End.Offset != 10 {
		t.Fatalf("%+v", b)
	}
}

func TestFileCreatedLater(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.jsonl")
	f := Follow(context.Background(), p, Position{}, Options{Poll: time.Hour})
	defer f.Close()
	noBatch(t, f)
	write(t, p, "x\n")
	b := next(t, f) // delivered by fsnotify; poll is an hour
	if !eq(texts(b), []string{"x"}) || b.Reset {
		t.Fatalf("%+v", b)
	}
}

func TestTruncation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.jsonl")
	write(t, p, "aaaa\nbbbb\n")
	f := Follow(context.Background(), p, Position{}, fast)
	defer f.Close()
	next(t, f)
	write(t, p, "c\n")
	b := next(t, f)
	if !b.Reset || !eq(texts(b), []string{"c"}) || b.End.Offset != 2 {
		t.Fatalf("%+v", b)
	}
}

func TestReplacementByRename(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.jsonl")
	write(t, p, "old1\n")
	f := Follow(context.Background(), p, Position{}, fast)
	defer f.Close()
	first := next(t, f)
	// Keep the old inode alive so the new file cannot reuse it.
	old, _ := os.Open(p)
	defer old.Close()
	write(t, filepath.Join(dir, "new"), "new-longer-line-1\nn2\n")
	if err := os.Rename(filepath.Join(dir, "new"), p); err != nil {
		t.Fatal(err)
	}
	b := next(t, f)
	if !b.Reset || !eq(texts(b), []string{"new-longer-line-1", "n2"}) || b.End.DevIno == first.End.DevIno {
		t.Fatalf("%+v", b)
	}
}

func TestDeleteAndRecreate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.jsonl")
	write(t, p, "old\n")
	f := Follow(context.Background(), p, Position{}, fast)
	defer f.Close()
	next(t, f)
	old, _ := os.Open(p)
	defer old.Close()
	os.Remove(p)
	write(t, p, "fresh-line\n")
	b := next(t, f)
	if !b.Reset || !eq(texts(b), []string{"fresh-line"}) {
		t.Fatalf("%+v", b)
	}
}

func TestDevInoMismatchAtStart(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.jsonl")
	write(t, p, "a\nb\n")
	f := Follow(context.Background(), p, Position{Offset: 2, DevIno: "1:2"}, fast)
	defer f.Close()
	b := next(t, f)
	if !b.Reset || !eq(texts(b), []string{"a", "b"}) {
		t.Fatalf("%+v", b)
	}
}

func TestShorterThanOffsetAtStart(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.jsonl")
	write(t, p, "a\n")
	f := Follow(context.Background(), p, Position{Offset: 100}, fast)
	defer f.Close()
	b := next(t, f)
	if !b.Reset || !eq(texts(b), []string{"a"}) {
		t.Fatalf("%+v", b)
	}
}

func TestMaxLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.jsonl")
	big := strings.Repeat("x", 300000)
	write(t, p, "ok\n"+big+"\nafter\n"+big)
	f := Follow(context.Background(), p, Position{}, Options{Poll: 20 * time.Millisecond, MaxLine: 1000})
	defer f.Close()
	b := next(t, f)
	if !eq(texts(b), []string{"ok", "after"}) || b.Skipped != 1 {
		t.Fatalf("%v skipped=%d", texts(b), b.Skipped)
	}
	if b.End.Offset != int64(3+300001+6) {
		t.Fatalf("end %d", b.End.Offset)
	}
	appendTo(t, p, "\nlast\n")
	b = next(t, f)
	if !eq(texts(b), []string{"last"}) || b.Skipped != 1 {
		t.Fatalf("%v skipped=%d", texts(b), b.Skipped)
	}
}

func TestCRLF(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.jsonl")
	write(t, p, "a\r\nb\r\n")
	f := Follow(context.Background(), p, Position{}, fast)
	defer f.Close()
	b := next(t, f)
	if !eq(texts(b), []string{"a", "b"}) || b.End.Offset != 6 {
		t.Fatalf("%+v", b)
	}
}

func TestScan(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.jsonl")
	write(t, p, "a\r\nbb\npartial")
	var got []string
	var offs []int64
	pos, err := Scan(p, func(l Line) error {
		got = append(got, string(l.Data))
		offs = append(offs, l.Offset)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(p)
	if !eq(got, []string{"a", "bb"}) || pos.Offset != 6 || pos.DevIno != DevIno(fi) || pos.DevIno == "" || offs[1] != 3 {
		t.Fatalf("%v %+v %v", got, pos, offs)
	}
	if _, err := Scan(filepath.Join(t.TempDir(), "none"), nil); err == nil {
		t.Fatal("expected error")
	}
}

func waitGoroutines(t *testing.T, base int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > base {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			t.Fatalf("goroutine leak: %d > %d\n%s", runtime.NumGoroutine(), base, buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCloseAndCancel(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.jsonl")
	write(t, p, "a\n")
	base := runtime.NumGoroutine()

	f := Follow(context.Background(), p, Position{}, fast)
	f.Close() // before consuming anything
	if _, ok := <-f.Batches(); ok {
		// a batch may have raced in; channel must still close
		for range f.Batches() {
		}
	}
	if f.Err() != nil {
		t.Fatal(f.Err())
	}

	ctx, cancel := context.WithCancel(context.Background())
	g := Follow(ctx, p, Position{}, fast)
	next(t, g)
	cancel()
	select {
	case _, ok := <-g.Batches():
		if ok {
			t.Fatal("unexpected batch")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("not closed after cancel")
	}
	if g.Err() != nil {
		t.Fatal(g.Err())
	}
	g.Close()
	waitGoroutines(t, base)
}

func TestPollOnly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.jsonl")
	write(t, p, "a\n")
	f := Follow(context.Background(), p, Position{}, Options{Poll: 5 * time.Millisecond})
	defer f.Close()
	next(t, f)
	appendTo(t, p, "b\n")
	if b := next(t, f); !eq(texts(b), []string{"b"}) {
		t.Fatalf("%+v", b)
	}
}

// A re-read of a long file comes in several batches. Every one of them is
// marked Reset, so old lines are never taken for new ones; a line written
// after the re-read reached the end is not marked.
func TestLongReReadIsResetInEveryBatch(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.jsonl")
	write(t, p, "old1\n")
	f := Follow(context.Background(), p, Position{}, fast)
	defer f.Close()
	next(t, f)
	old, _ := os.Open(p) // keep the old inode alive so the new file gets another one
	defer old.Close()
	line := strings.Repeat("x", 1000) + "\n"
	big := strings.Repeat(line, (maxBatchBytes/len(line))*2+10) // more than two batches
	write(t, filepath.Join(dir, "new"), big)
	if err := os.Rename(filepath.Join(dir, "new"), p); err != nil {
		t.Fatal(err)
	}
	var batches, lines int
	for lines < strings.Count(big, "\n") {
		b := next(t, f)
		if !b.Reset {
			t.Fatalf("batch %d of the re-read has no Reset", batches+1)
		}
		batches++
		lines += len(b.Lines)
	}
	if batches < 2 {
		t.Fatalf("the re-read came in %d batch, want several", batches)
	}
	appendTo(t, p, "new line\n")
	if b := next(t, f); b.Reset || !eq(texts(b), []string{"new line"}) {
		t.Fatalf("after the re-read: %+v", b.Reset)
	}
}
