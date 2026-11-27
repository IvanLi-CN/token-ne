package cores

import (
	"encoding/json"
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
// is ever sent. Policies are part of that contract: their field set and
// policy_allows are unchanged between the generations.
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
	// A policy that stops travelling would silently widen which upstream models
	// a request may see, so the older shape must carry it unchanged.
	withPolicy := []byte(`{
		"config_schema_version": 1,
		"features": {"upstream_model_policy": true},
		"models": [], "upstreams": [],
		"upstream_policies": [{"upstream_id":"0193f0c2-7b1a-7c3e-9f00-0000000000c1","default_action":"deny","allow_patterns":["gpt-*"],"deny_patterns":[]}]
	}`)
	if err := ValidateSnapshot(withPolicy); err != nil {
		t.Fatalf("schema 1 body carrying policies rejected: %v", err)
	}
	// The flag is what makes the array legal for the older reader.
	policyWithoutFlag := []byte(`{
		"config_schema_version": 1,
		"features": {},
		"models": [], "upstreams": [],
		"upstream_policies": [{"upstream_id":"0193f0c2-7b1a-7c3e-9f00-0000000000c1","default_action":"deny","allow_patterns":["gpt-*"],"deny_patterns":[]}]
	}`)
	if err := ValidateSnapshot(policyWithoutFlag); err == nil {
		t.Fatal("schema 1 body carrying policies without the flag accepted")
	}
	withFlag := []byte(`{"config_schema_version":1,"features":{"model_name_routing":true},"models":[],"upstreams":[]}`)
	if err := ValidateSnapshot(withFlag); err == nil {
		t.Fatal("schema 1 body declaring routing support accepted")
	}
}

// The schema 1 rendering is the only place that decides what an older core sees.
// Routes must go (the two generations read the list differently) but policies
// must stay: they mean the same thing on both sides, and dropping them would
// silently widen the upstream models a request can reach.
func TestSchemaOneRenderingKeepsPoliciesAndDropsRoutes(t *testing.T) {
	body := configBody{
		Protocol:     1,
		Schema:       SchemaFloor,
		Features:     map[string]bool{FeatureModelNameRouting: true, FeatureUpstreamModelPolicy: true, "chat_completions": true},
		Models:       json.RawMessage(`[{"model_id":"gpt-5","enabled":true}]`),
		Capabilities: json.RawMessage(`[{"upstream_id":"0193f0c2-7b1a-7c3e-9f00-0000000000c1","upstream_model_id":"gpt-5","model_id":"gpt-5","enabled":true}]`),
		ModelRoutes: []routeRule{{
			ID: "0193f0c2-7b1a-7c3e-9f00-0000000000a1", Pattern: "x", MatchType: "exact",
			TargetModelID: "gpt-5", Position: 0, Enabled: true,
		}},
		UpstreamPolicies: []upstreamPolicy{{
			UpstreamID:    "0193f0c2-7b1a-7c3e-9f00-0000000000c1",
			DefaultAction: "deny",
			AllowPatterns: []string{"gpt-*"},
		}},
	}
	if err := renderSchemaOne(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.ModelRoutes) != 0 {
		t.Fatalf("routes survived the schema 1 rendering: %+v", body.ModelRoutes)
	}
	if len(body.UpstreamPolicies) != 1 {
		t.Fatalf("policies were dropped: %+v", body.UpstreamPolicies)
	}
	if body.Features[FeatureModelNameRouting] {
		t.Fatal("routing capability survived the schema 1 rendering")
	}
	if !body.Features[FeatureUpstreamModelPolicy] {
		t.Fatal("policy capability was dropped, so the array would be rejected by the older reader")
	}
	// The binding must speak the older field names, or the core rejects it as an
	// unknown field.
	var caps []map[string]json.RawMessage
	if err := json.Unmarshal(body.Capabilities, &caps); err != nil {
		t.Fatal(err)
	}
	if _, ok := caps[0]["upstream_model_id"]; ok {
		t.Fatal("upstream_model_id survived the schema 1 rendering")
	}
	if _, ok := caps[0]["upstream_model_name"]; !ok {
		t.Fatal("upstream_model_name missing from the schema 1 rendering")
	}
}

// v1 orders candidates by `priority` and breaks ties with a weighted random
// draw, while v2 walks a stable (upstream_id, upstream_model_id) order. The
// older rendering has to reproduce that walk, not flatten it: a constant
// priority turns a tiered choice into a uniform one and silently redistributes
// live traffic across upstreams.
func TestSchemaOneRenderingReproducesTheV2CandidateOrder(t *testing.T) {
	body := configBody{
		Protocol: 1,
		Schema:   SchemaFloor,
		Features: map[string]bool{"chat_completions": true},
		// Already in the SQL's order: upstream_id, then upstream_model_id.
		Capabilities: json.RawMessage(`[
			{"upstream_id":"aaa","upstream_model_id":"m-a","model_id":"gpt-5","enabled":true},
			{"upstream_id":"aaa","upstream_model_id":"m-b","model_id":"gpt-5","enabled":true},
			{"upstream_id":"bbb","upstream_model_id":"m-c","model_id":"gpt-5","enabled":true},
			{"upstream_id":"aaa","upstream_model_id":"m-a","model_id":"gpt-4o","enabled":false}
		]`),
	}
	if err := renderSchemaOne(&body); err != nil {
		t.Fatal(err)
	}
	var caps []struct {
		UpstreamID string `json:"upstream_id"`
		Name       string `json:"upstream_model_name"`
		ModelID    string `json:"model_id"`
		Priority   int    `json:"priority"`
		Weight     int    `json:"weight"`
		Enabled    bool   `json:"enabled"`
	}
	if err := json.Unmarshal(body.Capabilities, &caps); err != nil {
		t.Fatal(err)
	}
	want := []struct {
		upstream, name string
		priority       int
		enabled        bool
	}{
		{"aaa", "m-a", 0, true},
		{"aaa", "m-b", 1, true},
		{"bbb", "m-c", 2, true},
		// A different model starts its own ranking at 0.
		{"aaa", "m-a", 0, false},
	}
	if len(caps) != len(want) {
		t.Fatalf("got %d bindings, want %d", len(caps), len(want))
	}
	for i, w := range want {
		c := caps[i]
		if c.UpstreamID != w.upstream || c.Name != w.name || c.Priority != w.priority || c.Enabled != w.enabled {
			t.Fatalf("binding %d = %+v, want upstream=%s name=%s priority=%d enabled=%v",
				i, c, w.upstream, w.name, w.priority, w.enabled)
		}
		if c.Weight <= 0 {
			t.Fatalf("binding %d has weight %d; v1 filters on weight > 0", i, c.Weight)
		}
	}
	// Distinct tiers are what makes the order deterministic: equal priorities
	// inside one model would hand the choice back to the weighted draw.
	seen := map[string]map[int]bool{}
	for _, c := range caps {
		if seen[c.ModelID] == nil {
			seen[c.ModelID] = map[int]bool{}
		}
		if seen[c.ModelID][c.Priority] {
			t.Fatalf("model %s reuses priority %d, so the order is no longer fixed", c.ModelID, c.Priority)
		}
		seen[c.ModelID][c.Priority] = true
	}
}
