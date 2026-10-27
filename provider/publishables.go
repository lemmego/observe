package provider

import (
	"path/filepath"

	"github.com/lemmego/api/app"
	"github.com/lemmego/api/utils"
)

// Publish tags, namespaced by the module.
//
// `lemmego run publish --tags` selects on these and the selection is real, so
// a bare "migrations" would sweep in every other package that ever publishes
// one.
const (
	TagMigrations = "observe-migrations"
)

// MigrationVersion is the version the published migration registers under.
//
// A fixed constant rather than a timestamp taken when the file is written. A
// generated timestamp would differ on every run, so publishing twice would
// leave two migrations creating the same table — and the migrator appends a
// duplicate version to its ordered list rather than rejecting it, so one of
// them would run twice. A constant makes a second publish a no-op, which is
// what anyone running it expects.
const MigrationVersion = "20261028000000"

// AddPublishables offers the migration to the project.
//
// Published rather than run at boot because the schema is the application's:
// published, it can be read, edited, rolled back alongside every other
// migration, and found in schema_migrations where an operator expects it.
// Creating the table silently at startup would put a table in somebody's
// database that nothing in their project mentions.
//
// Paths come from the project's configured directories rather than the
// conventional ones, so a project that moved its migrations gets the file
// where it asked for it.
func (p *Provider) AddPublishables() []*app.Publishable {
	return []*app.Publishable{
		{
			FilePath: filepath.Join(utils.MigrationPath(),
				MigrationVersion+"_create_observe_entries_table.go"),
			Content: []byte(MigrationStub),
			Tag:     TagMigrations,
		},
	}
}

// InstallInstructions is the sequence after publishing.
//
// Interpolated from the tag constant so it cannot drift from what `publish`
// actually accepts.
var InstallInstructions = `
Next:

  1. lemmego run publish --tags=` + TagMigrations + `
  2. go build ./...          # the published migration must be compiled in
  3. lemmego run migrate up

Step 2 is not optional and is the easy one to miss: the migration was written
by a running binary that does not contain it yet.

Then set OBSERVE_ENABLED=true, and give Provider.DashboardAuth a predicate —
until you do, the dashboard refuses every request, because these entries hold
request headers, bound query parameters and SQL.
`
