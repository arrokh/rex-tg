// Package termimg renders terminal text as a PNG: a monospace grid, light
// text on a dark background. It is made for a phone screen.
package termimg

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// Limits of the picture. Telegram takes a photo with each side up to 10000
// px and width plus height up to 10000.
const (
	MaxLines = 60
	MaxCols  = 200
	maxSum   = 10000

	fontSize = 14 // points at 72 dpi, so pixels, before the scale
	pad      = 8  // pixels around the text, before the scale
)

// fontPaths are the places of a monospace font with a regular face first.
var fontPaths = []string{
	"/System/Library/Fonts/Menlo.ttc",
	"/Library/Fonts/Menlo.ttc",
}

var (
	fontOnce sync.Once
	menlo    *sfnt.Font // nil when no font could be loaded
)

func loadFont() *sfnt.Font {
	fontOnce.Do(func() {
		for _, p := range fontPaths {
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			if c, err := sfnt.ParseCollection(data); err == nil && c.NumFonts() > 0 {
				if f, err := c.Font(0); err == nil {
					menlo = f
					return
				}
			}
		}
	})
	return menlo
}

// Layout is the grid of a picture and its size in pixels.
type Layout struct {
	Cols, Rows    int // cells
	CellW, CellH  int // pixels
	Scale         int
	Width, Height int // pixels
}

// Fit cuts the lines to the caps (the bottom MaxLines lines are kept, and a
// line is cut at MaxCols columns) and returns the cut lines with the width
// in columns of the widest one.
func Fit(lines []string) (out []string, cols int) {
	if len(lines) > MaxLines {
		lines = lines[len(lines)-MaxLines:]
	}
	out = make([]string, len(lines))
	for i, l := range lines {
		l = clean(l)
		w, cut := 0, len(l)
		for k, r := range l {
			rw := RuneWidth(r)
			if w+rw > MaxCols {
				cut = k
				break
			}
			w += rw
		}
		out[i] = strings.TrimRight(l[:cut], " ")
		cols = max(cols, Width(out[i]))
	}
	return out, cols
}

// Width is the number of terminal columns of a line.
func Width(l string) int {
	w := 0
	for _, r := range l {
		w += RuneWidth(r)
	}
	return w
}

// clean makes a line safe to draw: tabs become spaces, other control
// characters are dropped.
func clean(l string) string {
	if !strings.ContainsFunc(l, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return l
	}
	var b strings.Builder
	for _, r := range l {
		switch {
		case r == '\t':
			b.WriteString("    ")
		case r < 0x20 || r == 0x7f:
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// LayoutOf is the layout of a grid of cols by rows cells with the given cell
// size (in pixels at scale 1). It lowers the scale until the picture is
// within Telegram's limits.
func LayoutOf(cols, rows, cellW, cellH, scale int) Layout {
	cols, rows = max(cols, 1), max(rows, 1)
	for ; ; scale-- {
		l := Layout{Cols: cols, Rows: rows, CellW: cellW * scale, CellH: cellH * scale, Scale: scale}
		l.Width = cols*l.CellW + 2*pad*scale
		l.Height = rows*l.CellH + 2*pad*scale
		if scale <= 1 || l.Width+l.Height <= maxSum {
			return l
		}
	}
}

var (
	bg = color.RGBA{0x1e, 0x1e, 0x1e, 0xff}
	fg = color.RGBA{0xd4, 0xd4, 0xd4, 0xff}
)

// newFace makes the face of the loaded font at a scale, and its cell size at
// scale 1. ok is false when there is no usable font.
func newFace(sf *sfnt.Font, scale int) (face font.Face, ok bool) {
	if sf == nil {
		return nil, false
	}
	f, err := opentype.NewFace(sf, &opentype.FaceOptions{Size: fontSize * float64(scale), DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil, false
	}
	if adv, ok := f.GlyphAdvance('M'); !ok || adv <= 0 {
		_ = f.Close()
		return nil, false
	}
	return f, true
}

// Render draws the lines as a PNG. Only the bottom MaxLines lines and the
// first MaxCols columns are drawn. It fails for no text.
func Render(lines []string) ([]byte, error) {
	lines, cols := Fit(lines)
	if len(lines) == 0 || cols == 0 {
		return nil, errors.New("termimg: nothing to draw")
	}
	sf := loadFont()
	var (
		face font.Face
		l    Layout
	)
	if probe, ok := newFace(sf, 1); ok {
		adv, _ := probe.GlyphAdvance('M')
		cellW, cellH := adv.Ceil(), probe.Metrics().Height.Ceil()
		_ = probe.Close()
		l = LayoutOf(cols, len(lines), cellW, cellH, 2)
		if face, ok = newFace(sf, l.Scale); ok {
			// Use the cell size of the scaled face: it may differ by a pixel
			// from the cell size at scale 1 times the scale.
			adv, _ := face.GlyphAdvance('M')
			l.CellW, l.CellH = adv.Ceil(), face.Metrics().Height.Ceil()
			l.Width = cols*l.CellW + 2*pad*l.Scale
			l.Height = len(lines)*l.CellH + 2*pad*l.Scale
		}
	}
	if face == nil { // the fallback: a small bitmap font at scale 1
		sf = nil
		face = basicfont.Face7x13
		l = LayoutOf(cols, len(lines), 7, 13, 1)
	}
	img := image.NewRGBA(image.Rect(0, 0, l.Width, l.Height))
	draw.Draw(img, img.Bounds(), image.NewUniform(bg), image.Point{}, draw.Src)
	var buf sfnt.Buffer
	d := &font.Drawer{Dst: img, Src: image.NewUniform(fg), Face: face}
	m := face.Metrics()
	top := (l.CellH - m.Height.Ceil()) / 2 // center the glyphs in the line height
	for row, line := range lines {
		col := 0
		for _, r := range line {
			w := RuneWidth(r)
			x := pad*l.Scale + col*l.CellW
			y := pad*l.Scale + row*l.CellH
			col += w
			if r == ' ' {
				continue
			}
			have := true
			if sf != nil {
				idx, err := sf.GlyphIndex(&buf, r)
				have = err == nil && idx != 0
			} else {
				_, have = face.GlyphAdvance(r)
			}
			if !have {
				box(img, x, y, w*l.CellW, l.CellH, l.Scale)
				continue
			}
			d.Dot = fixed.P(x, y+top+m.Ascent.Ceil())
			d.DrawString(string(r))
		}
	}
	if c, ok := face.(interface{ Close() error }); ok {
		_ = c.Close()
	}
	var out bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// box draws a hollow rectangle in the cells of a character that the font
// lacks.
func box(img *image.RGBA, x, y, w, h, scale int) {
	mx, my := w/6, h/5
	x0, y0, x1, y1 := x+mx, y+my, x+w-mx, y+h-my
	for t := 0; t < scale; t++ {
		for i := x0; i < x1; i++ {
			img.SetRGBA(i, y0+t, fg)
			img.SetRGBA(i, y1-1-t, fg)
		}
		for j := y0; j < y1; j++ {
			img.SetRGBA(x0+t, j, fg)
			img.SetRGBA(x1-1-t, j, fg)
		}
	}
}
