package observe

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
)

// MaxSourceLength bounds the stored identity.
//
// It goes in a column on every row, so it is capped for the same reason tags
// are: what a process knows about itself mostly came from its environment,
// and an environment variable is not a length anybody checked.
const MaxSourceLength = 128

// Source identifies the process that recorded an entry.
//
// Fleet telemetry is incoherent without it. Two instances' entries are
// indistinguishable, so "which host is slow", "did the new release do this"
// and "is it only the workers" are unaskable — and a shared store across
// several web processes is the normal deployment, not the exotic one.
//
// It rides in two places and the split is deliberate. The full structured
// value goes in the export envelope, once per batch, because that is where
// it belongs when the batch is the unit. A compact rendering goes in a
// column on each entry, because the local dashboard reads rows and has no
// envelope to consult.
type Source struct {
	// Service names the application. Several services may share one store.
	Service string `json:"service,omitempty"`

	// Environment is production, staging, whatever the deployment calls
	// itself.
	Environment string `json:"environment,omitempty"`

	// Release identifies the build — a commit, a tag, a deploy id. The one
	// field that makes "did this start after the deploy" answerable.
	Release string `json:"release,omitempty"`

	// Host is the machine or container.
	Host string `json:"host,omitempty"`

	// Instance distinguishes one run from the next on the same host, so a
	// restart is visible rather than looking like continuous history.
	Instance string `json:"instance,omitempty"`
}

// DefaultSource fills in what a process can learn about itself.
//
// Host from the operating system and Instance newly minted, so two runs on
// one machine are told apart. Service, Environment and Release are the
// application's to supply: nothing here can guess them, and guessing wrong
// would be worse than leaving them empty — an entry labelled with the wrong
// release is an entry that sends somebody to the wrong commit.
func DefaultSource() Source {
	return Source{
		Host:     hostname(),
		Instance: newInstanceID(),
	}
}

// newInstanceID returns a short random identifier for this run.
//
// Random and not the front of a NewID, which is the mistake this replaced.
// NewID is sortable by time: its first eight bytes are the timestamp, so the
// first eight hex characters of one are four bytes of nanoseconds — about
// four seconds of resolution. Two replicas starting together would share an
// instance id, and replicas start together is exactly what replicas do after
// a deploy.
//
// Four bytes rather than sixteen because this only has to separate the
// processes alive at one time, and it is rendered into a column on every
// row.
func newInstanceID() string {
	var buf [4]byte
	// crypto/rand.Read cannot fail as of Go 1.24: it panics rather than
	// returning an error, so there is nothing to handle here.
	rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}

// hostname names this machine, or says it could not.
//
// Not an error. A container that will not report its hostname is not a
// reason to refuse to boot, and "unknown" in a fleet view is more useful
// than an application that did not start.
func hostname() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "unknown"
	}
	return name
}

// IsZero reports whether nothing was configured.
func (s Source) IsZero() bool {
	return s.Service == "" && s.Environment == "" && s.Release == "" &&
		s.Host == "" && s.Instance == ""
}

// String renders the source compactly, for the column on an entry.
//
// service/host/instance, with empty parts omitted. Self-describing on
// purpose: a reader looking at a row should learn where it came from without
// a second table to join, and a substring filter over it answers "everything
// this instance did" with no new index and no new query.
//
// Environment and Release are left out. Environment is constant for a given
// store in every deployment worth having, and a release changes often enough
// that putting it in every row would be the one field that stops the column
// compressing — it goes in the export envelope instead, where it costs once
// per batch.
func (s Source) String() string {
	parts := make([]string, 0, 3)
	for _, part := range []string{s.Service, s.Host, s.Instance} {
		if part = strings.TrimSpace(part); part != "" {
			parts = append(parts, part)
		}
	}
	rendered := strings.Join(parts, "/")
	if len(rendered) > MaxSourceLength {
		rendered = rendered[:MaxSourceLength]
	}
	return rendered
}
