package store

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

// A profile that carries cache-write, audio, reasoning or tier prices must not
// be mistaken for a price change on metadata-only saves. Before the revision
// was read, every such save appended a revision and a locked profile rejected
// the acceptance toggle outright.
func TestDimensionedProfileMetadataSaveIntegration(t *testing.T) {
	ctx, s := priceTestStore(t)
	suffix := uuid.NewString()[:8]
	modelID := "p6-dim-" + suffix
	t.Cleanup(func() {
		for _, sql := range []string{
			"delete from model_prices where model_id=$1",
			"delete from models where model_id=$1",
			"delete from price_revisions where model_id=$1",
		} {
			if _, err := s.Pool.Exec(ctx, sql, modelID); err != nil {
				t.Logf("cleanup %q: %v", sql, err)
			}
		}
	})

	billing := Billing{
		Input:      price(1),
		Cached:     price(0.5),
		CacheWrite: price(1.25),
		AudioIn:    price(2),
		AudioOut:   price(3),
		Reasoning:  price(4),
		Output:     price(5),
		Tiers: []PriceTier{{
			ThresholdTokens: 32000,
			Input:           price(6),
			CacheWrite:      price(7),
			Reasoning:       price(8),
			Output:          price(9),
		}},
	}
	created := testModel(t, ctx, s, modelID, billing)
	if created.BillingState == nil {
		t.Fatal("created model has no billing state")
	}
	first := created.BillingState.PriceRevisionID

	// Toggling acceptance carries the loaded profile verbatim.
	updated, err := s.SaveModel(ctx, Model{ID: modelID, Enabled: false, Billing: created.Billing}, false, priceAudit())
	if err != nil {
		t.Fatalf("metadata save: %v", err)
	}
	if updated.BillingState == nil || updated.BillingState.PriceRevisionID != first {
		t.Fatalf("metadata save appended a revision: %+v", updated.BillingState)
	}
	if updated.Enabled {
		t.Fatal("acceptance toggle was not persisted")
	}

	// Lock the profile, then flip acceptance again: no confirmation needed
	// because nothing price-related changed.
	locked, err := s.SaveModel(ctx, Model{ID: modelID, Enabled: false, Billing: created.Billing, BillingLocked: flag(true)}, false, priceAudit())
	if err != nil {
		t.Fatalf("lock save: %v", err)
	}
	if locked.BillingState == nil || !locked.BillingState.Locked {
		t.Fatalf("profile not locked: %+v", locked.BillingState)
	}
	if _, err = s.SaveModel(ctx, Model{ID: modelID, Enabled: true, Billing: created.Billing}, false, priceAudit()); err != nil {
		t.Fatalf("acceptance toggle on a locked profile: %v", err)
	}

	// A real price change still requires confirmation.
	changed := created.Billing
	changed.Reasoning = price(6)
	_, err = s.SaveModel(ctx, Model{ID: modelID, Enabled: true, Billing: changed}, false, priceAudit())
	if !errors.Is(err, ErrPriceProfileLocked) {
		t.Fatalf("unconfirmed price change = %v, want ErrPriceProfileLocked", err)
	}
}
