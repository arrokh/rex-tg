package termimg

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"os"
	"testing"

	"golang.org/x/image/font/sfnt"
)

func TestRuneWidthZeroWidth(t *testing.T) {
	for _, r := range []rune{0x200B, 0x200C, 0x200D, 0x200F, 0xFE0E, 0xFE0F, 0x0301, 0x036F, 0x20D0, 0x20FF, 0x1F3FB, 0x1F3FF, 0xE0100, 0xE01EF} {
		if got := RuneWidth(r); got != 0 {
			t.Errorf("RuneWidth(%U) = %d, want 0", r, got)
		}
	}
	if w := Width("á️‍b"); w != 2 {
		t.Errorf("Width = %d, want 2", w)
	}
}

func have(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// cellImage renders one line and returns the picture and its layout.
func cellImage(t *testing.T, line string) (img image.Image, l Layout) {
	t.Helper()
	data, err := Render([]string{line})
	if err != nil {
		t.Fatal(err)
	}
	img, err = png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	_, cols := Fit([]string{line})
	probe, ok := newFace(loadFont(), 1)
	if !ok {
		t.Skip("no Menlo")
	}
	adv, _ := probe.GlyphAdvance('M')
	cellH := probe.Metrics().Height.Ceil()
	_ = probe.Close()
	l = LayoutOf(cols, 1, adv.Ceil(), cellH, 2)
	l.CellW = (img.Bounds().Dx() - 2*pad*l.Scale) / cols
	return img, l
}

func countColored(img image.Image, l Layout, col, cells int) int {
	x0 := pad*l.Scale + col*l.CellW
	y0 := pad * l.Scale
	n := 0
	for y := y0; y < y0+l.CellH; y++ {
		for x := x0; x < x0+cells*l.CellW; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if r != g || g != b {
				n++
			}
		}
	}
	return n
}

func TestRenderColorEmoji(t *testing.T) {
	if !have(emojiPath) || loadFont() == nil {
		t.Skip("Apple Color Emoji or Menlo missing")
	}
	img, l := cellImage(t, "ok 🚀 ✅ ✓ 中")
	if c := countColored(img, l, 3, 2); c < 50 {
		t.Errorf("rocket cell has %d colored pixels, want many", c)
	}
	if c := countColored(img, l, 6, 2); c < 50 {
		t.Errorf("check mark button cell has %d colored pixels, want many", c)
	}
	if c := countColored(img, l, 0, 2); c != 0 {
		t.Errorf("text cell has %d colored pixels, want 0", c)
	}
}

func TestRenderSymbolFallback(t *testing.T) {
	if !have(outlinePaths[0]) || loadFont() == nil {
		t.Skip("Apple Symbols or Menlo missing")
	}
	fallbacks := loadFallbacks()
	if len(fallbacks) == 0 {
		t.Skip("no fallback font")
	}
	var buf sfnt.Buffer
	var sym rune
	for _, r := range "⏺⎿❯✻◐★" { // a symbol that Menlo lacks and a fallback has
		a, _ := loadFont().GlyphIndex(&buf, r)
		b, _ := fallbacks[0].GlyphIndex(&buf, r)
		if a == 0 && b != 0 {
			sym = r
			break
		}
	}
	if sym == 0 {
		t.Skip("no test symbol that only Apple Symbols has")
	}
	sample := func(r rune) []byte {
		img, l := cellImage(t, "a"+string(r))
		x0 := pad*l.Scale + l.CellW
		var out []byte
		for y := pad * l.Scale; y < pad*l.Scale+l.CellH; y++ {
			for x := x0; x < x0+l.CellW; x++ {
				v, _, _, _ := img.At(x, y).RGBA()
				out = append(out, byte(v>>8))
			}
		}
		return out
	}
	got, boxed := sample(sym), sample(0x10FFFD) // a private-use rune: no font has it
	if bytes.Equal(got, boxed) {
		t.Errorf("%q is drawn as the hollow box", sym)
	}
	lit := 0
	for _, p := range got {
		if p > 0x60 {
			lit++
		}
	}
	if lit == 0 {
		t.Errorf("%q drew nothing", sym)
	}
}

// fakeSbix builds a tiny TTC with one font that has maxp and sbix tables.
// Glyph 1 is a PNG, glyph 2 is a dupe of glyph 1.
func fakeSbix(t *testing.T) []byte {
	t.Helper()
	var pngBuf bytes.Buffer
	im := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	im.Set(1, 1, color.NRGBA{255, 0, 0, 255})
	if err := png.Encode(&pngBuf, im); err != nil {
		t.Fatal(err)
	}
	be := binary.BigEndian
	const numGlyphs = 3
	g1 := append([]byte{0, 0, 0, 0, 'p', 'n', 'g', ' '}, pngBuf.Bytes()...)
	g2 := []byte{0, 0, 0, 0, 'd', 'u', 'p', 'e', 0, 1}
	strikeHdr := 4 + (numGlyphs+1)*4
	strike := make([]byte, strikeHdr)
	be.PutUint16(strike, 32) // ppem
	offs := []int{strikeHdr, strikeHdr, strikeHdr + len(g1), strikeHdr + len(g1) + len(g2)}
	for i, o := range offs {
		be.PutUint32(strike[4+i*4:], uint32(o))
	}
	strike = append(append(strike, g1...), g2...)
	sb := make([]byte, 12)
	be.PutUint16(sb, 1)
	be.PutUint32(sb[4:], 1)
	be.PutUint32(sb[8:], 12)
	sb = append(sb, strike...)
	maxp := make([]byte, 6)
	be.PutUint16(maxp[4:], numGlyphs)

	out := make([]byte, 16+12+2*16)
	copy(out, "ttcf")
	be.PutUint32(out[4:], 0x00010000)
	be.PutUint32(out[8:], 1)
	be.PutUint32(out[12:], 16)
	be.PutUint32(out[16:], 0x00010000)
	be.PutUint16(out[20:], 2)
	base := len(out)
	rec := func(i int, tag string, off, n int) {
		r := out[28+i*16:]
		copy(r, tag)
		be.PutUint32(r[8:], uint32(off))
		be.PutUint32(r[12:], uint32(n))
	}
	rec(0, "maxp", base, len(maxp))
	rec(1, "sbix", base+len(maxp), len(sb))
	return append(append(out, maxp...), sb...)
}

func TestSbixReader(t *testing.T) {
	data := fakeSbix(t)
	s, err := readSbix(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if im := s.glyph(1, 20); im == nil || im.Bounds().Dx() != 4 {
		t.Errorf("glyph 1 = %v, want a 4x4 picture", im)
	}
	if im := s.glyph(2, 20); im == nil {
		t.Error("a dupe glyph was not followed")
	}
	if s.glyph(0, 20) != nil || s.glyph(99, 20) != nil {
		t.Error("an empty or unknown glyph gave a picture")
	}
}

func TestSbixCorruptInput(t *testing.T) {
	data := fakeSbix(t)
	probe := func(s *sbix) {
		for g := -1; g < 5; g++ {
			_ = s.glyph(g, 20)
		}
	}
	for n := 0; n < len(data); n++ { // every truncation: no panic
		trunc := data[:n]
		if s, err := readSbix(bytes.NewReader(trunc), int64(len(data))); err == nil { // the size lies
			probe(s)
		}
		if s, err := readSbix(bytes.NewReader(trunc), int64(n)); err == nil {
			probe(s)
		}
	}
	short := data[:len(data)-5]
	if s, err := readSbix(bytes.NewReader(short), int64(len(short))); err == nil {
		if s.glyph(1, 20) != nil && s.glyph(2, 20) != nil {
			t.Error("truncated data still gave both pictures")
		}
	}
	for i := range data { // flipped bytes: no panic
		bad := append([]byte(nil), data...)
		bad[i] ^= 0xFF
		if s, err := readSbix(bytes.NewReader(bad), int64(len(bad))); err == nil {
			probe(s)
		}
	}
}
