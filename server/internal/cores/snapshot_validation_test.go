package cores

import (
	"errors"
	"strings"
	"testing"
)

func snapshotBody(routes string, features string) []byte {
	return []byte(`{
		"features": ` + features + `,
		"models": [{"model_id":"gpt-5"},{"model_id":"gpt-4o"}],
		"upstreams": [{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000c1"}],
		"model_routes": ` + routes + `,
		"upstream_policies": []
	}`)
}

const phaseTwoFeatures = `{"model_name_routing":true,"upstream_model_policy":true}`

func TestValidateSnapshotMirrorsSamePriorityGlobOverlap(t *testing.T) {
	overlap := `[
		{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000a1","pattern":"gpt-*","match_type":"glob","target_model_id":"gpt-5","priority":10,"enabled":true},
		{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000a2","pattern":"gpt-4*","match_type":"glob","target_model_id":"gpt-4o","priority":10,"enabled":true}
	]`
	err := ValidateSnapshot(snapshotBody(overlap, phaseTwoFeatures))
	var rejection *SnapshotValidationError
	if !errors.As(err, &rejection) || rejection.Details[0].Path != "model_routes[1].pattern" {
		t.Fatalf("overlap rejection = %v", err)
	}

	// A disabled draft never routes, so it must not block the snapshot.
	disabled := strings.Replace(overlap,
		`"target_model_id":"gpt-4o","priority":10,"enabled":true`,
		`"target_model_id":"gpt-4o","priority":10,"enabled":false`, 1)
	if err := ValidateSnapshot(snapshotBody(disabled, phaseTwoFeatures)); err != nil {
		t.Fatalf("disabled draft rejected: %v", err)
	}
}

func TestValidateSnapshotReportsFirstUnknownFieldDeterministically(t *testing.T) {
	routes := `[{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000a1","pattern":"gpt-5","match_type":"exact","target_model_id":"gpt-5","priority":10,"enabled":true,"zzz":1,"aaa":2}]`
	for range 20 {
		err := ValidateSnapshot(snapshotBody(routes, phaseTwoFeatures))
		var rejection *SnapshotValidationError
		if !errors.As(err, &rejection) || rejection.Details[0].Path != "model_routes[0].aaa" {
			t.Fatalf("unknown field path = %v", err)
		}
	}
}
