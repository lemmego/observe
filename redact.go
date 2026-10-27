package observe

import (
	"encoding/json"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
)

// Redacted is what replaces a withheld value.
const Redacted = "[redacted]"

// secretKeyFragments are matched against a key, case-insensitively, as
// substrings.
//
// Denying by pattern rather than allowing by list, because an allowlist of
// safe keys has to be extended for every new configuration block and the cost
// of forgetting is a leaked credential — where the cost of over-redacting is
// somebody asking you for a value. This mirrors what lemmego/mcp withholds
// from an agent, for the same reason.
var secretKeyFragments = []string{
	"password", "passwd", "secret", "token", "credential", "salt",
	"apikey", "api_key", "private", "signature", "authorization",
	"cookie", "session", "dsn", "jwt", "bearer", "otp", "pin",
}

// RedactKey reports whether a key's name alone means its value must not be
// recorded.
func RedactKey(key string) bool {
	lower := strings.ToLower(key)
	for _, fragment := range secretKeyFragments {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	return false
}

// RedactValue renders a value for storage, withholding what must not be kept.
//
// A bound query parameter and a request header are where a password actually
// arrives, so this runs over both.
func RedactValue(key string, value any) string {
	if RedactKey(key) {
		return Redacted
	}
	rendered := renderValue(value)
	return RedactInline(rendered)
}

// RedactInline rewrites a credential found *inside* a value.
//
// This is the case a key-based rule alone gets wrong, and it is not
// hypothetical: DATABASE_URL is the standard name for a connection string, its
// name contains no secret-looking fragment, and its value contains the
// password. The host, user and database survive; the password does not.
func RedactInline(value string) string {
	if !strings.Contains(value, "://") {
		return value
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.User == nil {
		return value
	}
	if _, hasPassword := parsed.User.Password(); !hasPassword {
		return value
	}
	parsed.User = url.UserPassword(parsed.User.Username(), Redacted)
	return parsed.String()
}

// RedactMap copies a map, withholding secret values and walking nested maps.
//
// Nested, because sql.connections.pgsql.password is where a password lives —
// a rule that only looked at top-level keys would miss every one that
// matters.
func RedactMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		switch {
		case RedactKey(key):
			out[key] = Redacted
		default:
			switch typed := value.(type) {
			case map[string]any:
				out[key] = RedactMap(typed)
			case string:
				out[key] = RedactInline(typed)
			default:
				out[key] = value
			}
		}
	}
	return out
}

// RedactHeaders copies a header map, withholding the values that carry
// credentials.
//
// Takes and returns map[string][]string so it works on an http.Header without
// this package importing net/http — which it must not, because the root is
// the standard library only and net/http would be fine, but the same
// discipline keeps the signature usable from a watcher that is not serving
// HTTP at all.
func RedactHeaders(in map[string][]string) map[string][]string {
	out := make(map[string][]string, len(in))
	for key, values := range in {
		if RedactKey(key) {
			out[key] = []string{Redacted}
			continue
		}
		copied := make([]string, len(values))
		for i, v := range values {
			copied[i] = RedactInline(v)
		}
		out[key] = copied
	}
	return out
}

func renderValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return "NULL"
	case string:
		return typed
	case []byte:
		// Not rendered as text: a []byte is as likely to be a hashed password
		// or an encrypted column as a readable string, and either way its
		// length is the only part worth a debugger's attention.
		return "[]byte(" + itoa(len(typed)) + ")"
	case bool:
		return strconv.FormatBool(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return "?"
		}
		return string(encoded)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func mustJSON(v any) json.RawMessage {
	encoded, err := json.Marshal(v)
	if err != nil {
		slog.Error("observe: a payload could not be encoded", "error", err)
		return nil
	}
	return encoded
}
