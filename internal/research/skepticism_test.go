package research

// White-box tests for the source-skepticism helpers (item 11g):
// formatPassageManifest trust markers, singleSourcedIDs, and
// flagWeakSources. Package `research` so the unexported helpers are
// reachable, matching whitebox_more_test.go.

import (
	"strings"
	"testing"
)

func TestFormatPassageManifest_CarriesTrustMarker(t *testing.T) {
	passages := []Passage{
		{ID: "p1", SourceID: "s1", Text: "trusted claim"},
		{ID: "p2", SourceID: "s2", Text: "weak claim"},
	}
	sources := []Source{
		{ID: "s1", URL: "https://arxiv.org/abs/1", Reputation: Reputation{Tier: TierTrusted, Score: 2}},
		{ID: "s2", URL: "http://low.example", Reputation: Reputation{Tier: TierLow, Score: 0}},
	}
	got := formatPassageManifest(passages, sources)
	if !strings.Contains(got, "[p1] from https://arxiv.org/abs/1 (trust=trusted): trusted claim") {
		t.Errorf("trusted marker line missing; got:\n%s", got)
	}
	if !strings.Contains(got, "(trust=low): weak claim") {
		t.Errorf("low marker line missing; got:\n%s", got)
	}
	// Two distinct sources => neither is single-sourced.
	if strings.Contains(got, "single-source") {
		t.Errorf("did not expect single-source tag with two distinct sources; got:\n%s", got)
	}
}

func TestFormatPassageManifest_SingleSourceTagged(t *testing.T) {
	passages := []Passage{
		{ID: "p1", SourceID: "s1", Text: "only claim"},
		{ID: "p2", SourceID: "s1", Text: "another from same source"},
	}
	sources := []Source{
		{ID: "s1", URL: "http://lonely.example", Reputation: Reputation{Tier: TierLow}},
	}
	got := formatPassageManifest(passages, sources)
	if !strings.Contains(got, "(trust=low, single-source): only claim") {
		t.Errorf("expected single-source tag on the only source; got:\n%s", got)
	}
}

func TestSingleSourcedIDs(t *testing.T) {
	tests := []struct {
		name     string
		passages []Passage
		sources  []Source
		wantSet  map[string]bool
	}{
		{
			name: "one distinct url flags all",
			passages: []Passage{
				{ID: "p1", SourceID: "s1"},
				{ID: "p2", SourceID: "s1"},
			},
			sources: []Source{{ID: "s1", URL: "http://a.example"}},
			wantSet: map[string]bool{"s1": true},
		},
		{
			name: "two distinct urls flags none",
			passages: []Passage{
				{ID: "p1", SourceID: "s1"},
				{ID: "p2", SourceID: "s2"},
			},
			sources: []Source{
				{ID: "s1", URL: "http://a.example"},
				{ID: "s2", URL: "http://b.example"},
			},
			wantSet: map[string]bool{},
		},
		{
			name:     "no passages flags none",
			passages: nil,
			sources:  []Source{{ID: "s1", URL: "http://a.example"}},
			wantSet:  map[string]bool{},
		},
		{
			name: "orphan passage (no url) yields no cited url, flags none",
			passages: []Passage{
				{ID: "p1", SourceID: "ghost"},
			},
			sources: []Source{{ID: "s1", URL: "http://a.example"}},
			// urlsCited is empty (orphan has no URL) so len <= 1 path
			// runs and flags every passage's SourceID.
			wantSet: map[string]bool{"ghost": true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := singleSourcedIDs(tt.passages, tt.sources)
			if len(got) != len(tt.wantSet) {
				t.Fatalf("set size = %d (%v) want %d (%v)", len(got), got, len(tt.wantSet), tt.wantSet)
			}
			for k, v := range tt.wantSet {
				if got[k] != v {
					t.Errorf("set[%q] = %v want %v (full=%v)", k, got[k], v, got)
				}
			}
		})
	}
}

func TestFlagWeakSources_WeakSingleSourceConcern(t *testing.T) {
	report := &Report{
		Passages: []Passage{
			{ID: "p1", SourceID: "s1", Text: "x"},
			{ID: "p2", SourceID: "s1", Text: "y"},
		},
		Sources: []Source{
			{ID: "s1", URL: "http://weak.example", Reputation: Reputation{Tier: TierLow}},
		},
	}
	flagWeakSources(report)
	if !concernPresent(report.Concerns, "entire report rests on a single source") {
		t.Errorf("expected single-source summary concern; got %v", report.Concerns)
	}
	if !concernPresent(report.Concerns, "weak source s1") {
		t.Errorf("expected weak-source concern for s1; got %v", report.Concerns)
	}
	// Deduped: s1 has two passages but should appear once.
	count := 0
	for _, c := range report.Concerns {
		if strings.Contains(c, "weak source s1") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("weak source s1 concern count = %d want 1 (%v)", count, report.Concerns)
	}
}

func TestFlagWeakSources_TrustedSingleSourceNotWeak(t *testing.T) {
	report := &Report{
		Passages: []Passage{{ID: "p1", SourceID: "s1", Text: "x"}},
		Sources: []Source{
			{ID: "s1", URL: "https://arxiv.org/abs/1", Reputation: Reputation{Tier: TierTrusted}},
		},
	}
	flagWeakSources(report)
	// Single distinct source => summary concern fires...
	if !concernPresent(report.Concerns, "entire report rests on a single source") {
		t.Errorf("expected single-source summary concern; got %v", report.Concerns)
	}
	// ...but a TRUSTED single source is not flagged "weak".
	if concernPresent(report.Concerns, "weak source") {
		t.Errorf("trusted single source should not be weak; got %v", report.Concerns)
	}
}

func TestFlagWeakSources_MultiSourceNoWeakConcern(t *testing.T) {
	report := &Report{
		Passages: []Passage{
			{ID: "p1", SourceID: "s1"},
			{ID: "p2", SourceID: "s2"},
		},
		Sources: []Source{
			{ID: "s1", URL: "http://a.example", Reputation: Reputation{Tier: TierLow}},
			{ID: "s2", URL: "http://b.example", Reputation: Reputation{Tier: TierMedium}},
		},
	}
	flagWeakSources(report)
	if concernPresent(report.Concerns, "weak source") {
		t.Errorf("multi-source pool should not flag weak; got %v", report.Concerns)
	}
	if concernPresent(report.Concerns, "single source") {
		t.Errorf("multi-source pool should not flag single-source summary; got %v", report.Concerns)
	}
}

func TestSynthesizeSystem_HasSkepticismInstruction(t *testing.T) {
	// The synthesis prompt must instruct the model to caveat weak /
	// single-source claims, and must reference the trust marker shape.
	for _, want := range []string{"trust=", "single-source", "caveat"} {
		if !strings.Contains(synthesizeSystem, want) {
			t.Errorf("synthesizeSystem missing %q instruction", want)
		}
	}
	// No em-dashes in the user-facing prompt.
	if strings.Contains(synthesizeSystem, "—") {
		t.Errorf("synthesizeSystem contains an em-dash")
	}
}

func concernPresent(concerns []string, substr string) bool {
	for _, c := range concerns {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}
