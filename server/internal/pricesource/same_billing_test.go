package pricesource

import "testing"

func TestSameBillingComparesEveryDimensionAndTier(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	base := Billing{Input: f(1), Cached: f(0.5), Output: f(2)}
	sameBase := base

	differing := []struct {
		name string
		b    Billing
	}{
		{"cache write", Billing{Input: f(1), Cached: f(0.5), Output: f(2), CacheWrite: f(0.25)}},
		{"audio in", Billing{Input: f(1), Cached: f(0.5), Output: f(2), AudioIn: f(1)}},
		{"audio out", Billing{Input: f(1), Cached: f(0.5), Output: f(2), AudioOut: f(1)}},
		{"reasoning", Billing{Input: f(1), Cached: f(0.5), Output: f(2), Reasoning: f(1)}},
		{"tiers", Billing{Input: f(1), Cached: f(0.5), Output: f(2), Tiers: []Tier{{ThresholdTokens: 32000, Input: f(3)}}}},
	}
	for _, tc := range differing {
		if sameBilling(&base, tc.b) {
			t.Fatalf("%s: differing billings compared equal", tc.name)
		}
	}
	if !sameBilling(&base, sameBase) {
		t.Fatal("identical billings compared unequal")
	}
}
