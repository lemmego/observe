package observe

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// collapseWhitespace puts a statement on one line, so a list view shows the
// statement rather than its indentation.
func collapseWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

// NormaliseSQL strips the parts of a statement that vary between executions,
// so two executions of the same query compare equal.
//
// This is what makes N+1 detection possible: fifty statements differing only
// in a bound id collapse to one shape, and a shape appearing fifty times
// under one request is the thing to report. It runs at read time, so a
// request pays nothing for it.
//
// Deliberately a lexer rather than a parser. Recognising SQL properly means a
// dialect-aware grammar, and getting the grammar subtly wrong would mis-group
// statements silently — where a lexer's failures are visible, because two
// statements that should have grouped simply do not.
func NormaliseSQL(statement string) string {
	var out strings.Builder
	out.Grow(len(statement))

	runes := []rune(statement)
	for i := 0; i < len(runes); i++ {
		c := runes[i]

		switch {
		// A quoted literal becomes one placeholder, whatever is inside it.
		case c == '\'' || c == '"' || c == '`':
			quote := c
			out.WriteByte('?')
			i++
			for i < len(runes) {
				if runes[i] == '\\' && i+1 < len(runes) {
					i += 2
					continue
				}
				if runes[i] == quote {
					// A doubled quote is an escaped one, not the end.
					if i+1 < len(runes) && runes[i+1] == quote {
						i += 2
						continue
					}
					break
				}
				i++
			}

		// A number becomes a placeholder.
		case c >= '0' && c <= '9':
			out.WriteByte('?')
			for i+1 < len(runes) && isNumeric(runes[i+1]) {
				i++
			}

		// An existing placeholder is already one: $1, $2, ? and :name all
		// collapse, so the same statement prepared by two drivers groups
		// together.
		case c == '$' && i+1 < len(runes) && runes[i+1] >= '0' && runes[i+1] <= '9':
			out.WriteByte('?')
			for i+1 < len(runes) && runes[i+1] >= '0' && runes[i+1] <= '9' {
				i++
			}

		case c == '?':
			out.WriteByte('?')

		default:
			out.WriteRune(c)
		}
	}

	return collapseWhitespace(out.String())
}

func isNumeric(c rune) bool {
	return (c >= '0' && c <= '9') || c == '.' || c == 'e' || c == 'E' || c == '+' || c == '-'
}

// Fingerprint identifies a statement's shape, for grouping.
//
// Short on purpose: it is a map key and a JSON field, not a security
// boundary, and twelve hex characters is far more than enough to separate the
// handful of distinct statements one request issues.
func Fingerprint(statement string) string {
	sum := sha256.Sum256([]byte(NormaliseSQL(statement)))
	return hex.EncodeToString(sum[:6])
}
