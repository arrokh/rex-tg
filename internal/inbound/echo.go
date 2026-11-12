package inbound

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	hashWindow   = 10 * time.Minute
	prefixWindow = 30 * time.Second
	prefixRunes  = 40
)

// Normalize makes two spellings of one prompt equal: it trims, converts
// CRLF and CR to LF, trims trailing spaces on each line, collapses runs of
// blank lines to one blank line, and removes a trailing newline. It does not
// apply Unicode NFC (not in the standard library).
func Normalize(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(strings.TrimSpace(s), "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, l := range lines {
		l = strings.TrimRight(l, " \t")
		if l == "" {
			if blank {
				continue
			}
			blank = true
		} else {
			blank = false
		}
		out = append(out, l)
	}
	return strings.TrimSuffix(strings.Join(out, "\n"), "\n")
}

var (
	// imageMarker is how Claude Code shows an image it attached from a
	// pasted path, at the start of the prompt (seen live).
	imageMarker = regexp.MustCompile(`\[Image #\d+\]`)
	// imagePath is an absolute path to an image file. rex-tg types one for a
	// photo from Telegram; Claude Code replaces it with an image marker.
	imagePath = regexp.MustCompile(`(?i)/\S+\.(?:png|jpe?g|gif|webp)\b`)
)

// echoText is s as the echo check compares it: without image paths and
// image markers, so a photo matches whether the agent kept its path or
// attached the image (Claude Code), and then normalized.
func echoText(s string) string {
	s = imageMarker.ReplaceAllString(s, "")
	return Normalize(imagePath.ReplaceAllString(s, ""))
}

// Hash returns the hex SHA-256 of s as the echo check compares it: Normalize
// after image paths and "[Image #N]" markers are removed.
func Hash(s string) string {
	sum := sha256.Sum256([]byte(echoText(s)))
	return hex.EncodeToString(sum[:])
}

// Delivered is a message that was typed into a pane.
type Delivered struct {
	UpdateID    int64
	BodyHash    string
	Body        string
	DeliveredAt time.Time
}

func prefix(s string) string {
	r := []rune(s)
	if len(r) > prefixRunes {
		r = r[:prefixRunes]
	}
	return string(r)
}

// MatchEcho finds the delivered message that a transcript prompt seen at
// time at belongs to. It first looks for an equal hash among items delivered
// in the 10 minutes before at (oldest first). Otherwise it takes the first
// item delivered in the 30 seconds before at whose body starts with the same
// 40 characters as the prompt. Both compare the texts without image paths and
// image markers (see Hash). A prompt that is only an image matches only
// within those 30 seconds, because all such prompts look the same. It returns
// the update ID.
func MatchEcho(prompt string, at time.Time, delivered []Delivered) (int64, bool) {
	items := make([]Delivered, len(delivered))
	copy(items, delivered)
	sort.SliceStable(items, func(i, k int) bool { return items[i].DeliveredAt.Before(items[k].DeliveredAt) })

	within := func(d Delivered, w time.Duration) bool {
		return !d.DeliveredAt.After(at) && !d.DeliveredAt.Before(at.Add(-w))
	}
	h := Hash(prompt)
	window := hashWindow
	if echoText(prompt) == "" {
		// Only an image: every such prompt looks the same, so a match must be
		// as close in time as the prefix check (a photo typed a moment ago).
		window = prefixWindow
	}
	for _, d := range items {
		if within(d, window) && d.BodyHash == h {
			return d.UpdateID, true
		}
	}
	p := prefix(echoText(prompt))
	if p == "" {
		return 0, false
	}
	for _, d := range items {
		if within(d, prefixWindow) && prefix(echoText(d.Body)) == p {
			return d.UpdateID, true
		}
	}
	return 0, false
}
