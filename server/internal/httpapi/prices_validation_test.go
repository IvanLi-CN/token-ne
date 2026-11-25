package httpapi

import (
	"testing"

	"github.com/IvanLi-CN/token-ne/server/internal/store"
)

func TestValidateBillingCoversEveryDimensionAndTier(t *testing.T) {
	negative := -1.0
	tooLarge := 1e8
	zero, one := 0.0, 1.0
	cases := []struct {
		name    string
		billing store.Billing
		path    string
	}{
		{"cache write", store.Billing{CacheWrite: &negative}, "cache_write_usd_per_mtok"},
		{"audio in", store.Billing{AudioIn: &negative}, "audio_input_usd_per_mtok"},
		{"audio out", store.Billing{AudioOut: &tooLarge}, "audio_output_usd_per_mtok"},
		{"reasoning", store.Billing{Reasoning: &negative}, "reasoning_usd_per_mtok"},
		{"tier threshold", store.Billing{Tiers: []store.PriceTier{{ThresholdTokens: 0}}}, "tiers[0].threshold_tokens"},
		{
			"descending tiers",
			store.Billing{Tiers: []store.PriceTier{
				{ThresholdTokens: 200000, Input: &one},
				{ThresholdTokens: 32000, Input: &one},
			}},
			"tiers[1].threshold_tokens",
		},
		{
			"tier price",
			store.Billing{Tiers: []store.PriceTier{{ThresholdTokens: 1000, Input: &zero, Output: &negative}}},
			"tiers[0].output_usd_per_mtok",
		},
	}
	for _, tc := range cases {
		details := validateBilling(tc.billing)
		if len(details) != 1 || details[0].Path != tc.path {
			t.Fatalf("%s: %+v, want path %s", tc.name, details, tc.path)
		}
	}
}

func TestValidateModelPrefixesBillingPaths(t *testing.T) {
	negative := -1.0
	details := ValidateModel(store.Model{ID: "gpt-5", Billing: store.Billing{Reasoning: &negative}})
	if len(details) != 1 || details[0].Path != "billing.reasoning_usd_per_mtok" {
		t.Fatalf("model validation = %+v", details)
	}
}
