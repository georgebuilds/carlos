// Package research is the Phase 11 slice 11c research orchestrator
// engine. It turns a substantive question into a structured research
// arc: plan sub-queries, search the web for each, fetch top sources,
// extract relevant passages, synthesize a cited report, verify
// citation coverage.
//
// The engine is a pure compute. It returns a Report struct; it does
// NOT touch the event log, the approval queue, or the TUI. Persistence
// + chat / slash-command / TUI wiring lives in a separate follow-up
// slice.
//
// Architectural commitments:
//
//   - Pipeline is explicit phases, not magic. Each phase has its own
//     file and a Run(ctx) shape; failures abort but surface the
//     partial state.
//   - Citation tracking is load-bearing. Every Passage carries a
//     stable ID; the synthesis prompt forces the model to cite by ID
//     so the verify phase can audit coverage.
//   - Cost-bounded. ResearchBudget caps provider calls, fetched
//     bytes, and wall-clock. Refusals are clean - partial Report is
//     returned with a Concerns entry.
//   - Deterministic phase order:
//     decompose → search → fetch → read → synthesize → verify.
//   - Cancellation-aware. ctx propagates to provider streams and HTTP
//     fetches; a cancellation mid-phase returns cleanly with whatever
//     was filled in so far.
package research

import (
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// Query is the user's question plus the decomposed sub-queries the
// decompose phase fills in.
type Query struct {
	Question string   // the user's original question
	Sub      []string // decomposed sub-queries (filled by decompose phase)
}

// Source is one fetched page. ID is stable across the run ("s1", "s2",
// …) and lets passages back-reference where they came from.
type Source struct {
	ID        string    // stable: "s1", "s2", … assigned at fetch time
	URL       string    // the URL we asked to fetch (post-search choice)
	Title     string    // page title (from <title>); may be empty
	Content   string    // text-extracted body (capped by the Fetcher)
	FetchedAt time.Time // UTC
}

// Passage is one model-extracted, relevant excerpt from a Source. The
// ID flows from read → synthesize → verify so the citation auditor
// can score coverage.
type Passage struct {
	ID        string // "p1", "p2", …
	SourceID  string // back-ref to Source.ID
	Text      string // the extracted passage
	Relevance int    // 1-10, model-assigned during read phase
}

// Report is the engine's output. It always carries Question + the
// partial state of every phase that ran; on failure, callers can still
// see what was gathered before the abort.
type Report struct {
	Question     string                    // the input question, echoed
	Query        Query                     // includes Sub after decompose
	Routing      []SubQueryRoute           // model-picked backends + tailored queries (route phase)
	Sources      []Source                  // every source the engine fetched
	Passages     []Passage                 // every passage the read phase extracted
	Synthesis    string                    // markdown body with inline [pN] citations
	Verification *agent.VerificationReport // synthesis-quality judge (if Judge configured)
	Citations    *agent.Audit              // citation coverage audit (if synthesis ran)
	Concerns     []string                  // free-form issues surfaced during the run
	Budget       BudgetUsage               // what we spent
}

// SubQueryRoute is the model's plan for ONE sub-query: which backends
// to hit, and a tailored query string for each. Populated by the route
// phase; consumed by the search phase. When the route phase falls back
// (LLM failure, malformed output), the engine fills this with a
// default plan (every backend, verbatim sub-query) so search has
// something to consume — never crashes the run.
type SubQueryRoute struct {
	SubQuery string          // the original sub-query text, for traceability
	Searches []PlannedSearch // 1..N planned backend calls; empty means fallback for this row
}

// PlannedSearch is one (backend, tailored query, result cap) tuple
// inside a SubQueryRoute. The engine clamps MaxResults to PerBackendCap
// before issuing the call.
type PlannedSearch struct {
	Backend    string // backend Name() — must match one MultiBackend exposes; unknown silently dropped
	Query      string // query string tailored to this backend's strengths
	MaxResults int    // per-call result cap; clamped to PerBackendCap by the engine
}

// BudgetUsage tracks the running spend against ResearchBudget. The
// engine updates ProviderCalls + FetchedBytes inline; Elapsed is set
// at the end of Run.
type BudgetUsage struct {
	ProviderCalls int
	FetchedBytes  int64
	Elapsed       time.Duration
}

// ResearchBudget caps the engine's spend. A zero field means
// unlimited for that axis. The engine's defaults (filled at the top
// of Run when fields are zero) are conservative: ~20 calls, 32 MiB,
// 5 min wall-clock - enough headroom for the documented v0 use cases
// without risking a runaway research arc.
type ResearchBudget struct {
	MaxProviderCalls int           // 0 = unlimited; default 20
	MaxFetchedBytes  int64         // 0 = unlimited; default 32 MiB
	MaxWallClock     time.Duration // 0 = unlimited; default 5 min
}

// Defaults applied by Engine.Run when a budget field is zero. Exposed
// as named constants so tests can reference them and changes here
// trigger a green/red change rather than a silent drift.
const (
	DefaultMaxProviderCalls = 20
	DefaultMaxFetchedBytes  = int64(32 * 1024 * 1024) // 32 MiB
	DefaultMaxWallClock     = 5 * time.Minute

	DefaultMaxSubQueries   = 5
	DefaultSourcesPerQuery = 3

	// Caps the route phase enforces over whatever the model proposes.
	// PerSubQueryCap is the total result ceiling across all backends for
	// one sub-query; PerBackendCap is the per-(backend, sub-query) cap.
	// Both are HARD: the engine truncates and records a concern.
	DefaultPerSubQueryCap = 8
	DefaultPerBackendCap  = 5
)
