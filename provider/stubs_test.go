package provider_test

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/lemmego/observe/provider"
	"github.com/lemmego/observe/store/sqlstore"
)

// TestThePublishedMigrationIsValidGo.
//
// The stub is a string, so nothing compiles it until somebody publishes it
// into their project and runs `go build` — which is three commands after the
// mistake was made, in their repository rather than this one.
func TestThePublishedMigrationIsValidGo(t *testing.T) {
	if _, err := parser.ParseFile(token.NewFileSet(), "migration.go",
		provider.MigrationStub, parser.AllErrors); err != nil {
		t.Fatalf("the published migration is not valid Go: %v", err)
	}
}

// TestThePublishedMigrationMatchesTheSchema.
//
// The stub spells its DDL out rather than calling SchemaStatements, because
// that is the whole point of publishing it: the file becomes the project's to
// edit. The cost is that the two can drift, and the symptom of drift is a
// column the store writes and the table does not have — which fails at the
// first entry, from inside somebody's request.
func TestThePublishedMigrationMatchesTheSchema(t *testing.T) {
	// Every column the store reads and writes.
	columns := []string{
		"id", "kind", "request_id", "recorded_at", "duration_ns",
		"level", "summary", "data", "error", "watcher",
	}
	for _, column := range columns {
		if !strings.Contains(provider.MigrationStub, `"`+column+`"`) {
			t.Errorf("the published migration does not create %q, which the "+
				"store writes on every entry", column)
		}
	}

	// tags arrives in a second migration rather than the first, because the
	// first has already run everywhere it was going to run — so the pair is
	// what has to match the schema, not either file alone.
	if !strings.Contains(provider.TagsMigrationStub, "ADD COLUMN tags") {
		t.Error("nothing published adds the tags column, which the store " +
			"writes on every entry")
	}
	if !strings.Contains(provider.SourceMigrationStub, "ADD COLUMN source") {
		t.Error("nothing published adds the source column, which the store " +
			"writes on every entry")
	}
	if strings.Contains(provider.MigrationStub, `"tags"`) {
		t.Error("the create migration now makes tags as well, so publishing " +
			"both would ALTER a column that already exists")
	}

	// And the indexes, by the names the schema uses. They are not decoration:
	// the first serves the only question this table exists to answer, and the
	// second is what the list view's cursor pages on.
	for _, statement := range sqlstore.SchemaStatements(sqlstore.Postgres, "")[1:] {
		name := indexName(statement)
		if name == "" {
			t.Fatalf("could not read an index name out of %q", statement)
		}
		if !strings.Contains(provider.MigrationStub, name) {
			t.Errorf("the published migration is missing index %s", name)
		}
	}
}

// TestTheMigrationCreatesItsIndexesRaw.
//
// The schema builder names a multi-column index after its columns alone, and
// index names are schema-global on Postgres and database-global on SQLite —
// so two tables each wanting an index on (request_id, recorded_at) would
// collide and the second would fail the batch.
func TestTheMigrationCreatesItsIndexesRaw(t *testing.T) {
	if !strings.Contains(provider.MigrationStub, "CREATE INDEX idx_observe_entries_request") {
		t.Error("the request index is not created with raw SQL, so its name " +
			"will not carry the table and will collide")
	}
	if strings.Contains(provider.MigrationStub, `t.Index("request_id", "recorded_at")`) {
		t.Error("a multi-column index goes through the schema builder, which " +
			"names it after its columns alone")
	}
}

// TestThePublishTagIsNamespaced, because --tags is a hard selector and a bare
// "migrations" would sweep in every other package that publishes one.
func TestThePublishTagIsNamespaced(t *testing.T) {
	if !strings.HasPrefix(provider.TagMigrations, "observe-") {
		t.Errorf("TagMigrations = %q, want an observe- prefix", provider.TagMigrations)
	}
	if !strings.Contains(provider.InstallInstructions, provider.TagMigrations) {
		t.Error("the install instructions name a tag that publish does not accept")
	}
}

// TestTheMigrationVersionIsFixed.
//
// A generated timestamp would differ on every run, so publishing twice would
// leave two migrations creating the same table — and the migrator appends a
// duplicate version rather than rejecting it, so one would run twice.
func TestTheMigrationVersionIsFixed(t *testing.T) {
	if !strings.Contains(provider.MigrationStub, provider.MigrationVersion) {
		t.Errorf("the stub registers a version other than %s, so a republish "+
			"would not be a no-op", provider.MigrationVersion)
	}
	if len(provider.MigrationVersion) != 14 {
		t.Errorf("MigrationVersion = %q, want a 14-digit timestamp",
			provider.MigrationVersion)
	}
}

func indexName(statement string) string {
	const marker = "CREATE INDEX IF NOT EXISTS "
	start := strings.Index(statement, marker)
	if start < 0 {
		return ""
	}
	rest := statement[start+len(marker):]
	end := strings.Index(rest, " ")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// TestBothPublishedMigrationsAreValidGo, for the reason the first one is
// checked: nothing compiles a string until somebody publishes it into their
// project, which is three commands after the mistake was made and in their
// repository rather than this one.
func TestBothPublishedMigrationsAreValidGo(t *testing.T) {
	for name, stub := range map[string]string{
		"create": provider.MigrationStub,
		"tags":   provider.TagsMigrationStub,
		"source": provider.SourceMigrationStub,
	} {
		if _, err := parser.ParseFile(token.NewFileSet(), name+".go", stub,
			parser.AllErrors); err != nil {
			t.Errorf("the %s migration is not valid Go: %v", name, err)
		}
	}
}

// TestTheTagsMigrationIsOrderedAfterTheCreate.
//
// The migrator runs in version order, so an ALTER that sorted before its
// CREATE would fail against a table that does not exist yet — on a first
// publish, which is the one case nobody tests twice.
func TestTheTagsMigrationIsOrderedAfterTheCreate(t *testing.T) {
	if provider.TagsMigrationVersion <= provider.MigrationVersion {
		t.Errorf("TagsMigrationVersion %s does not sort after MigrationVersion %s",
			provider.TagsMigrationVersion, provider.MigrationVersion)
	}
	if !strings.Contains(provider.TagsMigrationStub, provider.TagsMigrationVersion) {
		t.Error("the tags stub registers a version other than TagsMigrationVersion")
	}
	if provider.SourceMigrationVersion <= provider.TagsMigrationVersion {
		t.Errorf("SourceMigrationVersion %s does not sort after %s",
			provider.SourceMigrationVersion, provider.TagsMigrationVersion)
	}
	if !strings.Contains(provider.SourceMigrationStub, provider.SourceMigrationVersion) {
		t.Error("the source stub registers a version other than SourceMigrationVersion")
	}
}

// TestEveryPublishableIsNamespacedAndDistinct.
//
// Two publishables writing one path would mean the second silently replaced
// the first, and a migration file that is not there is a column the store
// writes and the table does not have.
func TestEveryPublishableIsNamespacedAndDistinct(t *testing.T) {
	p := &provider.Provider{}
	seen := map[string]bool{}
	for _, publishable := range p.AddPublishables() {
		if !strings.HasPrefix(publishable.Tag, "observe-") {
			t.Errorf("%s carries tag %q, want an observe- prefix",
				publishable.FilePath, publishable.Tag)
		}
		if seen[publishable.FilePath] {
			t.Errorf("two publishables write %s", publishable.FilePath)
		}
		seen[publishable.FilePath] = true
	}
	if len(seen) != 3 {
		t.Errorf("published %d files, want the create, tags and source migrations",
			len(seen))
	}
}

// TestThePublishedMigrationTakesItsPrecisionFromTheDialect.
//
// A literal DATETIME(6) here made the table writable and unreadable on
// SQLite, which is the default dialect of every scaffolded project. SQLite
// stores no types: a driver reads a column's Go type from the type it was
// declared with, and glebarez/go-sqlite maps DATETIME to time.Time while
// DATETIME(6) comes back a string — so entries recorded cleanly and every
// read answered "entries could not be read". MySQL needs the 6, so the fix is
// a dialect check and not a deletion.
//
// Nothing else could have caught it: storetest builds its tables from
// SchemaStatements, which has always been right, and only a published project
// ever runs this file. TestSchemaStatementsCoverEveryDialect is the other half
// of the pair, and sqlstore's own test pins the driver behaviour underneath.
func TestThePublishedMigrationTakesItsPrecisionFromTheDialect(t *testing.T) {
	if strings.Contains(provider.MigrationStub, `t.DateTime("recorded_at", 6)`) {
		t.Error("recorded_at is published with a fixed precision of 6, which " +
			"SQLite cannot read back")
	}
	if !strings.Contains(provider.MigrationStub, `t.DateTime("recorded_at", recordedAtPrecision())`) {
		t.Error("recorded_at no longer takes its precision from the dialect")
	}
	// The helper has to be published with it, or the file does not compile.
	// TestThePublishedMigrationIsValidGo parses it and a type error is not a
	// parse error, so this is asserted rather than inferred.
	for _, needed := range []string{
		"func recordedAtPrecision() uint {", `"os"`, `"strings"`,
		`os.Getenv("DB_DRIVER")`, "return 6",
	} {
		if !strings.Contains(provider.MigrationStub, needed) {
			t.Errorf("the published migration is missing %q, so it will not "+
				"compile in a project", needed)
		}
	}
}
