// Package wr holds the walk-report helpers the report card vendors: Wilson interval,
// Fisher's exact test, pct formatting and the median/percentile helpers. Stdlib only.
package wr

import (
	"fmt"
	"math"
	"math/big"
	"sort"
)

// Wilson is the Wilson score interval for k/n at ~95% (z=1.96). n == 0 is undefined and
// returns (0.0, 1.0) (total uncertainty), never a crash.
func Wilson(k, n int64, z float64) (float64, float64) {
	if n <= 0 {
		return 0.0, 1.0
	}
	p := float64(k) / float64(n)
	denom := 1.0 + z*z/float64(n)
	center := (p + z*z/(2.0*float64(n))) / denom
	half := z * math.Sqrt(p*(1.0-p)/float64(n)+z*z/(4.0*float64(n)*float64(n))) / denom
	return math.Max(0.0, center-half), math.Min(1.0, center+half)
}

func binom(n, k int64) *big.Int {
	out := new(big.Int).Binomial(n, k)
	return out
}

// hypergeomProb is P(X == x) for a 2x2 table with row sums n1, n2 and col-1 sum k1, as
// Python's comb(n1, x) * comb(n2, k1 - x) / comb(n1 + n2, k1) computes it.
func hypergeomProb(x, n1, n2, k1 int64) float64 {
	num := new(big.Int).Mul(binom(n1, x), binom(n2, k1-x))
	den := binom(n1+n2, k1)
	rat := new(big.Rat).SetFrac(num, den)
	f, _ := rat.Float64()
	return f
}

// FisherTwoSided is the two-sided Fisher exact p for [[a, b], [c, d]]: the hypergeometric sum
// over all tables with the same margins whose probability is at most the observed table's
// (relative tolerance relTol).
func FisherTwoSided(a, b, c, d int64, relTol float64) float64 {
	n1 := a + b
	n2 := c + d
	k1 := a + c
	n := n1 + n2
	if n == 0 {
		return 1.0
	}
	lo := int64(0)
	if k1-n2 > 0 {
		lo = k1 - n2
	}
	hi := n1
	if k1 < hi {
		hi = k1
	}
	obs := hypergeomProb(a, n1, n2, k1)
	total := 0.0
	for x := lo; x <= hi; x++ {
		if prob := hypergeomProb(x, n1, n2, k1); prob <= obs*(1.0+relTol) {
			total += prob
		}
	}
	return math.Min(1.0, math.Max(0.0, total))
}

// Pct is 'k/n (x.y%)' with a 0/0-safe 0.0%.
func Pct(k, n int64) string {
	pct := 0.0
	if n != 0 {
		pct = 100.0 * float64(k) / float64(n)
	}
	return fmt.Sprintf("%d/%d (%.1f%%)", k, n, pct)
}

// Median is the median of vals ignoring None; ok is false when empty.
func Median(vals []*float64) (float64, bool) {
	var v []float64
	for _, item := range vals {
		if item != nil {
			v = append(v, *item)
		}
	}
	if len(v) == 0 {
		return 0, false
	}
	sort.Float64s(v)
	mid := len(v) / 2
	if len(v)%2 == 1 {
		return v[mid], true
	}
	return (v[mid-1] + v[mid]) / 2.0, true
}

// P90 is the 90th percentile (nearest rank) ignoring None; ok is false when empty.
func P90(vals []*float64) (float64, bool) {
	var v []float64
	for _, item := range vals {
		if item != nil {
			v = append(v, *item)
		}
	}
	if len(v) == 0 {
		return 0, false
	}
	sort.Float64s(v)
	idx := int(math.Ceil(0.9*float64(len(v)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx > len(v)-1 {
		idx = len(v) - 1
	}
	return v[idx], true
}

// NoteFor is a baseline comparison's note for a p value.
func NoteFor(p float64) string {
	if p < 0.05 {
		return "differs (p<0.05)"
	}
	if p < 0.20 {
		return "suggestive (p<0.20)"
	}
	return "no evidence"
}
