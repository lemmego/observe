package provider_test

import (
	"os"
	"testing"

	"github.com/lemmego/api/app"
	"github.com/lemmego/observe"
	"github.com/lemmego/observe/provider"
)

// booted runs the provider against a bare engine and returns the recorder it
// installed.
func booted(t *testing.T, p *provider.Provider) *observe.Recorder {
	t.Helper()
	t.Setenv("OBSERVE_ENABLED", "true")

	engine := app.Configure()
	if err := p.Provide(engine); err != nil {
		t.Fatalf("providing: %v", err)
	}
	recorder := p.Recorder()
	if recorder == nil {
		t.Fatal("recording is on but no recorder was installed")
	}
	t.Cleanup(func() {
		observe.SetDefault(nil)
		_ = p.Shutdown(t.Context())
	})
	return recorder
}

// TestTheProductionProfileReachesTheRecorder.
//
// One variable rather than five that have to be remembered together, which
// is only true if the one variable actually arrives.
func TestTheProductionProfileReachesTheRecorder(t *testing.T) {
	t.Setenv(observe.EnvProfile, "production")
	recorder := booted(t, &provider.Provider{})

	if recorder.RecordsArgs() {
		t.Error("bound parameters are still recorded under the production profile")
	}
	if !recorder.NormalisesStatements() {
		t.Error("statements are not normalised under the production profile")
	}
}

func TestDevelopmentKeepsEverything(t *testing.T) {
	t.Setenv(observe.EnvProfile, "development")
	recorder := booted(t, &provider.Provider{})

	if !recorder.RecordsArgs() {
		t.Error("bound parameters are dropped in development, where the whole " +
			"point is the detail")
	}
	if recorder.NormalisesStatements() {
		t.Error("statements are normalised in development")
	}
}

// TestTheProductionProfileSamplesWithoutBeingAsked.
//
// A profile having a default rate is the point of it: production means a
// tenth unless somebody says otherwise, rather than meaning a tenth only if
// they remembered a second variable.
//
// Asserted on the behaviour of the installed sampler rather than on a
// number read back out, because a rate that was configured and not wired is
// exactly the failure worth catching.
func TestTheProductionProfileSamplesWithoutBeingAsked(t *testing.T) {
	t.Setenv(observe.EnvProfile, "production")
	os.Unsetenv("OBSERVE_SAMPLE")
	recorder := booted(t, &provider.Provider{})

	kept := 0
	const requests = 2000
	for range requests {
		if recorder.ShouldSampleRequest(observe.NewID()) {
			kept++
		}
	}
	// Generous bounds: the claim is that a rate below one is in force, not
	// that FNV is a perfect hash.
	if kept == 0 || kept > requests/3 {
		t.Errorf("kept %d of %d requests; want roughly a tenth", kept, requests)
	}
}

// And an explicit variable wins, including one asking for everything back.
func TestObserveSampleOverridesTheProfile(t *testing.T) {
	t.Setenv(observe.EnvProfile, "production")
	t.Setenv("OBSERVE_SAMPLE", "1")
	recorder := booted(t, &provider.Provider{})

	for range 200 {
		if !recorder.ShouldSampleRequest(observe.NewID()) {
			t.Fatal("a request was sampled out with OBSERVE_SAMPLE=1")
		}
	}
}

// An explicit Sampler field beats both, because a field set in code is a
// decision somebody wrote down.
func TestAnExplicitSamplerBeatsTheEnvironment(t *testing.T) {
	t.Setenv(observe.EnvProfile, "production")
	t.Setenv("OBSERVE_SAMPLE", "0")

	recorder := booted(t, &provider.Provider{
		Sampler: func(string) bool { return true },
	})
	if !recorder.ShouldSampleRequest(observe.NewID()) {
		t.Error("the explicit Sampler was overridden by the environment")
	}
}
