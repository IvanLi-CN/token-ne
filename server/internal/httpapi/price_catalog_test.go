package httpapi

import (
	"testing"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/store"
)

// Missing dates must sort last in both directions; the page default is desc.
func TestCompareSourceModelKeepsMissingDatesLast(t *testing.T) {
	early := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	late := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	dated := store.SourceModel{SourceModelID: "a", ReleasedAt: &early}
	newer := store.SourceModel{SourceModelID: "b", ReleasedAt: &late}
	undated := store.SourceModel{SourceModelID: "c"}

	for _, tc := range []struct {
		name string
		a, b store.SourceModel
	}{
		{"undated first", undated, dated},
		{"undated second", dated, undated},
	} {
		if order, missing := compareSourceModel(tc.a, tc.b, "released"); missing == 0 || order != 0 {
			t.Fatalf("%s: order=%d missing=%d", tc.name, order, missing)
		}
	}
	if _, missing := compareSourceModel(undated, undated, "released"); missing != 0 {
		t.Fatalf("two undated rows reported a bias: %d", missing)
	}
	if order, missing := compareSourceModel(dated, newer, "released"); missing != 0 || order != -1 {
		t.Fatalf("dated ordering = %d/%d", order, missing)
	}
}
