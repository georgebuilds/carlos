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

// TestEmbedder_InvalidDim: a negative Dim is rejected.
func TestEmbedder_InvalidDim(t *testing.T) {
	e := skills.DeterministicEmbedder{Dim: -1}
	if _, err := e.Embed(context.Background(), "x"); err == nil {
		t.Error("want error for negative dim")
	}
}

// TestIndex_AddEmptyEmbedding: an empty embedding is rejected so we
// never seed the index with a zero-length vector.
func TestIndex_AddEmptyEmbedding(t *testing.T) {
	idx := skills.NewIndex()
	if err := idx.Add(&skills.Skill{Name: "a"}, nil); err == nil {
		t.Error("want error for empty embedding")
	}
}

// TestIndex_Dim reports the pinned dimension; 0 before the first Add.
func TestIndex_Dim(t *testing.T) {
	idx := skills.NewIndex()
	if idx.Dim() != 0 {
		t.Errorf("empty index Dim: want 0, got %d", idx.Dim())
	}
	if err := idx.Add(&skills.Skill{Name: "a"}, []float32{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if idx.Dim() != 3 {
		t.Errorf("Dim after Add: want 3, got %d", idx.Dim())
	}
}

// TestIndex_TopKDimMismatch: a query whose dimension differs from the
// pinned index dimension yields nil (caller mis-configured embedder).
func TestIndex_TopKDimMismatch(t *testing.T) {
	idx := skills.NewIndex()
	_ = idx.Add(&skills.Skill{Name: "a"}, []float32{1, 2, 3})
	if got := idx.TopK([]float32{1, 2}, 3); got != nil {
		t.Errorf("want nil on dim mismatch, got %v", got)
	}
	if got := idx.TopK([]float32{1, 2, 3}, 0); got != nil {
		t.Errorf("want nil for k<=0, got %v", got)
	}
}

// TestIndex_TopKClampAndTieBreak: k larger than the entry count clamps
// to the count, and equal similarities break ties by skill name.
func TestIndex_TopKTieBreakAndClamp(t *testing.T) {
	idx := skills.NewIndex()
	// Two skills with the SAME embedding → identical similarity to any
	// query → tie broken by name (z-skill should sort after a-skill).
	_ = idx.Add(&skills.Skill{Name: "z-skill"}, []float32{1, 0, 0})
	_ = idx.Add(&skills.Skill{Name: "a-skill"}, []float32{1, 0, 0})
	got := idx.TopK([]float32{1, 0, 0}, 10) // k > len → clamp to 2
	if len(got) != 2 {
		t.Fatalf("want 2 results (clamped), got %d", len(got))
	}
	if got[0].Skill.Name != "a-skill" {
		t.Errorf("tie should break to 'a-skill' first, got %q", got[0].Skill.Name)
	}
}

// TestIndex_MaxSimilarityGuards: empty index and dim mismatch both
// return 0.
func TestIndex_MaxSimilarityGuards(t *testing.T) {
	idx := skills.NewIndex()
	if idx.MaxSimilarity([]float32{1, 2, 3}) != 0 {
		t.Error("empty index MaxSimilarity should be 0")
	}
	_ = idx.Add(&skills.Skill{Name: "a"}, []float32{1, 2, 3})
	if idx.MaxSimilarity([]float32{1, 2}) != 0 {
		t.Error("dim mismatch MaxSimilarity should be 0")
	}
}

// errEmbedder always fails; used to exercise BuildIndex's embed-error
// branch.
type errEmbedder struct{}

func (errEmbedder) Embed(context.Context, string) ([]float32, error) {
	return nil, context.DeadlineExceeded
}

// TestIndex_BuildIndexEmbedError: an embedder failure aborts BuildIndex
// and names the skill in the error.
func TestIndex_BuildIndexEmbedError(t *testing.T) {
	_, err := skills.BuildIndex(context.Background(), errEmbedder{}, []*skills.Skill{
		{Name: "boom", Description: "x"},
	})
	if err == nil {
		t.Fatal("want embed error")
	}
}

// TestIndex_ZeroVectorSimilarity: a query that is the all-zero vector
// yields similarity 0 against any entry (the cosine zero-norm guard),
// so MaxSimilarity returns 0 and TopK still returns the entries but at
// zero score.
func TestIndex_ZeroVectorSimilarity(t *testing.T) {
	idx := skills.NewIndex()
	if err := idx.Add(&skills.Skill{Name: "a"}, []float32{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	zero := []float32{0, 0, 0}
	if got := idx.MaxSimilarity(zero); got != 0 {
		t.Errorf("all-zero query MaxSimilarity: want 0, got %v", got)
	}
	top := idx.TopK(zero, 1)
	if len(top) != 1 {
		t.Fatalf("want 1 result, got %d", len(top))
	}
	if top[0].Similarity != 0 {
		t.Errorf("all-zero query similarity: want 0, got %v", top[0].Similarity)
	}
}

// TestIndex_BuildIndexSkipsNil: nil skills in the slice are skipped.
func TestIndex_BuildIndexSkipsNil(t *testing.T) {
	idx, err := skills.BuildIndex(context.Background(), skills.DeterministicEmbedder{}, []*skills.Skill{
		nil,
		{Name: "a", Description: "alpha"},
		nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	if idx.Len() != 1 {
		t.Errorf("want 1 entry (nils skipped), got %d", idx.Len())
	}
}
