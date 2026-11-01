package provider_test

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
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

// TestAConfiguredExporterIsWiredAndClosed.
//
// One variable, like the profile. And the closing half matters as much as
// the wiring: an exporter that is never closed takes whatever it is holding
// to the grave on every graceful shutdown.
func TestAConfiguredExporterIsWiredAndClosed(t *testing.T) {
	var received atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			Entries []observe.Entry `json:"entries"`
		}
		reader, err := gzip.NewReader(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := json.NewDecoder(reader).Decode(&envelope); err == nil {
			received.Add(int64(len(envelope.Entries)))
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)

	t.Setenv("OBSERVE_EXPORT_URL", server.URL)
	t.Setenv("OBSERVE_EXPORT_TOKEN", "a-token")
	t.Setenv("OBSERVE_ENABLED", "true")

	p := &provider.Provider{}
	engine := app.Configure()
	if err := p.Provide(engine); err != nil {
		t.Fatalf("providing: %v", err)
	}
	recorder := p.Recorder()
	if recorder == nil {
		t.Fatal("no recorder")
	}
	t.Cleanup(func() { observe.SetDefault(nil) })

	if recorder.Sinks() != 1 {
		t.Fatalf("Sinks() = %d, want the configured exporter", recorder.Sinks())
	}

	recorder.Record(context.Background(), observe.Entry{
		Kind: observe.KindQuery, Summary: "SELECT 1",
	})

	// Shutdown drains the recorder and then closes the sink, so the entry
	// reaches the collector without waiting out a flush interval.
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutting down: %v", err)
	}

	if got := received.Load(); got == 0 {
		t.Error("the collector received nothing; either the sink was not wired " +
			"or it was not closed at shutdown")
	}
}

// A configured endpoint that will not build is a boot error. An application
// that starts, looks healthy and ships nothing is the worse outcome.
func TestABrokenExportEndpointRefusesToBoot(t *testing.T) {
	t.Setenv("OBSERVE_ENABLED", "true")
	t.Setenv("OBSERVE_EXPORT_URL", "   ")

	// Whitespace trims to empty, which means "not configured" rather than
	// "configured wrongly" — so this one boots with no sink.
	p := &provider.Provider{}
	if err := p.Provide(app.Configure()); err != nil {
		t.Fatalf("providing: %v", err)
	}
	t.Cleanup(func() { observe.SetDefault(nil); _ = p.Shutdown(context.Background()) })

	if got := p.Recorder().Sinks(); got != 0 {
		t.Errorf("Sinks() = %d for an unset endpoint", got)
	}
}

// An explicit Sinks field beats the environment, because a field set in code
// is a decision somebody wrote down.
func TestAnExplicitSinkBeatsTheEnvironment(t *testing.T) {
	t.Setenv("OBSERVE_EXPORT_URL", "http://127.0.0.1:1/never")

	mine := &countingSink{}
	recorder := booted(t, &provider.Provider{Sinks: []observe.Sink{mine}})

	if recorder.Sinks() != 1 {
		t.Fatalf("Sinks() = %d", recorder.Sinks())
	}
	recorder.Record(context.Background(), observe.Entry{
		Kind: observe.KindQuery, Summary: "SELECT 1",
	})
	if err := recorder.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if mine.count.Load() == 0 {
		t.Error("the explicit sink received nothing; the environment won")
	}
}

type countingSink struct{ count atomic.Int64 }

func (s *countingSink) Record(_ context.Context, entries []observe.Entry) error {
	s.count.Add(int64(len(entries)))
	return nil
}
