package export_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestTheExporterIsTheStandardLibraryOnly.
//
// An application that ships its telemetry somewhere should pay nothing in
// dependencies for it. That is affordable because everything this needs is
// in the standard library — net/http, compress/gzip, encoding/json — and the
// temptation to reach past it is real: a retry library, a metrics client, an
// OTLP encoder.
//
// The guard is the same one observe's root carries, and for the same reason:
// it is cheap now and expensive once something is already linked.
func TestTheExporterIsTheStandardLibraryOnly(t *testing.T) {
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
			t.Errorf("observe/export depends on %s.\n"+
				"Only the standard library and observe's root belong here. If "+
				"this is api/app it belongs in observe/provider; if it is a "+
				"client for a particular collector, it belongs in its own "+
				"package so an application that does not use that collector "+
				"does not link it.", dep)
		}
	}
}
