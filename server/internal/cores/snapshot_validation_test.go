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

// Overlapping and duplicated patterns are legal: order picks the winner, so the
// publisher must accept them instead of inventing a uniqueness rule.
func TestValidateSnapshotAcceptsDuplicateAndOverlappingRoutes(t *testing.T) {
	overlap := `[
		{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000a1","pattern":"gpt-*","match_type":"glob","target_model_id":"gpt-5","position":0,"enabled":true},
		{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000a2","pattern":"gpt-4*","match_type":"glob","target_model_id":"gpt-4o","position":1,"enabled":true},
		{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000a3","pattern":"chat","match_type":"exact","target_model_id":"gpt-5","position":2,"enabled":true},
		{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000a4","pattern":"chat","match_type":"exact","target_model_id":"gpt-4o","position":3,"enabled":true}
	]`
	if err := ValidateSnapshot(snapshotBody(overlap, phaseTwoFeatures)); err != nil {
		t.Fatalf("ordered duplicate routes rejected: %v", err)
	}

	// A pattern equal to a system model ID stopped being reserved.
	reserved := `[{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000a1","pattern":"gpt-5","match_type":"exact","target_model_id":"gpt-5","position":0,"enabled":true}]`
	if err := ValidateSnapshot(snapshotBody(reserved, phaseTwoFeatures)); err != nil {
		t.Fatalf("pattern equal to a model ID rejected: %v", err)
	}
}

// Positions are unique per scope only; two scopes never compete for a slot.
func TestValidateSnapshotScopesPositions(t *testing.T) {
	perScope := `[
		{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000a1","pattern":"a","match_type":"exact","target_model_id":"gpt-5","position":0,"enabled":true},
		{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000a2","pattern":"b","match_type":"exact","target_model_id":"gpt-5","upstream_id":"0193f0c2-7b1a-7c3e-9f00-0000000000c1","position":0,"enabled":true}
	]`
	if err := ValidateSnapshot(snapshotBody(perScope, phaseTwoFeatures)); err != nil {
		t.Fatalf("same position in two scopes rejected: %v", err)
	}

	duplicate := `[
		{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000a1","pattern":"a","match_type":"exact","target_model_id":"gpt-5","position":2,"enabled":true},
		{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000a2","pattern":"b","match_type":"exact","target_model_id":"gpt-5","position":2,"enabled":true}
	]`
	err := ValidateSnapshot(snapshotBody(duplicate, phaseTwoFeatures))
	var rejection *SnapshotValidationError
	if !errors.As(err, &rejection) || rejection.Details[0].Path != "model_routes[1].position" {
		t.Fatalf("duplicate position rejection = %v", err)
	}

	missing := `[{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000a1","pattern":"a","match_type":"exact","target_model_id":"gpt-5","enabled":true}]`
	if err = ValidateSnapshot(snapshotBody(missing, phaseTwoFeatures)); !errors.As(err, &rejection) || rejection.Details[0].Path != "model_routes[0].position" {
		t.Fatalf("missing position rejection = %v", err)
	}

	negative := `[{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000a1","pattern":"a","match_type":"exact","target_model_id":"gpt-5","position":-1,"enabled":true}]`
	if err = ValidateSnapshot(snapshotBody(negative, phaseTwoFeatures)); !errors.As(err, &rejection) || rejection.Details[0].Path != "model_routes[0].position" {
		t.Fatalf("negative position rejection = %v", err)
	}
}

func TestValidateSnapshotReportsFirstUnknownFieldDeterministically(t *testing.T) {
	routes := `[{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000a1","pattern":"gpt-5","match_type":"exact","target_model_id":"gpt-5","position":0,"enabled":true,"zzz":1,"aaa":2}]`
	for range 20 {
		err := ValidateSnapshot(snapshotBody(routes, phaseTwoFeatures))
		var rejection *SnapshotValidationError
		if !errors.As(err, &rejection) || rejection.Details[0].Path != "model_routes[0].aaa" {
			t.Fatalf("unknown field path = %v", err)
		}
	}
	// The retired priority field is unknown now, so a stale publisher is caught.
	stale := `[{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000a1","pattern":"gpt-5","match_type":"exact","target_model_id":"gpt-5","priority":10,"enabled":true}]`
	err := ValidateSnapshot(snapshotBody(stale, phaseTwoFeatures))
	var rejection *SnapshotValidationError
	if !errors.As(err, &rejection) || rejection.Details[0].Path != "model_routes[0].priority" {
		t.Fatalf("priority field path = %v", err)
	}
}

// The core rejects an exact pattern that is empty, over 256 scalars or carries
// a control character, and a description over 1024 scalars. The publisher must
// reject the same snapshot instead of learning at push time.
func TestValidateSnapshotMirrorsExactPatternAndDescriptionLimits(t *testing.T) {
	long := strings.Repeat("x", 257)
	routes := `[{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000a1","pattern":"` + long + `","match_type":"exact","target_model_id":"gpt-5","position":0,"enabled":true}]`
	err := ValidateSnapshot(snapshotBody(routes, phaseTwoFeatures))
	var rejection *SnapshotValidationError
	if !errors.As(err, &rejection) || rejection.Details[0].Path != "model_routes[0].pattern" {
		t.Fatalf("long exact pattern = %v", err)
	}

	control := `[{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000a1","pattern":"gpt\u0007","match_type":"exact","target_model_id":"gpt-5","position":0,"enabled":true}]`
	if err = ValidateSnapshot(snapshotBody(control, phaseTwoFeatures)); !errors.As(err, &rejection) || rejection.Details[0].Path != "model_routes[0].pattern" {
		t.Fatalf("control character pattern = %v", err)
	}

	description := strings.Repeat("d", 1025)
	described := `[{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000a1","pattern":"alias","match_type":"exact","target_model_id":"gpt-5","position":0,"enabled":true,"description":"` + description + `"}]`
	if err = ValidateSnapshot(snapshotBody(described, phaseTwoFeatures)); !errors.As(err, &rejection) || rejection.Details[0].Path != "model_routes[0].description" {
		t.Fatalf("long description = %v", err)
	}

	valid := `[{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000a1","pattern":"alias","match_type":"exact","target_model_id":"gpt-5","position":0,"enabled":true,"description":"ok"}]`
	if err = ValidateSnapshot(snapshotBody(valid, phaseTwoFeatures)); err != nil {
		t.Fatalf("valid exact route rejected: %v", err)
	}
}

// A schema 1 body is rendered for a core that predates position and
// upstream_model_id. The publisher must validate it against that reader's
// contract, not against the newest one, or a correct body is rejected before it
// is ever sent.
func TestValidateSnapshotAcceptsTheOlderShape(t *testing.T) {
	v1 := []byte(`{
		"config_schema_version": 1,
		"features": {"chat_completions":true,"responses":true,"embeddings":true,"stream_usage_injection":true,"status_rewrite":true},
		"models": [{"model_id":"gpt-5","default_upstream_model":null}],
		"upstreams": [{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000c1","name":"u"}],
		"upstream_models": [{"upstream_id":"0193f0c2-7b1a-7c3e-9f00-0000000000c1","model_id":"gpt-5","upstream_model_name":"gpt-5","priority":100,"weight":100}]
	}`)
	if err := ValidateSnapshot(v1); err != nil {
		t.Fatalf("schema 1 body rejected: %v", err)
	}

	// The two fields that must never cross the boundary.
	withRoutes := []byte(`{
		"config_schema_version": 1,
		"features": {},
		"models": [], "upstreams": [],
		"model_routes": [{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000a1","pattern":"x","match_type":"exact","target_model_id":"gpt-5","priority":1,"enabled":true}]
	}`)
	if err := ValidateSnapshot(withRoutes); err == nil {
		t.Fatal("schema 1 body carrying routes accepted")
	}
	withFlag := []byte(`{"config_schema_version":1,"features":{"model_name_routing":true},"models":[],"upstreams":[]}`)
	if err := ValidateSnapshot(withFlag); err == nil {
		t.Fatal("schema 1 body declaring routing support accepted")
	}
}
