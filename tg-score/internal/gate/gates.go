package gate

import (
	"fmt"
	"sort"

	"github.com/vernier/tg-score/internal/corpus"
)

// Thresholds recovered from the rejection strings on the public script registry.
// These are Telegraph's numbers, not ours; see docs/ACTIVATION-GATES.md for the
// verbatim source of each.
const (
	// StdevThreshold is gate 2's floor, from:
	//   "candidate scores collapsed: stdev=0.0000 <= threshold 0.0500"
	// The comparison is "<=", so a candidate must exceed it, not merely reach it.
	StdevThreshold = 0.05

	// RankAgreementThreshold is gate 3's floor, from:
	//   "rank agreement below threshold (0.60), got: map[...]"
	RankAgreementThreshold = 0.60

	// DeltaPromote is the Catch-Rate margin from whitepaper §12. A challenger
	// scoring at least this far below canonical, on an answer canonical rated
	// above CatchCanonicalFloor, satisfies the promotion condition.
	DeltaPromote = 0.10

	// CatchCanonicalFloor is the canonical score an answer must exceed before a
	// disagreement counts as a catch.
	CatchCanonicalFloor = 0.70

	// DeltaC is the consensus deviation threshold from whitepaper §12. A
	// validator whose local score deviates further than this from the
	// stake-weighted median is penalised, which is the ceiling the correction
	// layer must stay under.
	DeltaC = 0.15
)

// Status is a gate outcome. Undefined exists because a gate can legitimately have
// nothing to measure — an intent group where every reference score is identical
// carries no ranking to agree or disagree with, and scoring that as a failure
// would reject a candidate for the corpus's shortcomings rather than its own.
type Status int

const (
	Pass Status = iota
	Fail
	Undefined
)

func (s Status) String() string {
	switch s {
	case Pass:
		return "PASS"
	case Fail:
		return "FAIL"
	default:
		return "n/a"
	}
}

// ── Gate 1: structural self-match ────────────────────────────────────────────

// Pair is one self-match/cross-match comparison.
type Pair struct {
	Intent      string
	CrossIntent string
	Self        float64
	Cross       float64
}

func (p Pair) Passed() bool { return p.Self > p.Cross }

// Gate1Result reports the structural validation outcome.
//
// Models: "structural validation failed: self-match (0.0000) did not beat
// unrelated cross-match (0.0000)". A module scoring a correct answer no higher
// than an unrelated one is not scoring at all; in practice both figures came
// back as literal zeros, which is what a broken memory ABI produces.
type Gate1Result struct {
	Status    Status
	MeanSelf  float64
	MeanCross float64
	Pairs     []Pair
	PassedN   int
	PassRate  float64
}

// MinPairPassRate is the fraction of individual self/cross pairs that must order
// correctly for gate 1 to pass here.
//
// The network reports gate 1 as a single pair of aggregate figures, so a mean
// comparison is the closest model of its wording. But a mean alone is too
// forgiving: one live rejected binary (registration #8) orders only 4 of 21
// intent pairs correctly and still has a positive mean, because its cross-match
// scores are all exactly zero. Calling that PASS would tell an author their
// module is sound when it is degenerate.
//
// This check is therefore deliberately STRICTER than the network's observed
// message. For a pre-submission tool that is the right bias: being warned about
// something the network might have tolerated costs an hour, and being told PASS
// before a rejection costs a registration and a day.
const MinPairPassRate = 0.60

// Gate1 evaluates structural validation over the supplied pairs.
func Gate1(pairs []Pair) Gate1Result {
	r := Gate1Result{Pairs: pairs}
	if len(pairs) == 0 {
		r.Status = Undefined
		return r
	}
	selfs := make([]float64, 0, len(pairs))
	crosses := make([]float64, 0, len(pairs))
	for _, p := range pairs {
		selfs = append(selfs, p.Self)
		crosses = append(crosses, p.Cross)
		if p.Passed() {
			r.PassedN++
		}
	}
	r.MeanSelf = Mean(selfs)
	r.MeanCross = Mean(crosses)
	r.PassRate = float64(r.PassedN) / float64(len(pairs))
	if r.MeanSelf > r.MeanCross && r.PassRate >= MinPairPassRate {
		r.Status = Pass
	} else {
		r.Status = Fail
	}
	return r
}

// ── Gate 2: score dispersion ─────────────────────────────────────────────────

// Gate2Result reports the dispersion outcome.
//
// This gate is Telegraph's "resistance to gaming" criterion made mechanical: a
// script returning a constant collapses to stdev 0 and is refused, so a scorer
// cannot pass by rating everything identically.
type Gate2Result struct {
	Status Status
	Stdev  float64
	N      int
	Min    float64
	Max    float64
}

// Gate2 evaluates score dispersion across every scored row.
func Gate2(scores []float64) Gate2Result {
	r := Gate2Result{N: len(scores)}
	if len(scores) < 2 {
		r.Status = Undefined
		return r
	}
	r.Stdev = StdDev(scores)
	r.Min, r.Max = scores[0], scores[0]
	for _, s := range scores {
		if s < r.Min {
			r.Min = s
		}
		if s > r.Max {
			r.Max = s
		}
	}
	if r.Stdev > StdevThreshold {
		r.Status = Pass
	} else {
		r.Status = Fail
	}
	return r
}

// ── Gate 3: rank agreement ───────────────────────────────────────────────────

// IntentAgreement is one intent's rank-agreement figure.
type IntentAgreement struct {
	Intent string
	Epoch  int // -1 when epochs are pooled
	N      int
	Rho    float64
	Status Status

	// Why records the reason an undefined group carries no ranking. The
	// distinction matters: a constant REFERENCE is the corpus having nothing to
	// test against, which is nobody's fault, whereas a constant CANDIDATE is the
	// module returning the same score for every miner in the group — the exact
	// degeneracy gate 2 exists to reject, showing up per intent.
	Why string
}

// Gate3Result reports per-intent rank agreement against the reference ranking.
//
// Models: "rank agreement below threshold (0.60), got: map[AGENT_TASK:0.111
// LANGUAGE_GENERATION:-0.462 WEB_SEARCH:-0.677 ...]". Two things about that map
// matter. It is keyed per intent, so one bad intent sinks the submission; and
// its keys are intents the author did not register for, so a candidate is judged
// on intents it never chose.
type Gate3Result struct {
	Status     Status
	Groups     []IntentAgreement
	MeanRho    float64
	MinRho     float64
	MinIntent  string
	FailingN   int
	DefinedN   int
	UndefinedN int

	// ConstantCandidateN counts groups the candidate scored identically across
	// every miner. A high count means the module is not discriminating, which
	// gate 2 will also catch globally but which shows up here per intent.
	ConstantCandidateN int
}

// isConstant reports whether every element of v is identical.
func isConstant(v []float64) bool {
	for i := 1; i < len(v); i++ {
		if v[i] != v[0] {
			return false
		}
	}
	return true
}

// Gate3 computes rank agreement per group between candidate and reference scores.
//
// candidate and reference must be aligned with the rows inside each group.
func Gate3(groups []corpus.Group, candidate, reference map[string]float64) Gate3Result {
	r := Gate3Result{MinRho: 1.0, Status: Pass}

	var rhos []float64
	for _, g := range groups {
		cand := make([]float64, 0, len(g.Rows))
		ref := make([]float64, 0, len(g.Rows))
		for _, row := range g.Rows {
			c, okc := candidate[row.ID]
			f, okf := reference[row.ID]
			if !okc || !okf {
				continue
			}
			cand = append(cand, c)
			ref = append(ref, f)
		}

		ia := IntentAgreement{Intent: g.Intent, Epoch: g.Epoch, N: len(cand)}
		rho, ok := Spearman(cand, ref)
		if !ok {
			ia.Status = Undefined
			switch {
			case len(cand) < 2:
				ia.Why = "too few rows"
			case isConstant(cand) && isConstant(ref):
				ia.Why = "candidate AND reference constant"
			case isConstant(cand):
				ia.Why = "candidate returned one value for every miner"
				r.ConstantCandidateN++
			default:
				ia.Why = "reference has no ordering"
			}
			r.UndefinedN++
			r.Groups = append(r.Groups, ia)
			continue
		}
		ia.Rho = rho
		r.DefinedN++
		if rho >= RankAgreementThreshold {
			ia.Status = Pass
		} else {
			ia.Status = Fail
			r.FailingN++
			r.Status = Fail
		}
		if rho < r.MinRho {
			r.MinRho = rho
			r.MinIntent = g.Key()
		}
		rhos = append(rhos, rho)
		r.Groups = append(r.Groups, ia)
	}

	if r.DefinedN == 0 {
		r.Status = Undefined
		r.MinRho = 0
		return r
	}
	r.MeanRho = Mean(rhos)

	sort.Slice(r.Groups, func(i, j int) bool {
		// Failures first, then ascending rho, so the report leads with the
		// intent closest to sinking the submission.
		if (r.Groups[i].Status == Fail) != (r.Groups[j].Status == Fail) {
			return r.Groups[i].Status == Fail
		}
		if (r.Groups[i].Status == Undefined) != (r.Groups[j].Status == Undefined) {
			return r.Groups[j].Status == Undefined
		}
		return r.Groups[i].Rho < r.Groups[j].Rho
	})
	return r
}

// Headroom reports how far the weakest defined group sits above the threshold.
// Negative means that group is already failing.
func (r Gate3Result) Headroom() float64 { return r.MinRho - RankAgreementThreshold }

// ── Catch-Rate ───────────────────────────────────────────────────────────────

// Catch is one row where the candidate disagreed with canonical by enough to
// satisfy the promotion condition.
type Catch struct {
	RowID     string
	Intent    string
	Epoch     int
	Miner     string
	Canonical float64
	Candidate float64
	Delta     float64
}

// CatchResult is the Catch-Rate promotion table.
//
// Whitepaper §4.3/§12: a challenger script that scores at least delta_promote
// (0.10) below canonical, on at least one answer canonical rated above 0.70, is
// automatically promoted to Canonical for that intent after T_promote (3) epochs.
// Eligible counts the rows that could qualify; Catches counts those that did.
type CatchResult struct {
	Eligible int
	Catches  []Catch
	ByIntent map[string]int
}

// Rate returns caught/eligible, or 0 when nothing was eligible.
func (c CatchResult) Rate() float64 {
	if c.Eligible == 0 {
		return 0
	}
	return float64(len(c.Catches)) / float64(c.Eligible)
}

// CatchRate finds rows satisfying the promotion condition.
func CatchRate(rows []corpus.Row, candidate map[string]float64, canonical map[string]float64) CatchResult {
	res := CatchResult{ByIntent: map[string]int{}}
	for _, row := range rows {
		canon, ok := canonical[row.ID]
		if !ok || canon <= CatchCanonicalFloor {
			continue
		}
		res.Eligible++
		cand, ok := candidate[row.ID]
		if !ok {
			continue
		}
		delta := canon - cand
		if delta >= DeltaPromote {
			res.Catches = append(res.Catches, Catch{
				RowID: row.ID, Intent: row.Intent, Epoch: row.Epoch, Miner: row.Miner,
				Canonical: canon, Candidate: cand, Delta: delta,
			})
			res.ByIntent[row.Intent]++
		}
	}
	sort.Slice(res.Catches, func(i, j int) bool { return res.Catches[i].Delta > res.Catches[j].Delta })
	return res
}

// ── Overall ──────────────────────────────────────────────────────────────────

// Verdict is the combined outcome of the three activation gates.
type Verdict struct {
	Gate1 Gate1Result
	Gate2 Gate2Result
	Gate3 Gate3Result
}

// Activates reports whether all three gates pass. An Undefined gate is not a
// pass: it means the corpus could not test that gate, and the network will.
func (v Verdict) Activates() bool {
	return v.Gate1.Status == Pass && v.Gate2.Status == Pass && v.Gate3.Status == Pass
}

// RejectionReason renders the rejection string the network would produce, in the
// registry's own wording, or "" when all three gates pass.
//
// The gates are reported in registry order — structural, dispersion, agreement —
// because that is the order the observed rejections imply they are applied in.
func (v Verdict) RejectionReason() string {
	if v.Gate1.Status == Fail {
		if v.Gate1.MeanSelf <= v.Gate1.MeanCross {
			// The network's own wording, reproduced.
			return fmt.Sprintf("structural validation failed: self-match (%.4f) did not beat unrelated cross-match (%.4f)",
				v.Gate1.MeanSelf, v.Gate1.MeanCross)
		}
		// The mean passes but most individual pairs do not. This is the stricter
		// local check (see MinPairPassRate), so it is worded as ours rather than
		// dressed up as the network's message.
		return fmt.Sprintf("structural validation weak: only %d of %d intent pairs ranked a matching answer above an unrelated one (%.0f%%, tg-score requires %.0f%%); means were self %.4f vs cross %.4f",
			v.Gate1.PassedN, len(v.Gate1.Pairs), 100*v.Gate1.PassRate, 100*MinPairPassRate,
			v.Gate1.MeanSelf, v.Gate1.MeanCross)
	}
	if v.Gate2.Status == Fail {
		return fmt.Sprintf("candidate scores collapsed: stdev=%.4f <= threshold %.4f",
			v.Gate2.Stdev, StdevThreshold)
	}
	if v.Gate3.Status == Fail {
		parts := make([]string, 0, len(v.Gate3.Groups))
		for _, g := range v.Gate3.Groups {
			if g.Status == Undefined {
				continue
			}
			parts = append(parts, fmt.Sprintf("%s:%.8g", g.Intent, g.Rho))
		}
		return fmt.Sprintf("rank agreement below threshold (%.2f), got: map[%s]",
			RankAgreementThreshold, joinSpace(parts))
	}
	return ""
}

func joinSpace(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " "
		}
		out += p
	}
	return out
}
