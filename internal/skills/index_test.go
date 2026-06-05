package skills_test

import (
	"context"
	"math"
	"testing"

	"github.com/georgebuilds/carlos/internal/skills"
)

// TestEmbedder_Deterministic: same input → same output across calls.
func TestEmbedder_Deterministic(t *testing.T) {
	e := skills.DeterministicEmbedder{}
	v1, err := e.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatal(err)
	}
	v2, err := e.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatal(err)
	}
	if len(v1) != len(v2) {
		t.Fatalf("length: %d vs %d", len(v1), len(v2))
	}
	for i := range v1 {
		if v1[i] != v2[i] {
			t.Errorf("[%d] diverged: %v vs %v", i, v1[i], v2[i])
			return
		}
	}
}

// TestEmbedder_DifferentInputs: distinct strings produce distinct
// vectors (the SHA stir ensures this for plausible inputs).
func TestEmbedder_DifferentInputs(t *testing.T) {
	e := skills.DeterministicEmbedder{}
	v1, _ := e.Embed(context.Background(), "alpha")
	v2, _ := e.Embed(context.Background(), "beta")
	same := true
	for i := range v1 {
		if v1[i] != v2[i] {
			same = false
			break
		}
	}
	if same {
		t.Error("expected differing vectors for different inputs")
	}
}

// TestEmbedder_CustomDim: Dim controls output length.
func TestEmbedder_CustomDim(t *testing.T) {
	e := skills.DeterministicEmbedder{Dim: 64}
	v, err := e.Embed(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 64 {
		t.Errorf("want dim 64, got %d", len(v))
	}
}

// TestIndex_AddTopKBasic: TopK returns the correct top-1 for a query
// that matches one skill exactly.
func TestIndex_AddTopKBasic(t *testing.T) {
	idx := skills.NewIndex()
	a := &skills.Skill{Name: "a", Description: "alpha"}
	b := &skills.Skill{Name: "b", Description: "beta"}
	emb := skills.DeterministicEmbedder{}
	ea, _ := emb.Embed(context.Background(), a.Description)
	eb, _ := emb.Embed(context.Background(), b.Description)
	if err := idx.Add(a, ea); err != nil {
		t.Fatal(err)
	}
	if err := idx.Add(b, eb); err != nil {
		t.Fatal(err)
	}

	// Querying with the exact "alpha" embedding should put 'a' on top.
	q, _ := emb.Embed(context.Background(), "alpha")
	top := idx.TopK(q, 2)
	if len(top) != 2 {
		t.Fatalf("want 2 results, got %d", len(top))
	}
	if top[0].Skill.Name != "a" {
		t.Errorf("top1 should be 'a', got %q", top[0].Skill.Name)
	}
	// Self-cosine is 1.0 modulo float noise.
	if math.Abs(top[0].Similarity-1.0) > 1e-6 {
		t.Errorf("self-similarity want ~1.0, got %v", top[0].Similarity)
	}
}

// TestIndex_AddDimMismatch: 2nd Add with wrong dim returns an error.
func TestIndex_AddDimMismatch(t *testing.T) {
	idx := skills.NewIndex()
	if err := idx.Add(&skills.Skill{Name: "a"}, []float32{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	err := idx.Add(&skills.Skill{Name: "b"}, []float32{1, 2, 3, 4})
	if err == nil {
		t.Error("want dim mismatch error")
	}
}

// TestIndex_AddNil: nil skill rejected.
func TestIndex_AddNil(t *testing.T) {
	idx := skills.NewIndex()
	if err := idx.Add(nil, []float32{1}); err == nil {
		t.Error("want nil-skill error")
	}
}

// TestIndex_TopKEmpty: zero entries → nil result.
func TestIndex_TopKEmpty(t *testing.T) {
	idx := skills.NewIndex()
	if got := idx.TopK([]float32{1, 2, 3}, 5); got != nil {
		t.Errorf("want nil, got %v", got)
	}
}

// TestIndex_MaxSimilarity returns the max cosine.
func TestIndex_MaxSimilarity(t *testing.T) {
	idx := skills.NewIndex()
	emb := skills.DeterministicEmbedder{}
	a := &skills.Skill{Name: "a", Description: "alpha"}
	ea, _ := emb.Embed(context.Background(), a.Description)
	_ = idx.Add(a, ea)
	// Identical query → max=1.0.
	q, _ := emb.Embed(context.Background(), "alpha")
	got := idx.MaxSimilarity(q)
	if math.Abs(got-1.0) > 1e-6 {
		t.Errorf("want ~1.0, got %v", got)
	}
}

// TestIndex_BuildIndex builds from a slice of skills.
func TestIndex_BuildIndex(t *testing.T) {
	a := &skills.Skill{Name: "a", Description: "alpha"}
	b := &skills.Skill{Name: "b", Description: "beta"}
	idx, err := skills.BuildIndex(context.Background(), skills.DeterministicEmbedder{}, []*skills.Skill{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if idx.Len() != 2 {
		t.Errorf("want 2 entries, got %d", idx.Len())
	}
}
