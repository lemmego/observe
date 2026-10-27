package observe_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestTheRootIsTheStandardLibraryOnly pins this package's dependency set,
// because the shape of the whole module depends on it.
//
// The lesson is lemmego/log's: with its provider inside the module, a
// consumer importing only the core inherited 289 modules — cloud SDKs,
// OpenTelemetry, gRPC — for a package that formats text. Moving the provider
// out took it to 2.
//
// Here the temptation is api/app. Recording what an application did is what
// this package is for, so reaching for the application reads as the obvious
// design — and api/app reaches api/db and therefore a SQL driver, which is
// how notify's own guard caught the same mistake. Keeping it out is also what
// makes observe.Wrap and observe.RecordQuery usable from a program that is
// not a Lemmego application at all, which is the entire answer to "what about
// an app that does not use our stack".
//
// Everything needing the framework belongs in observe/provider.
func TestTheRootIsTheStandardLibraryOnly(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("listing dependencies: %v", err)
	}

	for _, dep := range strings.Fields(string(out)) {
		if dep == "github.com/lemmego/observe" ||
			strings.HasPrefix(dep, "github.com/lemmego/observe/") {
			continue
		}
		// A standard-library path has no dot in its first segment, which is
		// how the toolchain itself tells the two apart.
		first, _, _ := strings.Cut(dep, "/")
		if strings.Contains(first, ".") {
			t.Errorf("the root package depends on %s.\n"+
				"Only the standard library belongs here. If this is api/app, "+
				"it belongs in observe/provider — api/app reaches api/db and "+
				"so a SQL driver, and it would also stop Wrap and RecordQuery "+
				"being usable outside a Lemmego application. If it is a "+
				"watcher's module (orm, cache, tasker, mail), it belongs in "+
				"that watcher's subpackage.", dep)
		}
	}
}

// TestTheStoresDoNotDragDependenciesIntoTheRoot, because a store is chosen by
// an application and the memory one must cost nothing.
func TestTheMemoryStoreIsAlsoStandardLibraryOnly(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "./store/memory").Output()
	if err != nil {
		t.Fatalf("listing the memory store's dependencies: %v", err)
	}

	for _, dep := range strings.Fields(string(out)) {
		if strings.HasPrefix(dep, "github.com/lemmego/observe") {
			continue
		}
		first, _, _ := strings.Cut(dep, "/")
		if strings.Contains(first, ".") {
			t.Errorf("the memory store depends on %s; it is the store a test "+
				"suite and a single-process setup use, so it must cost nothing", dep)
		}
	}
}
