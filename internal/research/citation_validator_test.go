package research

import (
	"reflect"
	"testing"
)

func passages(ids ...string) []Passage {
	out := make([]Passage, 0, len(ids))
	for _, id := range ids {
		out = append(out, Passage{ID: id})
	}
	return out
}

func TestValidatePassageCitations(t *testing.T) {
	tests := []struct {
		name      string
		synthesis string
		passages  []Passage
		want      CitationValidation
	}{
		{
			name:      "all valid",
			synthesis: "First claim [p1]. Second claim [p2].",
			passages:  passages("p1", "p2"),
			want: CitationValidation{
				TotalCitations: 2,
				Cited:          []string{"p1", "p2"},
				Valid:          []string{"p1", "p2"},
				Unknown:        nil,
			},
		},
		{
			name:      "single hallucinated id",
			synthesis: "Wild claim [p99].",
			passages:  passages("p1", "p2"),
			want: CitationValidation{
				TotalCitations: 1,
				Cited:          []string{"p99"},
				Valid:          nil,
				Unknown:        []string{"p99"},
			},
		},
		{
			name:      "mix of valid and unknown",
			synthesis: "Good [p1] and bad [p9] and good [p2].",
			passages:  passages("p1", "p2"),
			want: CitationValidation{
				TotalCitations: 3,
				Cited:          []string{"p1", "p2", "p9"},
				Valid:          []string{"p1", "p2"},
				Unknown:        []string{"p9"},
			},
		},
		{
			name:      "zero citations",
			synthesis: "No citations here at all.",
			passages:  passages("p1"),
			want:      CitationValidation{},
		},
		{
			name:      "duplicate citations counted once distinctly",
			synthesis: "Claim [p1]. Restated [p1]. Again [p1] and [p2].",
			passages:  passages("p1", "p2"),
			want: CitationValidation{
				TotalCitations: 4,
				Cited:          []string{"p1", "p2"},
				Valid:          []string{"p1", "p2"},
				Unknown:        nil,
			},
		},
		{
			name:      "duplicate hallucinated counted once distinctly",
			synthesis: "Bad [p9]. Bad again [p9].",
			passages:  passages("p1"),
			want: CitationValidation{
				TotalCitations: 2,
				Cited:          []string{"p9"},
				Valid:          nil,
				Unknown:        []string{"p9"},
			},
		},
		{
			name:      "malformed brackets ignored",
			synthesis: "Empty [p]. Alpha [pX]. Numeric [12]. Spaced [p 1]. Real [p3].",
			passages:  passages("p3"),
			want: CitationValidation{
				TotalCitations: 1,
				Cited:          []string{"p3"},
				Valid:          []string{"p3"},
				Unknown:        nil,
			},
		},
		{
			name:      "passages empty means every cite is unknown",
			synthesis: "Claim [p1] and [p2].",
			passages:  nil,
			want: CitationValidation{
				TotalCitations: 2,
				Cited:          []string{"p1", "p2"},
				Valid:          nil,
				Unknown:        []string{"p1", "p2"},
			},
		},
		{
			name:      "numeric sort not lexical",
			synthesis: "[p10] [p2] [p1].",
			passages:  passages("p1", "p2", "p10"),
			want: CitationValidation{
				TotalCitations: 3,
				Cited:          []string{"p1", "p2", "p10"},
				Valid:          []string{"p1", "p2", "p10"},
				Unknown:        nil,
			},
		},
		{
			name:      "fenced code citation ignored",
			synthesis: "Real [p1].\n```\nexample [p99] here\n```\n",
			passages:  passages("p1"),
			want: CitationValidation{
				TotalCitations: 1,
				Cited:          []string{"p1"},
				Valid:          []string{"p1"},
				Unknown:        nil,
			},
		},
		{
			name:      "inline code citation ignored",
			synthesis: "Real [p1] but `not [p99]` in code.",
			passages:  passages("p1"),
			want: CitationValidation{
				TotalCitations: 1,
				Cited:          []string{"p1"},
				Valid:          []string{"p1"},
				Unknown:        nil,
			},
		},
		{
			name:      "empty synthesis",
			synthesis: "",
			passages:  passages("p1"),
			want:      CitationValidation{},
		},
		{
			name:      "passage with empty id never matches",
			synthesis: "Claim [p1].",
			passages:  []Passage{{ID: ""}, {ID: "p1"}},
			want: CitationValidation{
				TotalCitations: 1,
				Cited:          []string{"p1"},
				Valid:          []string{"p1"},
				Unknown:        nil,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := validatePassageCitations(tc.synthesis, tc.passages)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("validatePassageCitations()\n got: %+v\nwant: %+v", got, tc.want)
			}
		})
	}
}

func TestCitationValidationOK(t *testing.T) {
	if !(CitationValidation{}).OK() {
		t.Error("zero-value CitationValidation should be OK")
	}
	if !(CitationValidation{Valid: []string{"p1"}}).OK() {
		t.Error("all-valid CitationValidation should be OK")
	}
	if (CitationValidation{Unknown: []string{"p9"}}).OK() {
		t.Error("CitationValidation with unknown IDs should not be OK")
	}
}

func TestPassageNum(t *testing.T) {
	tests := []struct {
		id     string
		want   int
		wantOK bool
	}{
		{"p1", 1, true},
		{"p42", 42, true},
		{"p0", 0, true},
		{"x1", 0, false},
		{"p", 0, false},
		{"pX", 0, false},
	}
	for _, tc := range tests {
		got, ok := passageNum(tc.id)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("passageNum(%q) = (%d, %v), want (%d, %v)", tc.id, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestSortPassageIDsEmpty(t *testing.T) {
	if got := sortPassageIDs(map[string]bool{}); got != nil {
		t.Errorf("sortPassageIDs(empty) = %v, want nil", got)
	}
}
