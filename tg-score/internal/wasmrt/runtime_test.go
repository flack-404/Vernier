package wasmrt

import (
	"context"
	"math"
	"os"
	"testing"
)

// wasmUnderTest points at a built scoring module. These tests exercise the
// memory ABI against a real binary rather than a mock, because the ABI is
// exactly what a mock would get wrong.
func wasmUnderTest(t *testing.T) string {
	t.Helper()
	p := os.Getenv("TG_TEST_WASM")
	if p == "" {
		p = "../../../vernier/target/wasm32-unknown-unknown/release/vernier.wasm"
	}
	if _, err := os.Stat(p); err != nil {
		t.Skipf("no wasm to test against (%s); set TG_TEST_WASM", p)
	}
	return p
}

func openPool(t *testing.T, n int) *Pool {
	t.Helper()
	p, err := Open(context.Background(), wasmUnderTest(t), n)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

// The headline ABI check: a module that is wired up correctly scores a matching
// answer strictly higher than an unrelated one. This is gate 1 in miniature, and
// the two-zeros rejection on the public registry is what failing it looks like.
func TestSelfMatchBeatsCrossMatch(t *testing.T) {
	p := openPool(t, 1)

	q := "What is the current price of Solana?"
	gt := "Solana (SOL) is trading at $94.06 USD as of the latest market data."
	unrelated := "The Eiffel Tower is a wrought-iron lattice tower in Paris, completed in 1889."

	self, err := p.Score(q, gt, gt)
	if err != nil {
		t.Fatalf("self-match score: %v", err)
	}
	cross, err := p.Score(q, gt, unrelated)
	if err != nil {
		t.Fatalf("cross-match score: %v", err)
	}
	t.Logf("self=%.4f cross=%.4f", self, cross)

	if self == 0 && cross == 0 {
		t.Fatal("both scores are exactly zero — the memory ABI is not wired up")
	}
	if self <= cross {
		t.Errorf("self-match %.4f must beat cross-match %.4f", self, cross)
	}
}

// The module returns 0 for a whitespace-only answer before touching the pointer,
// so (0,0) must be an acceptable way to pass an empty string.
func TestEmptyAnswerScoresZero(t *testing.T) {
	p := openPool(t, 1)
	for _, ma := range []string{"", "   ", "\n\t "} {
		got, err := p.Score("What is the price of SOL?", "SOL is $94.12 USD.", ma)
		if err != nil {
			t.Fatalf("score(%q): %v", ma, err)
		}
		if got != 0 {
			t.Errorf("empty answer %q scored %.6f, want 0", ma, got)
		}
	}
}

// Scores must land in [0,1]; the module clamps, and a value outside the range
// means the composite escaped its clamp.
func TestScoresAreInUnitRange(t *testing.T) {
	p := openPool(t, 1)
	cases := [][3]string{
		{"What is 2+2?", "4", "4"},
		{"Explain gravity", "Gravity is the attraction between masses.", "Bananas are yellow."},
		{"Price of BTC?", "BTC is $61,204.11", "BTC is $61,204.11 according to the exchange feed."},
	}
	for _, c := range cases {
		got, err := p.Score(c[0], c[1], c[2])
		if err != nil {
			t.Fatalf("score: %v", err)
		}
		if got < 0 || got > 1 || math.IsNaN(float64(got)) {
			t.Errorf("score %.6f outside [0,1] for %q", got, c[2])
		}
	}
}

// Determinism is a hard requirement: validators take a stake-weighted median and
// penalise deviation beyond delta_c. The same input must give bit-identical
// output, both repeatedly on one instance and across separate instances.
func TestScoringIsDeterministic(t *testing.T) {
	p := openPool(t, 2)
	q, gt, ma := "What is the capital of France?", "Paris is the capital of France.", "The capital of France is Paris."

	first, err := p.Score(q, gt, ma)
	if err != nil {
		t.Fatalf("score: %v", err)
	}
	for i := 0; i < 12; i++ {
		got, err := p.Score(q, gt, ma)
		if err != nil {
			t.Fatalf("score: %v", err)
		}
		if math.Float32bits(got) != math.Float32bits(first) {
			t.Fatalf("run %d returned %.9f, first run %.9f — not bit-identical", i, got, first)
		}
	}
}

// Allocations must be returned to the module. Replaying the full corpus pushes
// tens of thousands of strings through one instance, so a leak here shows up as
// a trap partway through a long run rather than as an obvious failure.
func TestRepeatedScoringDoesNotExhaustMemory(t *testing.T) {
	p := openPool(t, 1)
	big := make([]byte, 64*1024)
	for i := range big {
		big[i] = byte('a' + i%26)
	}
	answer := string(big)

	for i := 0; i < 200; i++ {
		if _, err := p.Score("q", "ground truth text", answer); err != nil {
			t.Fatalf("iteration %d with a 64 KB answer failed: %v", i, err)
		}
	}
}

// breakdown_answer's composite must equal rank_answer's return for the same
// input; they are documented as sharing one composite() and must not drift.
func TestBreakdownCompositeMatchesRankAnswer(t *testing.T) {
	p := openPool(t, 1)
	if !p.HasBreakdown() {
		t.Skip("module does not export breakdown_answer")
	}
	q := "What is the current price of Solana?"
	gt := "Solana (SOL) is trading at $94.06 USD."
	ma := "SOL is currently around $94 per token."

	rank, err := p.Score(q, gt, ma)
	if err != nil {
		t.Fatalf("score: %v", err)
	}
	bd, err := p.Breakdown(q, gt, ma)
	if err != nil {
		t.Fatalf("breakdown: %v", err)
	}
	t.Logf("relevance=%.4f correctness=%.4f lexical=%.4f length=%.4f composite=%.4f rank=%.4f",
		bd.Relevance, bd.Correctness, bd.Lexical, bd.Length, bd.Composite, rank)

	if math.Float32bits(bd.Composite) != math.Float32bits(rank) {
		t.Errorf("breakdown composite %.9f != rank_answer %.9f", bd.Composite, rank)
	}
}

// ScoreAll must preserve input order regardless of which worker finishes first.
func TestScoreAllPreservesOrder(t *testing.T) {
	p := openPool(t, 4)
	var jobs []Job
	var want []float32
	for i := 0; i < 16; i++ {
		j := Job{Index: i, Question: "What is the capital of France?", GT: "Paris.", MA: "Paris."}
		if i%2 == 1 {
			j.MA = "" // must score exactly 0
		}
		jobs = append(jobs, j)
	}
	res := p.ScoreAll(jobs, nil)
	if len(res) != len(jobs) {
		t.Fatalf("got %d results for %d jobs", len(res), len(jobs))
	}
	_ = want
	for i, r := range res {
		if r.Err != nil {
			t.Fatalf("job %d: %v", i, r.Err)
		}
		if r.Index != i {
			t.Errorf("result %d carries index %d", i, r.Index)
		}
		if i%2 == 1 && r.Score != 0 {
			t.Errorf("job %d had an empty answer but scored %.6f", i, r.Score)
		}
		if i%2 == 0 && r.Score == 0 {
			t.Errorf("job %d had a matching answer but scored 0", i)
		}
	}
}
