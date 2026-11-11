package format

import "regexp"

const redacted = "[redacted]"

var (
	pemFull = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`)
	pemOpen = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*`)
	bearer  = regexp.MustCompile(`(\b[Bb]earer\s+)[A-Za-z0-9._~+/=-]{16,}`)
	basic   = regexp.MustCompile(`(\b[Bb]asic\s+)[A-Za-z0-9+/=]{16,}`)
	// keyValue keeps the name and hides the value of an assignment whose
	// name ends in a secret word, as in .env files, YAML, or JSON. The word
	// must end the name: "tokenizer" or "max_tokens" do not match.
	keyValue = regexp.MustCompile(`(?i)(\b[A-Za-z0-9_]*(?:secret|token|password|passwd|api_?key|access_?key)\b["']?\s*[=:]\s*["']?)[A-Za-z0-9_\-./+=]{12,}`)

	secretPatterns = []*regexp.Regexp{
		regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{5,}\.eyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]+`),
		regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`),
		regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`),
		regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`),
		regexp.MustCompile(`\bAKIA[0-9A-Z]{16}`),
		regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`),
		regexp.MustCompile(`\b\d{8,10}:[A-Za-z0-9_-]{35}`),
		regexp.MustCompile(`\b[rs]k_(?:live|test)_[A-Za-z0-9]{16,}`), // Stripe
		regexp.MustCompile(`\bwhsec_[A-Za-z0-9]{24,}`),               // Stripe webhooks
		regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}`),                // Google
		regexp.MustCompile(`\bnpm_[A-Za-z0-9]{36}`),                  // npm
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
	s = keyValue.ReplaceAllString(s, "${1}"+redacted)
	s = basic.ReplaceAllString(s, "${1}"+redacted)
	return bearer.ReplaceAllString(s, "${1}"+redacted)
}
