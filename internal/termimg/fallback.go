package termimg

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/draw"
	"image/png"
	"io"
	"os"
	"sync"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
)

// outlinePaths are fonts for characters that Menlo lacks, tried in order.
// Each one is optional.
var outlinePaths = []string{
	"/System/Library/Fonts/Apple Symbols.ttf",
	"/System/Library/Fonts/Supplemental/Arial Unicode.ttf",
	"/System/Library/Fonts/Hiragino Sans GB.ttc",
}

const emojiPath = "/System/Library/Fonts/Apple Color Emoji.ttc"

var (
	fallbackOnce sync.Once
	fallbacks    []*sfnt.Font
)

// openFont parses font 0 of a file. The file stays open for the life of the
// process, so a big font is read on demand and not held in memory.
func openFont(path string) (*sfnt.Font, *os.File) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil
	}
	c, err := sfnt.ParseCollectionReaderAt(f)
	if err == nil && c.NumFonts() > 0 {
		if sf, err := c.Font(0); err == nil {
			return sf, f
		}
	}
	_ = f.Close()
	return nil, nil
}

func loadFallbacks() []*sfnt.Font {
	fallbackOnce.Do(func() {
		for _, p := range outlinePaths {
			if sf, _ := openFont(p); sf != nil {
				fallbacks = append(fallbacks, sf)
			}
		}
	})
	return fallbacks
}

// emoji is the color emoji font: the glyph index comes from sf, the pictures
// from the sbix table.
type emoji struct {
	sf *sfnt.Font
	sb *sbix
}

var (
	emojiOnce sync.Once
	emojiFont *emoji // nil when the font is missing or unreadable
)

func loadEmoji() *emoji {
	emojiOnce.Do(func() {
		sf, f := openFont(emojiPath)
		if sf == nil {
			return
		}
		st, err := f.Stat()
		if err != nil {
			return
		}
		sb, err := readSbix(f, st.Size())
		if err != nil {
			return
		}
		emojiFont = &emoji{sf: sf, sb: sb}
	})
	return emojiFont
}

// bitmap is the picture of a rune at about px pixels, or nil.
func (e *emoji) bitmap(buf *sfnt.Buffer, r rune, px int) *image.NRGBA {
	idx, err := e.sf.GlyphIndex(buf, r)
	if err != nil || idx == 0 {
		return nil
	}
	return e.sb.glyph(int(idx), px)
}

// Limits that keep a corrupt font from costing much.
const (
	maxGlyphBytes = 4 << 20
	maxGlyphSide  = 1024
	maxStrikes    = 64
	maxTables     = 256
)

// sbix reads the bitmap glyphs of an Apple color emoji font (TTC, font 0).
type sbix struct {
	r         io.ReaderAt
	size      int64 // size of the file
	numGlyphs int
	table     int64 // offset of the sbix table
	tableLen  int64
	strikes   []int64 // offsets from the table start
}

func u32(b []byte) int64 { return int64(binary.BigEndian.Uint32(b)) }
func u16(b []byte) int   { return int(binary.BigEndian.Uint16(b)) }

// readAt reads exactly n bytes at off, or fails.
func readAt(r io.ReaderAt, size, off int64, n int) ([]byte, error) {
	if off < 0 || n < 0 || off+int64(n) > size {
		return nil, errors.New("sbix: out of range")
	}
	b := make([]byte, n)
	if got, err := r.ReadAt(b, off); got < n {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return b, nil
}

// readSbix finds the sbix and maxp tables of the first font of a collection
// (or of a plain font file).
func readSbix(r io.ReaderAt, size int64) (*sbix, error) {
	head, err := readAt(r, size, 0, 16)
	if err != nil {
		return nil, err
	}
	fontOff := int64(0)
	if string(head[:4]) == "ttcf" {
		if u32(head[8:]) < 1 {
			return nil, errors.New("sbix: empty collection")
		}
		fontOff = u32(head[12:])
	}
	dir, err := readAt(r, size, fontOff, 12)
	if err != nil {
		return nil, err
	}
	n := u16(dir[4:])
	if n > maxTables {
		return nil, errors.New("sbix: too many tables")
	}
	recs, err := readAt(r, size, fontOff+12, n*16)
	if err != nil {
		return nil, err
	}
	s := &sbix{r: r, size: size}
	var maxp int64 = -1
	for i := 0; i < n; i++ {
		rec := recs[i*16:]
		off, ln := u32(rec[8:]), u32(rec[12:])
		switch string(rec[:4]) {
		case "sbix":
			s.table, s.tableLen = off, ln
		case "maxp":
			maxp = off
		}
	}
	if maxp < 0 || s.tableLen < 8 || s.table+s.tableLen > size {
		return nil, errors.New("sbix: no sbix or maxp table")
	}
	mb, err := readAt(r, size, maxp+4, 2)
	if err != nil {
		return nil, err
	}
	s.numGlyphs = u16(mb)
	hb, err := readAt(r, size, s.table, 8)
	if err != nil {
		return nil, err
	}
	ns := int(u32(hb[4:]))
	if ns < 1 || ns > maxStrikes || s.numGlyphs < 1 {
		return nil, errors.New("sbix: bad strike count")
	}
	ob, err := readAt(r, size, s.table+8, ns*4)
	if err != nil {
		return nil, err
	}
	for i := 0; i < ns; i++ {
		off := u32(ob[i*4:])
		if off+4+int64(s.numGlyphs+1)*4 > s.tableLen {
			return nil, errors.New("sbix: bad strike offset")
		}
		s.strikes = append(s.strikes, off)
	}
	return s, nil
}

// strikeFor picks the strike with the smallest ppem that is at least px,
// else the largest one.
func (s *sbix) strikeFor(px int) (int64, bool) {
	best, bestPPEM := int64(-1), 0
	for _, off := range s.strikes {
		b, err := readAt(s.r, s.size, s.table+off, 2)
		if err != nil {
			continue
		}
		p := u16(b)
		better := best < 0 ||
			bestPPEM >= px && p >= px && p < bestPPEM ||
			bestPPEM < px && p > bestPPEM
		if better {
			best, bestPPEM = off, p
		}
	}
	return best, best >= 0
}

// glyph decodes the picture of a glyph, or returns nil when there is none or
// the data is bad.
func (s *sbix) glyph(gid, px int) *image.NRGBA {
	if s == nil || gid <= 0 || gid >= s.numGlyphs {
		return nil
	}
	strike, ok := s.strikeFor(px)
	if !ok {
		return nil
	}
	for hop := 0; hop < 2; hop++ { // a "dupe" glyph points to another one, once
		ob, err := readAt(s.r, s.size, s.table+strike+4+int64(gid)*4, 8)
		if err != nil {
			return nil
		}
		a, b := u32(ob), u32(ob[4:])
		if b-a <= 8 || b-a > maxGlyphBytes || strike+b > s.tableLen {
			return nil
		}
		data, err := readAt(s.r, s.size, s.table+strike+a, int(b-a))
		if err != nil {
			return nil
		}
		switch string(data[4:8]) {
		case "png ":
			cfg, err := png.DecodeConfig(bytes.NewReader(data[8:]))
			if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > maxGlyphSide || cfg.Height > maxGlyphSide {
				return nil
			}
			img, err := png.Decode(bytes.NewReader(data[8:]))
			if err != nil {
				return nil
			}
			out := image.NewNRGBA(img.Bounds())
			draw.Draw(out, out.Bounds(), img, img.Bounds().Min, draw.Src)
			return out
		case "dupe":
			if hop > 0 || len(data) < 10 {
				return nil
			}
			gid = u16(data[8:])
			if gid <= 0 || gid >= s.numGlyphs {
				return nil
			}
		default:
			return nil
		}
	}
	return nil
}

// scaleSquare scales a picture to fit a square of the given side, keeping
// its aspect ratio, centered.
func scaleSquare(src *image.NRGBA, side int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, side, side))
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := side, side
	if w > h {
		dh = max(1, h*side/w)
	} else {
		dw = max(1, w*side/h)
	}
	r := image.Rect((side-dw)/2, (side-dh)/2, (side-dw)/2+dw, (side-dh)/2+dh)
	xdraw.CatmullRom.Scale(dst, r, src, b, xdraw.Over, nil)
	return dst
}

// faceSet holds the outline fallback faces of one Render, made when first
// needed.
type faceSet struct {
	scale int
	faces []font.Face // nil entries: no face for that font
	fonts []*sfnt.Font
	ready bool
}

func (fs *faceSet) load() {
	if fs.ready {
		return
	}
	fs.ready = true
	fs.fonts = loadFallbacks()
	for _, f := range fs.fonts {
		face, ok := newFace(f, fs.scale)
		if !ok {
			face = nil
		}
		fs.faces = append(fs.faces, face)
	}
}

// find returns the first fallback face that has the rune.
func (fs *faceSet) find(buf *sfnt.Buffer, r rune) font.Face {
	fs.load()
	for i, f := range fs.fonts {
		if fs.faces[i] == nil {
			continue
		}
		if idx, err := f.GlyphIndex(buf, r); err == nil && idx != 0 {
			return fs.faces[i]
		}
	}
	return nil
}

func (fs *faceSet) close() {
	for _, f := range fs.faces {
		if f != nil {
			_ = f.Close()
		}
	}
}
