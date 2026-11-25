package cores

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/IvanLi-CN/token-ne/server/internal/modelglob"
)

// SnapshotDetail is one field-level reason a configuration snapshot is
// rejected. Path uses the same indexing the core reports, so a publisher bug
// and a core rejection point at the same field.
type SnapshotDetail struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// SnapshotValidationError is a semantic rejection. Malformed JSON is reported
// separately as ErrInvalid because the protocol maps it to 400.
type SnapshotValidationError struct {
	Details []SnapshotDetail
}

func (e *SnapshotValidationError) Error() string {
	if len(e.Details) == 0 {
		return "invalid configuration snapshot"
	}
	return fmt.Sprintf("%s: %s", e.Details[0].Path, e.Details[0].Reason)
}

// Unwrap lets callers keep using errors.Is for the shared control errors.
func (e *SnapshotValidationError) Unwrap() error { return ErrInvalid }

func snapshotReject(path, reason string) *SnapshotValidationError {
	return &SnapshotValidationError{Details: []SnapshotDetail{{Path: path, Reason: reason}}}
}

var routeFieldSet = map[string]bool{
	"id": true, "pattern": true, "match_type": true, "target_model_id": true,
	"upstream_id": true, "priority": true, "enabled": true, "description": true,
}

var policyFieldSet = map[string]bool{
	"upstream_id": true, "default_action": true, "allow_patterns": true, "deny_patterns": true,
}

type snapshotRoute struct {
	ID            string  `json:"id"`
	Pattern       string  `json:"pattern"`
	MatchType     string  `json:"match_type"`
	TargetModelID string  `json:"target_model_id"`
	UpstreamID    *string `json:"upstream_id"`
	Priority      *int    `json:"priority"`
	Enabled       bool    `json:"enabled"`
	Description   *string `json:"description"`
}

type snapshotPolicy struct {
	UpstreamID    string   `json:"upstream_id"`
	DefaultAction string   `json:"default_action"`
	AllowPatterns []string `json:"allow_patterns"`
	DenyPatterns  []string `json:"deny_patterns"`
}

type snapshotEnvelope struct {
	Features map[string]json.RawMessage `json:"features"`
	Models   []struct {
		ModelID string `json:"model_id"`
	} `json:"models"`
	Upstreams []struct {
		ID string `json:"id"`
	} `json:"upstreams"`
	ModelRoutes      []map[string]json.RawMessage `json:"model_routes"`
	UpstreamPolicies []map[string]json.RawMessage `json:"upstream_policies"`
}

// ValidateSnapshot applies the second-phase semantic rules the core enforces
// before a snapshot may be sent. Every rejection carries details[].path. It is
// deliberately narrow: no credentials, no deployment switches and no
// phase-one fields beyond the references these rules need.
func ValidateSnapshot(body []byte) error {
	var envelope snapshotEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ErrInvalid
	}
	// Unknown fields are rejected first: a misspelled routing field must not
	// become a silent no-op in a newer core.
	for i, item := range envelope.ModelRoutes {
		if field, ok := firstUnknownField(item, routeFieldSet); ok {
			return snapshotReject(fmt.Sprintf("model_routes[%d].%s", i, field), "unknown route field")
		}
	}
	for i, item := range envelope.UpstreamPolicies {
		if field, ok := firstUnknownField(item, policyFieldSet); ok {
			return snapshotReject(fmt.Sprintf("upstream_policies[%d].%s", i, field), "unknown policy field")
		}
	}

	// Content and capability flags must agree; a core that ignores the array
	// would silently change routing.
	if len(envelope.ModelRoutes) > 0 {
		if err := requireFeatureTrue(envelope.Features, FeatureModelNameRouting, "features."+FeatureModelNameRouting); err != nil {
			return err
		}
	}
	if len(envelope.UpstreamPolicies) > 0 {
		if err := requireFeatureTrue(envelope.Features, FeatureUpstreamModelPolicy, "features."+FeatureUpstreamModelPolicy); err != nil {
			return err
		}
	}

	routes := make([]snapshotRoute, 0, len(envelope.ModelRoutes))
	for i, item := range envelope.ModelRoutes {
		raw, err := json.Marshal(item)
		if err != nil {
			return snapshotReject(fmt.Sprintf("model_routes[%d]", i), "invalid route object")
		}
		var route snapshotRoute
		if err := json.Unmarshal(raw, &route); err != nil {
			return snapshotReject(fmt.Sprintf("model_routes[%d]", i), "invalid route field type")
		}
		routes = append(routes, route)
	}
	policies := make([]snapshotPolicy, 0, len(envelope.UpstreamPolicies))
	for i, item := range envelope.UpstreamPolicies {
		raw, err := json.Marshal(item)
		if err != nil {
			return snapshotReject(fmt.Sprintf("upstream_policies[%d]", i), "invalid policy object")
		}
		var policy snapshotPolicy
		if err := json.Unmarshal(raw, &policy); err != nil {
			return snapshotReject(fmt.Sprintf("upstream_policies[%d]", i), "invalid policy field type")
		}
		policies = append(policies, policy)
	}

	models := map[string]bool{}
	for _, m := range envelope.Models {
		models[m.ModelID] = true
	}
	upstreams := map[string]bool{}
	for _, u := range envelope.Upstreams {
		upstreams[u.ID] = true
	}

	seenPolicies := map[string]bool{}
	for i, policy := range policies {
		base := fmt.Sprintf("upstream_policies[%d]", i)
		if seenPolicies[policy.UpstreamID] {
			return snapshotReject("upstream_policies.upstream_id", "duplicate upstream policy")
		}
		seenPolicies[policy.UpstreamID] = true
		if !upstreams[policy.UpstreamID] {
			return snapshotReject(base+".upstream_id", "referenced upstream does not exist")
		}
		if policy.DefaultAction != "allow" && policy.DefaultAction != "deny" {
			return snapshotReject(base+".default_action", "must be allow or deny")
		}
		for _, list := range []struct {
			name     string
			patterns []string
		}{{"allow_patterns", policy.AllowPatterns}, {"deny_patterns", policy.DenyPatterns}} {
			seen := map[string]bool{}
			for j, pattern := range list.patterns {
				path := fmt.Sprintf("%s.%s[%d]", base, list.name, j)
				if err := modelglob.Validate(pattern); err != nil {
					return snapshotReject(path, err.Error())
				}
				if seen[pattern] {
					return snapshotReject(path, "duplicate pattern")
				}
				seen[pattern] = true
			}
		}
	}

	seenIDs := map[string]bool{}
	exact := map[string]bool{}
	for i, route := range routes {
		base := fmt.Sprintf("model_routes[%d]", i)
		if seenIDs[route.ID] {
			return snapshotReject("model_routes.id", "duplicate route id")
		}
		seenIDs[route.ID] = true
		if !UUID(route.ID, false) {
			return snapshotReject(base+".id", "must be a canonical UUID")
		}
		switch route.MatchType {
		case "exact":
			if !validSnapshotName(route.Pattern) {
				return snapshotReject(base+".pattern", "must contain 1 to 256 characters")
			}
		case "glob":
			if err := modelglob.Validate(route.Pattern); err != nil {
				return snapshotReject(base+".pattern", err.Error())
			}
		default:
			return snapshotReject(base+".match_type", "must be exact or glob")
		}
		if models[route.Pattern] {
			return snapshotReject(base+".pattern", "pattern is reserved by a global model id")
		}
		if !models[route.TargetModelID] {
			return snapshotReject(base+".target_model_id", "referenced model does not exist")
		}
		if route.UpstreamID != nil && !upstreams[*route.UpstreamID] {
			return snapshotReject(base+".upstream_id", "referenced upstream does not exist")
		}
		if route.Priority == nil || *route.Priority < 0 || *route.Priority > 100000 {
			return snapshotReject(base+".priority", "must be between 0 and 100000")
		}
		if route.Description != nil && utf8.RuneCountInString(*route.Description) > 1024 {
			return snapshotReject(base+".description", "maximum length is 1024")
		}
		if route.Enabled && route.MatchType == "exact" {
			if exact[route.Pattern] {
				return snapshotReject(base+".pattern", "duplicate enabled exact rule")
			}
			exact[route.Pattern] = true
		}
	}
	// Two enabled glob rules at the same priority must agree on their target,
	// or which one wins depends on iteration order. The core enforces this too,
	// so publishing such a snapshot would fail every instance.
	for i, a := range routes {
		if !a.Enabled || a.MatchType != "glob" {
			continue
		}
		for j := i + 1; j < len(routes); j++ {
			b := routes[j]
			if !b.Enabled || b.MatchType != "glob" || a.Priority == nil || b.Priority == nil || *a.Priority != *b.Priority {
				continue
			}
			overlap, err := modelglob.Overlaps(a.Pattern, b.Pattern)
			if err != nil {
				return snapshotReject(fmt.Sprintf("model_routes[%d].pattern", j), err.Error())
			}
			if overlap && (a.TargetModelID != b.TargetModelID || !sameOptional(a.UpstreamID, b.UpstreamID)) {
				return snapshotReject(fmt.Sprintf("model_routes[%d].pattern", j), "overlaps a same-priority glob rule with a different target or upstream")
			}
		}
	}
	return nil
}

// firstUnknownField returns the alphabetically first key outside allowed so the
// reported path is stable; the core reports unknown fields in map order, which
// serde keeps sorted.
func firstUnknownField(item map[string]json.RawMessage, allowed map[string]bool) (string, bool) {
	unknown := make([]string, 0, len(item))
	for field := range item {
		if !allowed[field] {
			unknown = append(unknown, field)
		}
	}
	if len(unknown) == 0 {
		return "", false
	}
	sort.Strings(unknown)
	return unknown[0], true
}

func sameOptional(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// validSnapshotName mirrors the core's `text` validation for exact patterns:
// non-empty, at most 256 scalars and no control characters. The publisher must
// not accept a snapshot the target core would reject with 422.
func validSnapshotName(value string) bool {
	return value != "" && utf8.RuneCountInString(value) <= 256 && !strings.ContainsFunc(value, unicode.IsControl)
}

func requireFeatureTrue(features map[string]json.RawMessage, name, path string) error {
	raw, ok := features[name]
	if !ok {
		return snapshotReject(path, "feature must be true when the array is non-empty")
	}
	var enabled bool
	if err := json.Unmarshal(raw, &enabled); err != nil || !enabled {
		return snapshotReject(path, "feature must be true when the array is non-empty")
	}
	return nil
}
