package research_test

import (
	"context"
	"errors"
	"testing"

	"github.com/georgebuilds/carlos/internal/research"
	"github.com/georgebuilds/carlos/internal/tools"
)

// Budget exhausts AFTER read but BEFORE synthesize; fires the
// runSynthesize callProvider-err branch.
func TestRunSynthesize_BudgetExhaustedBeforeSynthesize(t *testing.T) {
	prov := newScriptedProvider("p1",
		"sub1",
		`{"text":"x","relevance":7}`,
		// synthesize would consume the 3rd call but cap is 2.
	)
	fs := &fakeSearch{defaultResults: []tools.SearchResult{{Rank: 1, URL: "https://a.example.com"}}}
	ff := &fakeFetcher{bodies: map[string]string{"https://a.example.com": "alpha"}}
	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search: fs, Fetcher: ff, SourcesPerQuery: 1,
		Budget: research.ResearchBudget{MaxProviderCalls: 2}, // dec + read only
	}
	_, err := eng.Run(context.Background(), "q?")
	if err == nil {
		t.Fatal("expected budget abort")
	}
	if !errors.Is(err, research.ErrBudgetExceeded) {
		t.Errorf("err = %v want ErrBudgetExceeded", err)
	}
}

// ctx canceled mid-search loop: pre-cancel the ctx and let decompose
// run via a fast-completing scripted provider; search will see the
// cancellation as it iterates the second sub-query.
func TestRunSearch_CtxCancelMidLoop(t *testing.T) {
	prov := newScriptedProvider("p1",
		"sub1\nsub2\nsub3",
		`{"text":"x","relevance":7}`,
		"synth",
	)
	// Slow backend: search blocks on ctx → after first iter, when the
	// loop checks ctx.Err it sees canceled.
	fs := &slowBackend{}
	ff := &fakeFetcher{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search: fs, Fetcher: ff, SourcesPerQuery: 1,
	}
	_, err := eng.Run(ctx, "q?")
	if err == nil {
		t.Fatal("expected cancel error")
	}
}

// slowBackend immediately returns nothing; combined with a pre-canceled
// ctx, the search loop's ctx.Err check trips on the second sub-query.
type slowBackend struct{}

func (slowBackend) Name() string { return "slow" }
func (slowBackend) Search(ctx context.Context, _ string, _ int) ([]tools.SearchResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, nil
}
