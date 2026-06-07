// metrics.go - skill-induction instrumentation.
//
// # Why this exists
//
// SPEC § Instrumentation: "carlos instruments these signals from day
// one, because no prior research has measured them and they are the
// direct operationalizations of 'skills people keep'."
//
// The signals:
//
//   - acceptance_rate - fraction of proposals the user approves. Top-
//     line precision proxy. Target ≥ 50% (DESIGN § Cost model).
//   - post-acceptance edit distance - how much the user rewrites before
//     approving. Draft-quality proxy. (Not computed here; see Phase 6+
//     wiring note - needs a `pre` blob alongside the accepted
//     SKILL.md, which we'll record at promote time.)
//   - reuse_count + time_to_first_reuse - directly per skill.
//   - survival curves at 30 / 60 / 90 days - fraction of induced skills
//     still active at each cutoff.
//
// # No private state
//
// Metrics holds no persistent state of its own - every reading is
// derived from (a) the SkillLibrary on disk and (b) the agent event
// log. At carlos scale (hundreds of skills, low thousands of
// proposals), a full scan per readout is fine. When the volume grows
// past where this is comfortable, the same pattern as the agents
// projection table applies: materialize a `skill_metrics` projection
// updated incrementally from EvtApproval* events.
package skillwire

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/skills"
)

// Metrics computes induction telemetry from the event log + library.
// The struct is config-only; no caching across calls.
type Metrics struct {
	// SkillProposalKind is the artifact kind we count as "induced
	// skill". Exposed as a field so tests can override; defaults to
	// agent.ArtifactKindSkillProposal.
	SkillProposalKind string
}

// NewMetrics returns a Metrics with sane defaults.
func NewMetrics() *Metrics {
	return &Metrics{SkillProposalKind: agent.ArtifactKindSkillProposal}
}

// Report is the printable snapshot.
type Report struct {
	TotalProposals   int       `json:"total_proposals"`
	Accepted         int       `json:"accepted"`
	Rejected         int       `json:"rejected"`
	Pending          int       `json:"pending"`
	AcceptanceRate   float64   `json:"acceptance_rate"`
	ActiveSkills     int       `json:"active_skills"`
	StaleSkills      int       `json:"stale_skills"`
	ArchivedSkills   int       `json:"archived_skills"`
	TotalReuseCount  int       `json:"total_reuse_count"`
	Survival30dCount int       `json:"survival_30d_count"`
	Survival60dCount int       `json:"survival_60d_count"`
	Survival90dCount int       `json:"survival_90d_count"`
	Survival30dRatio float64   `json:"survival_30d_ratio"`
	Survival60dRatio float64   `json:"survival_60d_ratio"`
	Survival90dRatio float64   `json:"survival_90d_ratio"`
	GeneratedAt      time.Time `json:"generated_at"`
}

// String returns a multi-line printable summary. Useful for `carlos
// skills metrics` CLI output (future slice).
func (r Report) String() string {
	out, _ := json.MarshalIndent(r, "", "  ")
	return string(out)
}

// AcceptanceRate returns accepted / (accepted + rejected). Pending
// proposals are EXCLUDED from the denominator - they have no decision
// yet, so attributing them either way distorts the precision read.
// Returns 0 when no decided proposals exist.
func (m *Metrics) AcceptanceRate(ctx context.Context, log *agent.SQLiteEventLog) (float64, error) {
	if log == nil {
		return 0, fmt.Errorf("metrics: nil log")
	}
	a, r, _, err := m.proposalCounts(ctx, log)
	if err != nil {
		return 0, err
	}
	if a+r == 0 {
		return 0, nil
	}
	return float64(a) / float64(a+r), nil
}

// Snapshot returns a fully-populated Report. lib MAY be nil (then
// active/stale/archived counts are 0 and survival ratios are 0).
// `now` is supplied so tests can drive the survival cutoffs
// deterministically.
func (m *Metrics) Snapshot(ctx context.Context, log *agent.SQLiteEventLog, lib *skills.Library, now time.Time) (Report, error) {
	rep := Report{GeneratedAt: now}
	if log != nil {
		a, r, p, err := m.proposalCounts(ctx, log)
		if err != nil {
			return rep, err
		}
		rep.Accepted = a
		rep.Rejected = r
		rep.Pending = p
		rep.TotalProposals = a + r + p
		if a+r > 0 {
			rep.AcceptanceRate = float64(a) / float64(a+r)
		}
	}
	if lib != nil {
		for _, s := range lib.Active {
			if s == nil {
				continue
			}
			rep.TotalReuseCount += s.ReuseCount
			switch s.Status {
			case skills.StatusStale:
				rep.StaleSkills++
			case skills.StatusArchived:
				rep.ArchivedSkills++
			default:
				rep.ActiveSkills++
			}
			// Survival: a skill counts as "surviving at N days" if its
			// Created is older than N days AND it's still active (not
			// archived). The 30/60/90 cohorts overlap by design.
			age := now.Sub(s.Created)
			if age >= 30*24*time.Hour && s.Status != skills.StatusArchived {
				rep.Survival30dCount++
			}
			if age >= 60*24*time.Hour && s.Status != skills.StatusArchived {
				rep.Survival60dCount++
			}
			if age >= 90*24*time.Hour && s.Status != skills.StatusArchived {
				rep.Survival90dCount++
			}
		}
		// Ratios use total INDUCED skills (active+stale+archived) as
		// the denominator - same cohort whether they survived or not.
		totalInduced := rep.ActiveSkills + rep.StaleSkills + rep.ArchivedSkills
		if totalInduced > 0 {
			rep.Survival30dRatio = float64(rep.Survival30dCount) / float64(totalInduced)
			rep.Survival60dRatio = float64(rep.Survival60dCount) / float64(totalInduced)
			rep.Survival90dRatio = float64(rep.Survival90dCount) / float64(totalInduced)
		}
	}
	return rep, nil
}

// proposalCounts scans the event log for skill_proposal artifacts and
// their accept/reject resolutions. Returns (accepted, rejected,
// pending, error).
//
// Implementation note: we issue ONE cross-namespace events query for
// every approval-queue event, then filter in-memory to those whose
// ArtifactRef.Kind matches our SkillProposalKind. At v0 scale the full
// scan is cheap (low thousands of events); the same projection-table
// upgrade path applies if it becomes hot.
func (m *Metrics) proposalCounts(ctx context.Context, log *agent.SQLiteEventLog) (accepted, rejected, pending int, err error) {
	kind := m.SkillProposalKind
	if kind == "" {
		kind = agent.ArtifactKindSkillProposal
	}

	rows, err := log.DB().QueryContext(ctx, `
		SELECT type, payload FROM events
		WHERE type IN (?, ?, ?)
		ORDER BY seq ASC
	`, string(agent.EvtApprovalProposed), string(agent.EvtApprovalAccepted), string(agent.EvtApprovalRejected))
	if err != nil {
		return 0, 0, 0, fmt.Errorf("metrics: query approvals: %w", err)
	}
	defer rows.Close()

	type state int
	const (
		stPending state = iota
		stAccepted
		stRejected
	)
	tracked := map[string]state{} // artifact id → current state
	isSkill := map[string]bool{}  // artifact id → kind == skill_proposal

	for rows.Next() {
		var (
			typeS   string
			payload []byte
		)
		if err := rows.Scan(&typeS, &payload); err != nil {
			return 0, 0, 0, err
		}
		switch agent.EventType(typeS) {
		case agent.EvtApprovalProposed:
			var p agent.ApprovalProposalPayload
			if err := json.Unmarshal(payload, &p); err != nil {
				continue
			}
			if p.Ref.Kind != kind {
				continue
			}
			isSkill[p.Ref.ID] = true
			tracked[p.Ref.ID] = stPending
		case agent.EvtApprovalAccepted:
			var r agent.ApprovalResolutionPayload
			if err := json.Unmarshal(payload, &r); err != nil {
				continue
			}
			if !isSkill[r.ArtifactID] {
				continue
			}
			tracked[r.ArtifactID] = stAccepted
		case agent.EvtApprovalRejected:
			var r agent.ApprovalResolutionPayload
			if err := json.Unmarshal(payload, &r); err != nil {
				continue
			}
			if !isSkill[r.ArtifactID] {
				continue
			}
			tracked[r.ArtifactID] = stRejected
		}
	}
	if err := rows.Err(); err != nil {
		return 0, 0, 0, err
	}

	for _, st := range tracked {
		switch st {
		case stAccepted:
			accepted++
		case stRejected:
			rejected++
		default:
			pending++
		}
	}
	return accepted, rejected, pending, nil
}
