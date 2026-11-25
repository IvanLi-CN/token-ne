package store

import (
	"testing"

	"github.com/google/uuid"
)

// TestUsageCostUsesDimensionsAndTiersIntegration proves the ingestion cost uses
// every billed dimension and switches to the context tier's rates.
func TestUsageCostUsesDimensionsAndTiersIntegration(t *testing.T) {
	ctx, s := priceTestStore(t)
	suffix := uuid.NewString()[:8]
	modelID := "dims-" + suffix
	instance, version := registerUsageSource(t, ctx, s, suffix)
	cleanPriceState(t, ctx, s, []string{modelID}, nil, instance, version)

	tiered := Billing{
		Input: price(1), Cached: price(0.5), CacheWrite: price(1.5),
		AudioIn: price(2), AudioOut: price(3), Output: price(4),
		Tiers: []PriceTier{{ThresholdTokens: 1000, Input: price(10), Output: price(40)}},
	}
	testModel(t, ctx, s, modelID, tiered)

	// Below the tier: 1000 input (200 cached, 100 cache writes, 50 audio) and
	// 200 output (50 audio).
	event := testEvent(instance, version, modelID, nil, 900, 200, 200)
	event.CacheWriteTokens = int64p(100)
	event.AudioInputTokens = int64p(50)
	event.AudioOutputTokens = int64p(50)
	ingestOne(t, ctx, s, instance, event)
	got, revision, _, _ := storedEvent(t, ctx, s, event.EventID)
	if got == nil || *got != "0.001650" {
		t.Fatalf("dimension cost = %v", got)
	}
	if revision == nil {
		t.Fatal("dimension cost has no price revision")
	}

	// Above the tier: the tier replaces input and output rates while cache and
	// audio dimensions keep the base rates.
	big := testEvent(instance, version, modelID, nil, 2000, 0, 1000)
	ingestOne(t, ctx, s, instance, big)
	got, _, _, _ = storedEvent(t, ctx, s, big.EventID)
	if got == nil || *got != "0.060000" {
		t.Fatalf("tier cost = %v", got)
	}
}
