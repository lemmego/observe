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
// under one request is the thing to report.
//
// It runs at read time for that, so a request pays nothing for N+1
// detection. A recorder configured with [WithNormalisedStatements] also runs
// it at write time, which costs about 700ns and one allocation per statement
// and buys smaller stored and shipped entries — see the benchmarks.
//
// Deliberately a lexer rather than a parser. Recognising SQL properly means a
// dialect-aware grammar, and getting the grammar subtly wrong would mis-group
// statements silently — where a lexer's failures are visible, because two
// statements that should have grouped simply do not.
func NormaliseSQL(statement string) string {
	var out strings.Builder
	out.Grow(len(statement))

	// Scanned a byte at a time rather than over []rune(statement), and that
	// is a correctness claim as much as a performance one: every character
	// this recognises — quotes, digits, $, ?, whitespace — is ASCII, and a
	// multi-byte UTF-8 sequence never contains a byte below 0x80. So a
	// non-ASCII character cannot be mistaken for a delimiter, and its bytes
	// fall through to the default and are copied verbatim.
	//
	// The []rune conversion it replaces allocated four bytes per character
	// of every statement, on the path of every statement, which is the
	// allocation this phase exists to remove. It also runs at read time for
	// N+1 grouping, so the saving is paid twice.
	//
	// Whitespace is collapsed as it goes rather than in a second pass over
	// the result, which removes the other allocation.
	pendingSpace := false
	wrote := false

	write := func(b byte) {
		if pendingSpace && wrote {
			out.WriteByte(' ')
		}
		pendingSpace = false
		out.WriteByte(b)
		wrote = true
	}

	for i := 0; i < len(statement); i++ {
		c := statement[i]

		switch {
		case isSpace(c):
			pendingSpace = true

		// A quoted literal becomes one placeholder, whatever is inside it.
		case c == '\'' || c == '"' || c == '`':
			quote := c
			write('?')
			i++
			for i < len(statement) {
				if statement[i] == '\\' && i+1 < len(statement) {
					i += 2
					continue
				}
				if statement[i] == quote {
					// A doubled quote is an escaped one, not the end.
					if i+1 < len(statement) && statement[i+1] == quote {
						i += 2
						continue
					}
					break
				}
				i++
			}

		// A number becomes a placeholder.
		case c >= '0' && c <= '9':
			write('?')
			for i+1 < len(statement) && isNumericByte(statement[i+1]) {
				i++
			}

		// An existing placeholder is already one: $1, $2, ? and :name all
		// collapse, so the same statement prepared by two drivers groups
		// together.
		case c == '$' && i+1 < len(statement) && statement[i+1] >= '0' && statement[i+1] <= '9':
			write('?')
			for i+1 < len(statement) && statement[i+1] >= '0' && statement[i+1] <= '9' {
				i++
			}

		case c == '?':
			write('?')

		default:
			write(c)
		}
	}

	return out.String()
}

// isSpace matches what strings.Fields treats as a separator, for the ASCII
// range a statement's indentation actually uses.
func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

func isNumericByte(c byte) bool {
	return (c >= '0' && c <= '9') || c == '.' || c == 'e' || c == 'E' || c == '+' || c == '-'
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
