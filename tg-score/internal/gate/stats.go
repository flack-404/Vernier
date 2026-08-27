// Package gate implements Telegraph's three WASM activation gates plus the
// Catch-Rate promotion table, as reconstructed from the verbatim rejection
// strings on the public script registry.
//
// The three gate names and thresholds are not documented by Telegraph. They were
// recovered from the RejectionReason field of GET /engine/v1/intents/{id}/wasm,
// which returned five records, all rejected. See docs/ACTIVATION-GATES.md for the
// raw strings each gate here is modelled on.
package gate

import (
	"math"
	"sort"
)

// Spearman returns the Spearman rank-correlation coefficient between x and y.
//
// Computed as the Pearson correlation of the fractional ranks, which is the
// definition that stays correct in the presence of ties. The shortcut form
// (1 - 6*sum(d^2)/(n^3-n)) is only valid for tie-free data, and this corpus is
// full of ties: 44% of scored rounds return a published score of exactly 0, so
// a ten-miner intent group routinely contains six identical values. Using the
// shortcut here would silently report the wrong agreement on the majority of
// groups.
//
// ok is false when the coefficient is undefined — fewer than two points, or one
// of the inputs being constant (zero variance gives a zero denominator). A caller
// must not treat !ok as a failing score; it means "this group carries no ranking
// information", which is a different thing entirely.
func Spearman(x, y []float64) (rho float64, ok bool) {
	if len(x) != len(y) || len(x) < 2 {
		return 0, false
	}
	rx := fractionalRanks(x)
	ry := fractionalRanks(y)
	return pearson(rx, ry)
}

// fractionalRanks assigns each element its 1-based rank, with tied elements
// receiving the arithmetic mean of the ranks they jointly occupy. Three values
// tied across ranks 4,5,6 each receive 5.0.
func fractionalRanks(v []float64) []float64 {
	n := len(v)
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return v[idx[a]] < v[idx[b]] })

	ranks := make([]float64, n)
	for i := 0; i < n; {
		j := i
		// Extend j over the whole run of values equal to v[idx[i]].
		for j+1 < n && v[idx[j+1]] == v[idx[i]] {
			j++
		}
		// Ranks are 1-based, so the run spans ranks i+1 .. j+1.
		avg := float64(i+1+j+1) / 2.0
		for k := i; k <= j; k++ {
			ranks[idx[k]] = avg
		}
		i = j + 1
	}
	return ranks
}

// pearson returns the Pearson product-moment correlation of a and b.
// ok is false when either input has zero variance.
func pearson(a, b []float64) (float64, bool) {
	n := float64(len(a))
	var ma, mb float64
	for i := range a {
		ma += a[i]
		mb += b[i]
	}
	ma /= n
	mb /= n

	var num, da, db float64
	for i := range a {
		xa := a[i] - ma
		xb := b[i] - mb
		num += xa * xb
		da += xa * xa
		db += xb * xb
	}
	if da == 0 || db == 0 {
		return 0, false
	}
	return num / math.Sqrt(da*db), true
}

// StdDev returns the population standard deviation of v.
//
// Population (dividing by n), not sample (n-1). Gate 2's rejection string reports
// "stdev=0.0000" against a fixed 0.0500 threshold with no mention of degrees of
// freedom; population is the conventional choice for a dispersion check over a
// complete evaluation set rather than a sample of one. On the group sizes here
// (n>=10) the two differ by under 6%, well inside the margin that matters, but
// the choice is recorded so a future reader knows it was a choice.
func StdDev(v []float64) float64 {
	if len(v) < 2 {
		return 0
	}
	var mean float64
	for _, x := range v {
		mean += x
	}
	mean /= float64(len(v))

	var ss float64
	for _, x := range v {
		d := x - mean
		ss += d * d
	}
	return math.Sqrt(ss / float64(len(v)))
}

// Mean returns the arithmetic mean of v, or 0 for an empty slice.
func Mean(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	var s float64
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}
