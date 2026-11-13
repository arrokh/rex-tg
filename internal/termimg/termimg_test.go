package termimg

import (
	"bytes"
	"image/png"
	"strings"
	"testing"
)

func TestRuneWidth(t *testing.T) {
	for r, want := range map[rune]int{'a': 1, '─': 1, '中': 2, 'あ': 2, '한': 2, '🚀': 2, '✅': 2} {
		if got := RuneWidth(r); got != want {
			t.Errorf("RuneWidth(%q) = %d, want %d", r, got, want)
		}
	}
}

func TestFitCapsLinesAndColumns(t *testing.T) {
	var in []string
	for i := 0; i < 100; i++ {
		in = append(in, strings.Repeat("x", i))
	}
	out, cols := Fit(in)
	if len(out) != MaxLines {
		t.Fatalf("lines = %d, want %d", len(out), MaxLines)
	}
	if out[0] != in[40] || out[len(out)-1] != in[99] {
		t.Errorf("kept %d..%d chars, want the bottom part (40..99)", len(out[0]), len(out[len(out)-1]))
	}
	if cols != 99 {
		t.Errorf("cols = %d, want 99", cols)
	}
	_, cols = Fit([]string{strings.Repeat("y", 500)})
	if cols != MaxCols {
		t.Errorf("a long line: cols = %d, want %d", cols, MaxCols)
	}
}

func TestFitWideCharsCountTwo(t *testing.T) {
	out, cols := Fit([]string{"中文ab", strings.Repeat("中", 150)})
	if cols != MaxCols || Width(out[1]) != MaxCols {
		t.Errorf("cols = %d, width = %d, want %d (100 wide chars)", cols, Width(out[1]), MaxCols)
	}
	if Width(out[0]) != 6 {
		t.Errorf("width = %d, want 6", Width(out[0]))
	}
	odd, _ := Fit([]string{"a" + strings.Repeat("中", 150)})
	if Width(odd[0]) > MaxCols {
		t.Errorf("a wide character crossed the column cap: %d", Width(odd[0]))
	}
}

func TestFitCleansControls(t *testing.T) {
	out, _ := Fit([]string{"a\tb\x1b\r  "})
	if out[0] != "a    b" {
		t.Errorf("line = %q", out[0])
	}
}

func TestLayoutLimits(t *testing.T) {
	l := LayoutOf(MaxCols, MaxLines, 9, 17, 2)
	if l.Width+l.Height > 10000 {
		t.Errorf("max layout %dx%d is over the limits", l.Width, l.Height)
	}
	if l.Width != MaxCols*18+2*pad*2 || l.Height != MaxLines*34+2*pad*2 {
		t.Errorf("layout = %+v", l)
	}
	if huge := LayoutOf(1000, 1000, 9, 17, 2); huge.Scale != 1 {
		t.Errorf("a huge grid keeps scale %d, want 1", huge.Scale)
	}
	if one := LayoutOf(0, 0, 9, 17, 2); one.Cols != 1 || one.Rows != 1 {
		t.Errorf("empty grid = %+v", one)
	}
}

func TestRenderSize(t *testing.T) {
	lines := []string{"hello", "中文 wide", "", "last line here", "⟪no glyph⟫ 🚀"}
	data, err := Render(lines)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	fit, cols := Fit(lines)
	if cols != 14 {
		t.Fatalf("cols = %d, want 14", cols)
	}
	b := img.Bounds()
	// The picture is the grid plus a border, at a whole scale.
	ok := false
	for scale := 1; scale <= 2; scale++ {
		w, h := b.Dx()-2*pad*scale, b.Dy()-2*pad*scale
		if w > 0 && h > 0 && w%cols == 0 && h%len(fit) == 0 {
			ok = true
		}
	}
	if !ok {
		t.Errorf("size %dx%d does not fit a %dx%d grid", b.Dx(), b.Dy(), cols, len(fit))
	}
	if b.Dx()+b.Dy() > 10000 {
		t.Errorf("size %dx%d is over the limit", b.Dx(), b.Dy())
	}
	light := 0
	for y := 0; y < b.Dy(); y += 2 {
		for x := 0; x < b.Dx(); x += 2 {
			if r, _, _, _ := img.At(x, y).RGBA(); r > 0x8000 {
				light++
			}
		}
	}
	if light == 0 {
		t.Error("no text was drawn")
	}
}

func TestRenderWideCharTakesTwoCells(t *testing.T) {
	narrow, err := Render([]string{"ab"})
	if err != nil {
		t.Fatal(err)
	}
	wide, err := Render([]string{"中"})
	if err != nil {
		t.Fatal(err)
	}
	n, _ := png.DecodeConfig(bytes.NewReader(narrow))
	w, _ := png.DecodeConfig(bytes.NewReader(wide))
	if n.Width != w.Width {
		t.Errorf("one wide character is %d px wide, two narrow ones %d px", w.Width, n.Width)
	}
}

func TestRenderEmptyAndMax(t *testing.T) {
	if _, err := Render(nil); err == nil {
		t.Error("no lines: want an error")
	}
	if _, err := Render([]string{"", ""}); err == nil {
		t.Error("blank lines: want an error")
	}
	var lines []string
	for i := 0; i < 80; i++ {
		lines = append(lines, strings.Repeat("0123456789", 30))
	}
	data, err := Render(lines)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := png.DecodeConfig(bytes.NewReader(data))
	if cfg.Width+cfg.Height > 10000 || len(data) > 10<<20 {
		t.Errorf("max picture %dx%d, %d bytes", cfg.Width, cfg.Height, len(data))
	}
}
