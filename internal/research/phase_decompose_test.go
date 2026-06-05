package research_test

import (
	"context"
	"testing"

	"github.com/georgebuilds/carlos/internal/research"
)

func TestDecompose_LineParsing(t *testing.T) {
	prov := newScriptedProvider("p1",
		"What versions of Safari support WebGPU?\nWhich devices are affected?\nWhat is the rollout timeline?",
	)
	eng := &research.Engine{
		Provider: prov,
		Model:    "m",
		Search:   &fakeSearch{},
		Fetcher:  &fakeFetcher{bodies: map[string]string{}},
	}
	// Stop after decompose by feeding empty search results — we only
	// care about the decomposition result here. Search will set a
	// concern and the engine will fail, but we read Report state.
	report, _ := eng.Run(context.Background(), "WebGPU in Safari?")
	if report == nil {
		t.Fatal("report nil")
	}
	if got, want := len(report.Query.Sub), 3; got != want {
		t.Fatalf("Sub count = %d want %d (subs=%v)", got, want, report.Query.Sub)
	}
	if report.Query.Sub[0] != "What versions of Safari support WebGPU?" {
		t.Errorf("Sub[0] = %q", report.Query.Sub[0])
	}
}

func TestDecompose_StripsBulletAndNumberPrefixes(t *testing.T) {
	prov := newScriptedProvider("p1",
		"- one\n* two\n1. three\n2) four\n• five",
	)
	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search: &fakeSearch{}, Fetcher: &fakeFetcher{bodies: map[string]string{}},
	}
	report, _ := eng.Run(context.Background(), "q")
	want := []string{"one", "two", "three", "four", "five"}
	if len(report.Query.Sub) != len(want) {
		t.Fatalf("len = %d want %d (subs=%v)", len(report.Query.Sub), len(want), report.Query.Sub)
	}
	for i, w := range want {
		if report.Query.Sub[i] != w {
			t.Errorf("Sub[%d] = %q want %q", i, report.Query.Sub[i], w)
		}
	}
}

func TestDecompose_CapsAtMaxSubQueries(t *testing.T) {
	prov := newScriptedProvider("p1",
		"a\nb\nc\nd\ne\nf\ng\nh",
	)
	eng := &research.Engine{
		Provider: prov, Model: "m", MaxSubQueries: 3,
		Search: &fakeSearch{}, Fetcher: &fakeFetcher{bodies: map[string]string{}},
	}
	report, _ := eng.Run(context.Background(), "q")
	if got := len(report.Query.Sub); got != 3 {
		t.Errorf("Sub count = %d want 3 (cap)", got)
	}
}

func TestDecompose_DedupesCaseInsensitively(t *testing.T) {
	prov := newScriptedProvider("p1",
		"Foo bar\nfoo bar\nBaz",
	)
	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search: &fakeSearch{}, Fetcher: &fakeFetcher{bodies: map[string]string{}},
	}
	report, _ := eng.Run(context.Background(), "q")
	if got := len(report.Query.Sub); got != 2 {
		t.Errorf("Sub count = %d want 2 (dedup); subs=%v", got, report.Query.Sub)
	}
}

func TestDecompose_FallbackToQuestionOnEmptyResponse(t *testing.T) {
	prov := newScriptedProvider("p1", "") // empty body
	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search: &fakeSearch{}, Fetcher: &fakeFetcher{bodies: map[string]string{}},
	}
	report, _ := eng.Run(context.Background(), "the original question")
	if len(report.Query.Sub) != 1 || report.Query.Sub[0] != "the original question" {
		t.Errorf("expected fallback to question; got %v", report.Query.Sub)
	}
	// Concern recorded.
	found := false
	for _, c := range report.Concerns {
		if c == "decompose: model returned no parseable sub-queries; falling back to original question" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected fallback concern in %v", report.Concerns)
	}
}
