// index.go — description-embedding index for top-k retrieval.
//
// Voyager pattern (verified from the paper, Fig. 4): index by skill
// description, retrieve top-k by cosine similarity over the description
// embeddings, then progressively load bodies only when the model
// confirms relevance. This is explicitly NOT RAG over conversation text.
//
// # The v0 embedder
//
// The Embedder interface is the seam. The v0 implementation,
// DeterministicEmbedder, is a SHA-256-derived 256-dim float vector with
// per-byte sign. It is deterministic, dependency-free, and good enough
// to validate the wiring (the cosine top-k machinery is provably
// correct over any non-zero vectors). It is NOT a real semantic
// embedder — two unrelated descriptions will not cluster by topic.
//
// # Production wiring (deferred to Phase 6+)
//
// Phase 6+ will swap in a real embedder behind the same interface:
//
//   - Anthropic: no first-party embedding API as of late 2025; will
//     either use Claude with a structured-prompt embedding workaround or
//     defer to OpenAI/local.
//   - OpenAI: text-embedding-3-small (1536d) or -large (3072d).
//   - Local: a small ONNX model run via the existing CGO-free
//     interpreter once available.
//
// The interface is intentionally narrow: one method, Embed(ctx, text),
// returns a float32 vector of arbitrary dimension. The Index does not
// pin a dimension — the first Add wins; subsequent Adds with mismatched
// dimensions return an error.
package skills

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
)

// Embedder turns a piece of text into a fixed-dimension float vector.
// Implementations must be deterministic for the same input (real
// embedders satisfy this by construction; we rely on it for cache
// invalidation logic in a future slice).
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// DeterministicEmbedder is the v0 stub. Produces a 256-dim vector from
// the SHA-256 of the input (8 bits per dim, mapped to [-1, +1]). Not a
// semantic embedder — wire a real one before relying on cluster quality
// in production.
type DeterministicEmbedder struct {
	// Dim controls the output vector length. Zero defaults to 256. The
	// hash output is exactly 32 bytes; for Dim > 256 we cycle the bytes.
	Dim int
}

// Embed implements Embedder.
func (d DeterministicEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	dim := d.Dim
	if dim == 0 {
		dim = 256
	}
	if dim < 1 {
		return nil, fmt.Errorf("embedder: dim %d invalid", dim)
	}
	sum := sha256.Sum256([]byte(text))
	out := make([]float32, dim)
	for i := 0; i < dim; i++ {
		// Cycle through hash bytes. Map [0,255] -> [-1, +1] via /127.5-1.
		b := sum[i%len(sum)]
		// Stir slightly so cycles don't all collide: XOR with a
		// running hash of (text-len, i).
		var seed [16]byte
		binary.BigEndian.PutUint64(seed[:8], uint64(len(text)))
		binary.BigEndian.PutUint64(seed[8:], uint64(i))
		stir := sha256.Sum256(seed[:])
		b ^= stir[i%len(stir)]
		out[i] = float32(b)/127.5 - 1.0
	}
	return out, nil
}

// Sanity-check the deterministic embedder satisfies the interface.
var _ Embedder = DeterministicEmbedder{}

// IndexEntry pairs a skill with its description embedding. The Skill
// pointer is held by reference; mutating the underlying skill after Add
// changes what later TopK calls see (intentional — when the curator
// updates a skill, the index should reflect it next query).
type IndexEntry struct {
	Skill *Skill
	Emb   []float32
}

// Index is the in-memory description-embedding index. Not safe for
// concurrent mutation; callers serialize Add. Read-only TopK is safe
// for concurrent callers provided no Add is racing.
type Index struct {
	entries []IndexEntry
	dim     int // pinned by the first Add; subsequent dims must match
}

// NewIndex returns an empty index.
func NewIndex() *Index {
	return &Index{}
}

// Add inserts a skill+embedding pair. The first call pins the index
// dimension; mismatched dimensions on later Adds return an error so the
// caller learns immediately rather than silently producing zero-score
// results downstream.
func (idx *Index) Add(s *Skill, emb []float32) error {
	if s == nil {
		return errors.New("index: nil skill")
	}
	if len(emb) == 0 {
		return errors.New("index: empty embedding")
	}
	if idx.dim == 0 {
		idx.dim = len(emb)
	} else if len(emb) != idx.dim {
		return fmt.Errorf("index: dim mismatch (have %d, got %d)", idx.dim, len(emb))
	}
	idx.entries = append(idx.entries, IndexEntry{Skill: s, Emb: append([]float32(nil), emb...)})
	return nil
}

// Len returns the entry count.
func (idx *Index) Len() int { return len(idx.entries) }

// Dim returns the pinned vector dimension; 0 if empty.
func (idx *Index) Dim() int { return idx.dim }

// ScoredSkill is one (skill, similarity) pair returned by TopK.
type ScoredSkill struct {
	Skill      *Skill
	Similarity float64
}

// TopK returns up to k skills ranked by descending cosine similarity to
// queryEmb. Ties are broken by skill name to keep results stable.
// k <= 0 returns nil. A dimension mismatch yields an empty slice — the
// caller mis-configured the embedder.
func (idx *Index) TopK(queryEmb []float32, k int) []ScoredSkill {
	if k <= 0 || len(idx.entries) == 0 {
		return nil
	}
	if len(queryEmb) != idx.dim {
		return nil
	}
	scored := make([]ScoredSkill, 0, len(idx.entries))
	for _, e := range idx.entries {
		s := cosine(queryEmb, e.Emb)
		scored = append(scored, ScoredSkill{Skill: e.Skill, Similarity: s})
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Similarity != scored[j].Similarity {
			return scored[i].Similarity > scored[j].Similarity
		}
		ni, nj := "", ""
		if scored[i].Skill != nil {
			ni = scored[i].Skill.Name
		}
		if scored[j].Skill != nil {
			nj = scored[j].Skill.Name
		}
		return ni < nj
	})
	if k > len(scored) {
		k = len(scored)
	}
	return scored[:k]
}

// MaxSimilarity returns the highest cosine similarity between queryEmb
// and any entry, or 0 if the index is empty / dim mismatches. Used by
// the novelty conjunct in the trigger evaluator: novelty = 1 - max_cos.
func (idx *Index) MaxSimilarity(queryEmb []float32) float64 {
	if len(idx.entries) == 0 || len(queryEmb) != idx.dim {
		return 0
	}
	best := math.Inf(-1)
	for _, e := range idx.entries {
		if s := cosine(queryEmb, e.Emb); s > best {
			best = s
		}
	}
	if math.IsInf(best, -1) {
		return 0
	}
	return best
}

// BuildIndex is a convenience: embed every skill description and stuff
// it into a fresh Index. Returns the first embed error; on success the
// index is fully populated.
func BuildIndex(ctx context.Context, emb Embedder, skills []*Skill) (*Index, error) {
	idx := NewIndex()
	for _, s := range skills {
		if s == nil {
			continue
		}
		v, err := emb.Embed(ctx, s.Description)
		if err != nil {
			return nil, fmt.Errorf("index: embed %q: %w", s.Name, err)
		}
		if err := idx.Add(s, v); err != nil {
			return nil, fmt.Errorf("index: add %q: %w", s.Name, err)
		}
	}
	return idx, nil
}

// cosine returns the cosine similarity between two equal-length
// vectors. Assumes len(a) == len(b); callers above check this.
// Returns 0 if either vector is all-zero.
func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
