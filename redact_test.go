package observe_test

import (
	"net/http"
	"testing"

	"github.com/lemmego/observe"
)

// TestACredentialIsWithheldUnderEverySpellingOfItsName.
//
// The list denies by substring, so a name spelled a way nobody thought of is
// recorded in clear — and the three spellings of an API key are not
// interchangeable. "api_key" is how configuration spells it, "apikey" how a Go
// field does, and "api-key" how an HTTP header does: X-Api-Key is the ordinary
// name for the ordinary thing, and matching the first two while missing the
// third is how the first application this was enabled for stored its own key
// on every authenticated request.
func TestACredentialIsWithheldUnderEverySpellingOfItsName(t *testing.T) {
	for _, key := range []string{
		"X-Api-Key", "x-api-key", "X-API-KEY", "Api-Key",
		"api_key", "apikey", "ApiKey", "API_KEY",
		"Authorization", "Cookie", "X-Session-Id", "db_password",
	} {
		if !observe.RedactKey(key) {
			t.Errorf("%q is recorded in clear; it names a credential", key)
		}
	}
}

// TestAnOrdinaryHeaderIsStillRecorded.
//
// The other half of the same contract: over-redacting costs somebody an
// answer, and a request entry whose headers are all "[redacted]" is no more
// use than one with none.
func TestAnOrdinaryHeaderIsStillRecorded(t *testing.T) {
	for _, key := range []string{"Accept", "User-Agent", "Content-Type", "X-Request-Id"} {
		if observe.RedactKey(key) {
			t.Errorf("%q is withheld, and it holds nothing worth withholding", key)
		}
	}
}

// TestRedactHeadersWithholdsAnAPIKeyHeader is the same rule through the
// function the request watcher actually calls, because that is the path a
// credential travels.
func TestRedactHeadersWithholdsAnAPIKeyHeader(t *testing.T) {
	header := http.Header{}
	header.Set("X-Api-Key", "a-real-credential")
	header.Set("Accept", "application/json")

	redacted := observe.RedactHeaders(header)

	if got := redacted["X-Api-Key"]; len(got) != 1 || got[0] != observe.Redacted {
		t.Errorf("X-Api-Key recorded as %v, want withheld", got)
	}
	if got := redacted["Accept"]; len(got) != 1 || got[0] != "application/json" {
		t.Errorf("Accept recorded as %v, want the value", got)
	}
}
