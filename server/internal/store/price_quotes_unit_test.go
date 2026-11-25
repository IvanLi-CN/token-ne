package store

import (
	"strings"
	"testing"

	"github.com/IvanLi-CN/token-ne/server/internal/pricesource"
)

func TestQuotedBillingKeepsEveryDimensionAndTier(t *testing.T) {
	in := &pricesource.Billing{
		Input:      fp(1),
		Cached:     fp(0.5),
		CacheWrite: fp(1.25),
		AudioIn:    fp(2),
		AudioOut:   fp(3),
		Reasoning:  fp(4),
		Output:     fp(5),
		Tiers: []pricesource.Tier{{
			ThresholdTokens: 32000,
			Input:           fp(6),
			Cached:          fp(6.5),
			CacheWrite:      fp(7),
			AudioIn:         fp(8),
			AudioOut:        fp(9),
			Reasoning:       fp(10),
			Output:          fp(11),
		}},
	}
	out := quotedBilling(in)
	if out == nil {
		t.Fatal("quotedBilling dropped the billing")
	}
	if out.CacheWrite == nil || out.AudioIn == nil || out.AudioOut == nil || out.Reasoning == nil {
		t.Fatalf("extra dimensions lost: %+v", out)
	}
	if len(out.Tiers) != 1 || out.Tiers[0].ThresholdTokens != 32000 ||
		out.Tiers[0].CacheWrite == nil || out.Tiers[0].Reasoning == nil {
		t.Fatalf("tier lost: %+v", out.Tiers)
	}
}

func TestQuoteBillingValidationCoversDimensionsAndTiers(t *testing.T) {
	if got := validateQuoteBilling(Billing{CacheWrite: fp(-1)}); got != "cache_write_usd_per_mtok" {
		t.Fatalf("negative cache write = %q", got)
	}
	if got := validateQuoteBilling(Billing{Reasoning: fp(1e8)}); got != "reasoning_usd_per_mtok" {
		t.Fatalf("oversized reasoning = %q", got)
	}
	descending := Billing{Tiers: []PriceTier{
		{ThresholdTokens: 200000, Input: fp(1)},
		{ThresholdTokens: 32000, Input: fp(2)},
	}}
	if got := validateQuoteBilling(descending); !strings.Contains(got, "tiers[1].threshold_tokens") {
		t.Fatalf("descending tiers = %q", got)
	}
	if got := validateQuoteBilling(Billing{Tiers: []PriceTier{{ThresholdTokens: 0}}}); got != "tiers[0].threshold_tokens" {
		t.Fatalf("zero threshold = %q", got)
	}
}


func TestMergePreservingBillingKeepsKnownDimensions(t *testing.T) {
	current := Billing{CacheWrite: fp(1.25), AudioIn: fp(3), Tiers: []PriceTier{{ThresholdTokens: 128000, Input: fp(2)}}}
	proposed := Billing{Input: fp(0.5), Output: fp(1)}
	got := mergePreservingBilling(current, proposed)
	if got.CacheWrite == nil || *got.CacheWrite != 1.25 || got.AudioIn == nil || *got.AudioIn != 3 {
		t.Fatalf("known dimensions dropped: %+v", got)
	}
	if len(got.Tiers) != 1 || got.Tiers[0].ThresholdTokens != 128000 {
		t.Fatalf("tier ladder dropped: %+v", got)
	}
	if got.Input == nil || *got.Input != 0.5 || got.Output == nil || *got.Output != 1 {
		t.Fatalf("source values lost: %+v", got)
	}
}
func fp(v float64) *float64 { return &v }
