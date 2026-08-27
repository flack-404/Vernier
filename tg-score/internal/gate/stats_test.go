package gate

import (
	"math"
	"testing"
)

func approx(t *testing.T, got, want, tol float64, label string) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s: got %.6f, want %.6f (tol %g)", label, got, want, tol)
	}
}

func TestSpearmanPerfectAgreement(t *testing.T) {
	x := []float64{1, 2, 3, 4, 5}
	y := []float64{10, 20, 30, 40, 50}
	rho, ok := Spearman(x, y)
	if !ok {
		t.Fatal("expected defined rho")
	}
	approx(t, rho, 1.0, 1e-9, "monotone increasing")
}

func TestSpearmanPerfectInversion(t *testing.T) {
	x := []float64{1, 2, 3, 4, 5}
	y := []float64{50, 40, 30, 20, 10}
	rho, _ := Spearman(x, y)
	approx(t, rho, -1.0, 1e-9, "monotone decreasing")
}

// Spearman is rank-based, so any monotone transform of an input must leave it
// unchanged. This is the property the gate actually relies on: absolute score
// values differ wildly between the local baseline and the deployed scorer
// (mean |err| 0.383) while the ranking is preserved.
func TestSpearmanInvariantUnderMonotoneTransform(t *testing.T) {
	x := []float64{0.11, 0.42, 0.37, 0.98, 0.05, 0.63}
	y := []float64{3, 1, 4, 1, 5, 9}

	base, _ := Spearman(x, y)

	squashed := make([]float64, len(x))
	for i, v := range x {
		squashed[i] = 0.63 + 0.12*v // the 0.63-0.75 compression band
	}
	got, _ := Spearman(squashed, y)
	approx(t, got, base, 1e-12, "compressed into 0.63-0.75 band")
}

// The tie case is the one the textbook shortcut formula gets wrong, and it is
// the common case here: 44% of published rounds score exactly 0.
func TestSpearmanHandlesTies(t *testing.T) {
	// Six of ten reference values tied at zero, mirroring a real intent group.
	x := []float64{0, 0, 0, 0, 0, 0, 0.2, 0.5, 0.7, 0.9}
	y := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}

	rho, ok := Spearman(x, y)
	if !ok {
		t.Fatal("expected defined rho")
	}
	// The six tied elements all take rank 3.5, so they contribute no ordering
	// information and the coefficient must fall short of 1.0. Hand-computed:
	// ranks are [3.5 x6, 7, 8, 9, 10] against [1..10], giving
	// rho = 65 / sqrt(65 * 82.5) = 0.887625. A shortcut formula that ignores
	// ties reports 1.0 for this input, which is why that formula is not used.
	if rho >= 1.0 {
		t.Errorf("ties must prevent perfect correlation, got %.6f", rho)
	}
	approx(t, rho, 0.887625, 1e-6, "six-way tie against a perfect ordering")
}

func TestFractionalRanksAveragesTiedRuns(t *testing.T) {
	// Sorted ascending this is 1,1,1,5,9: the three-way tie occupies ranks
	// 1,2,3 and each element takes their mean, 2.0. Then 5 -> 4 and 9 -> 5.
	got := fractionalRanks([]float64{5, 1, 1, 1, 9})
	want := []float64{4, 2, 2, 2, 5}
	for i := range want {
		approx(t, got[i], want[i], 1e-12, "tied run average")
	}
}

// A constant input has no ranking to correlate against. Reporting 0.0 here would
// be indistinguishable from genuine disagreement and would fail a candidate for
// a group that carries no information.
func TestSpearmanUndefinedOnConstantInput(t *testing.T) {
	if _, ok := Spearman([]float64{1, 1, 1, 1}, []float64{4, 3, 2, 1}); ok {
		t.Error("constant input must report undefined, not a score")
	}
	if _, ok := Spearman([]float64{1, 2}, []float64{1, 2}); !ok {
		t.Error("n=2 with variance is defined")
	}
	if _, ok := Spearman([]float64{1}, []float64{1}); ok {
		t.Error("n=1 must report undefined")
	}
}

func TestStdDevKnownValue(t *testing.T) {
	// Population stdev of {2,4,4,4,5,5,7,9} is exactly 2.
	approx(t, StdDev([]float64{2, 4, 4, 4, 5, 5, 7, 9}), 2.0, 1e-12, "population stdev")
}

// The exact condition gate 2 rejects on: a scorer returning the same value for
// every input.
func TestStdDevOfConstantScorerIsZero(t *testing.T) {
	approx(t, StdDev([]float64{0.5, 0.5, 0.5, 0.5}), 0.0, 1e-12, "collapsed scores")
}
