package cost

import "testing"

// A hand-written profile may store tiers out of order; the highest threshold at
// or below the prompt must still win instead of the last array element.
func TestCalculateUsesHighestTierRegardlessOfOrder(t *testing.T) {
	input, output := int64(600000), int64(0)
	base, tier32, tier200 := "1", "2", "3"
	tiers := []Tier{
		{ThresholdTokens: 200000, Input: &tier200},
		{ThresholdTokens: 32000, Input: &tier32},
	}
	cost, ok := Calculate(Prices{Input: &base, Output: &base}, tiers, Usage{Input: &input, Output: &output})
	if !ok || cost == nil {
		t.Fatalf("priced usage: %v %v", cost, ok)
	}
	// 600k prompt tokens at the 200k rate of $3/Mtok = $1.80.
	if *cost != "1.800000" {
		t.Fatalf("descending tiers billed %s, want 1.800000", *cost)
	}
}
