package format

import (
	"strings"
	"testing"
)

func TestSplitWideCodeKeepsNarrowBlocks(t *testing.T) {
	md := "Run this:\n\n```sh\ngo test ./...\n```\n\nDone."
	got, files := SplitWideCode(md)
	if got != md || files != nil {
		t.Fatalf("got %q, %v", got, files)
	}
}

func TestSplitWideCodeTakesOutAWideBlock(t *testing.T) {
	long := "x := " + strings.Repeat("a", 90)
	body := []string{"package main", long, "func a() {}", "func b() {}", "func c() {}"}
	md := "Here is the file:\n\n```go\n" + strings.Join(body, "\n") + "\n```\n\nAnd a tree:\n\n```\nshort\n```\n"
	got, files := SplitWideCode(md)
	if len(files) != 1 || files[0].Name != "code-1.go" || files[0].Content != strings.Join(body, "\n")+"\n" {
		t.Fatalf("files = %+v", files)
	}
	want := "Here is the file:\n\n```go\npackage main\n" + cutLine(long, previewWidth) + "\nfunc a() {}\n…\n```\n" +
		"_Full block (5 lines) in `code-1.go`, below._\n\nAnd a tree:\n\n```\nshort\n```\n"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if n := len([]rune(cutLine(long, previewWidth))); n != previewWidth {
		t.Errorf("preview line has %d characters", n)
	}
}

func TestSplitWideCodeNamesAndEdges(t *testing.T) {
	wide := strings.Repeat("w", WideCodeLine+1)
	md := "~~~~json\n" + wide + "\n~~~~\n\n- item\n\n  ```\n  " + wide + "\n  ```"
	got, files := SplitWideCode(md)
	if len(files) != 2 || files[0].Name != "code-1.json" || files[1].Name != "code-2.txt" {
		t.Fatalf("files = %+v", files)
	}
	if files[1].Content != wide+"\n" {
		t.Errorf("the list item's block keeps its text without the indent: %q", files[1].Content)
	}
	if !strings.Contains(got, "  _Full block (1 lines) in `code-2.txt`, below._") {
		t.Errorf("the note keeps the list item's indent:\n%s", got)
	}
	// A fence that is never closed, and inline code, stay as they are.
	open := "``` not closed\n" + wide
	if got, files := SplitWideCode(open); got != open || files != nil {
		t.Errorf("unclosed: %q %v", got, files)
	}
	inline := "use ```" + wide + "``` here"
	if got, files := SplitWideCode(inline); got != inline || files != nil {
		t.Errorf("inline: %q %v", got, files)
	}
	// A line exactly at the limit stays.
	at := "```\n" + strings.Repeat("a", WideCodeLine) + "\n```"
	if _, files := SplitWideCode(at); files != nil {
		t.Errorf("a line at the limit was taken out")
	}
}
