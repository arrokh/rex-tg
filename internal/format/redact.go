package format

import "regexp"

const redacted = "[redacted]"

var (
	pemFull = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`)
	pemOpen = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*`)
	bearer  = regexp.MustCompile(`(\b[Bb]earer\s+)[A-Za-z0-9._~+/=-]{16,}`)

	secretPatterns = []*regexp.Regexp{
		regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{5,}\.eyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]+`),
		regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`),
		regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`),
		regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`),
		regexp.MustCompile(`\bAKIA[0-9A-Z]{16}`),
		regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`),
		regexp.MustCompile(`\b\d{8,10}:[A-Za-z0-9_-]{35}`),
	}
)

// Redact replaces common secret formats in s with "[redacted]". It is a best
// effort, not a guarantee.
func Redact(s string) string {
	s = pemFull.ReplaceAllString(s, redacted)
	s = pemOpen.ReplaceAllString(s, redacted)
	for _, re := range secretPatterns {
		s = re.ReplaceAllString(s, redacted)
	}
	return bearer.ReplaceAllString(s, "${1}"+redacted)
}
