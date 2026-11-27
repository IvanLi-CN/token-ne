package cores

import (
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strconv"
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
	"upstream_id": true, "position": true, "enabled": true, "description": true,
}

var policyFieldSet = map[string]bool{
	"upstream_id": true, "default_action": true, "allow_patterns": true, "deny_patterns": true,
}

var proxyFieldSet = map[string]bool{"id": true, "endpoint": true, "enabled": true}

// upstreamFieldSet is the closed set the publisher emits for an upstream. The
// core tolerates unknown upstream fields for forward compatibility, so a
// misspelled `egressProxyId` would silently mean direct egress there; the
// publisher must reject it instead.
var upstreamFieldSet = map[string]bool{
	"id": true, "name": true, "base_url": true, "auth_style": true, "api_key": true, "enabled": true,
	"connect_timeout_ms": true, "first_byte_timeout_ms": true, "idle_timeout_ms": true, "egress_proxy_id": true,
}

type snapshotRoute struct {
	ID            string  `json:"id"`
	Pattern       string  `json:"pattern"`
	MatchType     string  `json:"match_type"`
	TargetModelID string  `json:"target_model_id"`
	UpstreamID    *string `json:"upstream_id"`
	Position      *int    `json:"position"`
	Enabled       *bool   `json:"enabled"`
	Description   *string `json:"description"`
}

type snapshotPolicy struct {
	UpstreamID    string   `json:"upstream_id"`
	DefaultAction string   `json:"default_action"`
	AllowPatterns []string `json:"allow_patterns"`
	DenyPatterns  []string `json:"deny_patterns"`
}

type snapshotEnvelope struct {
	Schema   int                        `json:"config_schema_version"`
	Features map[string]json.RawMessage `json:"features"`
	Models   []struct {
		ModelID string `json:"model_id"`
	} `json:"models"`
	Upstreams        []map[string]json.RawMessage `json:"upstreams"`
	ModelRoutes      []map[string]json.RawMessage `json:"model_routes"`
	UpstreamPolicies []map[string]json.RawMessage `json:"upstream_policies"`
	EgressProxies    []map[string]json.RawMessage `json:"egress_proxies"`
}

type snapshotProxy struct {
	ID       string `json:"id"`
	Endpoint string `json:"endpoint"`
	Enabled  *bool  `json:"enabled"`
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
	// The rules below are the schema 2 contract. A schema 1 body is rendered for a
	// core that predates position and upstream_model_id, so validating it against
	// these field sets would reject a body that is correct for its reader.
	if envelope.Schema == 1 {
		return validateSchemaOneEnvelope(envelope)
	}
	// Unknown fields are rejected first: a misspelled routing field must not
	// become a silent no-op in a newer core.
	for i, item := range envelope.ModelRoutes {
		if field, ok := firstUnknownField(item, routeFieldSet); ok {
			return snapshotReject(fmt.Sprintf("model_routes[%d].%s", i, field), "unknown route field")
		}
		// Enum checks run before the feature gate so a combined violation reports
		// the same field path the core's schema pass reports.
		if raw, ok := item["match_type"]; ok {
			var matchType string
			if json.Unmarshal(raw, &matchType) != nil || (matchType != "exact" && matchType != "glob") {
				return snapshotReject(fmt.Sprintf("model_routes[%d].match_type", i), "must be exact or glob")
			}
		}
	}
	for i, item := range envelope.UpstreamPolicies {
		if field, ok := firstUnknownField(item, policyFieldSet); ok {
			return snapshotReject(fmt.Sprintf("upstream_policies[%d].%s", i, field), "unknown policy field")
		}
		if raw, ok := item["default_action"]; ok {
			var action string
			if json.Unmarshal(raw, &action) != nil || (action != "allow" && action != "deny") {
				return snapshotReject(fmt.Sprintf("upstream_policies[%d].default_action", i), "must be allow or deny")
			}
		}
		// An explicit JSON null is not an empty pattern list; the core's Vec
		// rejects it, so the publisher must reject it too.
		for _, field := range []string{"allow_patterns", "deny_patterns"} {
			if raw, ok := item[field]; ok && string(raw) == "null" {
				return snapshotReject(fmt.Sprintf("upstream_policies[%d].%s", i, field), "must be an array")
			}
		}
	}
	for i, item := range envelope.EgressProxies {
		if field, ok := firstUnknownField(item, proxyFieldSet); ok {
			return snapshotReject(fmt.Sprintf("egress_proxies[%d].%s", i, field), "unknown egress proxy field")
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
	for i, item := range envelope.Upstreams {
		if field, ok := firstUnknownField(item, upstreamFieldSet); ok {
			return snapshotReject(fmt.Sprintf("upstreams[%d].%s", i, field), "unknown upstream field")
		}
		var u struct {
			ID string `json:"id"`
		}
		if err := unmarshalObject(item, &u); err != nil {
			return snapshotReject(fmt.Sprintf("upstreams[%d]", i), "invalid upstream field type")
		}
		upstreams[u.ID] = true
	}
	if err := validateEgress(envelope); err != nil {
		return err
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
	positions := map[string]map[int]bool{}
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
		if !models[route.TargetModelID] {
			return snapshotReject(base+".target_model_id", "referenced model does not exist")
		}
		if route.UpstreamID != nil && !upstreams[*route.UpstreamID] {
			return snapshotReject(base+".upstream_id", "referenced upstream does not exist")
		}
		if route.Position == nil || *route.Position < 0 {
			return snapshotReject(base+".position", "must be zero or greater")
		}
		if route.Enabled == nil {
			return snapshotReject(base+".enabled", "is required")
		}
		if route.Description != nil && utf8.RuneCountInString(*route.Description) > 1024 {
			return snapshotReject(base+".description", "maximum length is 1024")
		}
		// Positions are unique inside a scope only; two scopes never compete.
		// Patterns carry no uniqueness or overlap rule: order decides the winner,
		// so duplicates and overlapping Globs are legitimate.
		scope := ""
		if route.UpstreamID != nil {
			scope = *route.UpstreamID
		}
		if positions[scope] == nil {
			positions[scope] = map[int]bool{}
		}
		if positions[scope][*route.Position] {
			return snapshotReject(base+".position", "duplicate position in scope")
		}
		positions[scope][*route.Position] = true
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

// unmarshalObject re-decodes one raw object so a typed view of it is available
// without a second parse of the whole body.
func unmarshalObject(item map[string]json.RawMessage, dst any) error {
	raw, err := json.Marshal(item)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

// validateEgress mirrors the core's egress rules so the publisher never sends a
// snapshot the target core answers with 422. Paths are the ones the contract
// names: egress_proxies[<i>].<field> for a bad entry, egress_proxies for a
// feature disagreement on the array and upstreams[<i>].egress_proxy_id for a
// dangling binding or a feature disagreement on a binding.
func validateEgress(envelope snapshotEnvelope) error {
	proxies := make([]snapshotProxy, 0, len(envelope.EgressProxies))
	for i, item := range envelope.EgressProxies {
		var proxy snapshotProxy
		if err := unmarshalObject(item, &proxy); err != nil {
			return snapshotReject(fmt.Sprintf("egress_proxies[%d]", i), "invalid egress proxy field type")
		}
		proxies = append(proxies, proxy)
	}
	active, _ := featureTrue(envelope.Features, FeatureEgressProxy)

	defined := map[string]bool{}
	for i, proxy := range proxies {
		base := fmt.Sprintf("egress_proxies[%d]", i)
		if !UUID(proxy.ID, false) {
			return snapshotReject(base+".id", "must be a canonical lowercase UUID")
		}
		if defined[proxy.ID] {
			return snapshotReject("egress_proxies.id", "duplicate egress proxy id")
		}
		defined[proxy.ID] = true
		if !validSocks5hEndpoint(proxy.Endpoint) {
			return snapshotReject(base+".endpoint", "must be socks5h://<host>:<port> with a port between 1024 and 65535")
		}
		if proxy.Enabled == nil {
			return snapshotReject(base+".enabled", "is required")
		}
	}

	// The core checks the array/feature agreement before any binding, so a
	// snapshot that violates both reports "egress_proxies".
	if len(proxies) > 0 && !active {
		return snapshotReject("egress_proxies", "feature must be true when the array is non-empty")
	}

	for i, item := range envelope.Upstreams {
		raw, ok := item["egress_proxy_id"]
		if !ok || !nonNullJSON(raw) {
			continue
		}
		path := fmt.Sprintf("upstreams[%d].egress_proxy_id", i)
		if !active {
			return snapshotReject(path, "feature must be true when an upstream binds a proxy")
		}
		var id string
		if err := json.Unmarshal(raw, &id); err != nil {
			return snapshotReject(path, "must be a string or null")
		}
		if !defined[id] {
			return snapshotReject(path, "referenced egress proxy does not exist in this snapshot")
		}
	}
	return nil
}

// validSocks5hEndpoint accepts exactly socks5h://<host>:<port>: lowercase
// scheme, a DNS name or a bracketed IP literal, no credentials, path, query or
// fragment and a port inside 1024 to 65535.
func validSocks5hEndpoint(raw string) bool {
	authority, ok := strings.CutPrefix(raw, "socks5h://")
	if !ok || len(raw) > 256 {
		return false
	}
	// The port is the last segment: an IPv6 literal carries colons of its own.
	i := strings.LastIndexByte(authority, ':')
	if i <= 0 || i == len(authority)-1 {
		return false
	}
	host, port := authority[:i], authority[i+1:]
	for _, r := range port {
		if r < '0' || r > '9' {
			return false
		}
	}
	// The schema pattern has no leading zero form, so accepting one here would
	// publish bytes the schema itself rejects.
	if len(port) > 1 && port[0] == '0' {
		return false
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1024 || n > 65535 {
		return false
	}
	if inner, ok := strings.CutPrefix(host, "["); ok {
		inner, closed := strings.CutSuffix(inner, "]")
		return closed && strings.Contains(inner, ":") && net.ParseIP(inner) != nil
	}
	if !isAlnum(host[0]) || !isAlnum(host[len(host)-1]) {
		return false
	}
	for _, r := range host {
		if !isAlnum(byte(r)) && r != '.' && r != '-' {
			return false
		}
	}
	return true
}

func isAlnum(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// featureTrue reports whether a snapshot feature is explicitly true.
func featureTrue(features map[string]json.RawMessage, name string) (bool, bool) {
	raw, ok := features[name]
	if !ok {
		return false, false
	}
	var enabled bool
	if json.Unmarshal(raw, &enabled) != nil {
		return false, false
	}
	return enabled, true
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

// validateSchemaOneEnvelope enforces only what both generations agree on: the
// body carries no routing or policy (the surface was dropped for this reader) and
// every capability points at objects that exist. The schema 2 rules about
// position, pattern uniqueness and upstream_model_id do not apply here.
func validateSchemaOneEnvelope(envelope snapshotEnvelope) error {
	if len(envelope.ModelRoutes) > 0 {
		return snapshotReject("model_routes", "a schema 1 snapshot must not carry routing")
	}
	if len(envelope.UpstreamPolicies) > 0 {
		return snapshotReject("upstream_policies", "a schema 1 snapshot must not carry policies")
	}
	for _, name := range []string{FeatureModelNameRouting, FeatureUpstreamModelPolicy} {
		raw, present := envelope.Features[name]
		if !present {
			continue
		}
		var enabled bool
		if json.Unmarshal(raw, &enabled) == nil && enabled {
			return snapshotReject("features."+name, "a schema 1 snapshot must not declare routing or policy support")
		}
	}
	models := map[string]bool{}
	for _, m := range envelope.Models {
		models[m.ModelID] = true
	}
	upstreams := map[string]bool{}
	for i, item := range envelope.Upstreams {
		if field, ok := firstUnknownField(item, upstreamFieldSet); ok {
			return snapshotReject(fmt.Sprintf("upstreams[%d].%s", i, field), "unknown upstream field")
		}
		var u struct {
			ID string `json:"id"`
		}
		if err := unmarshalObject(item, &u); err != nil {
			return snapshotReject(fmt.Sprintf("upstreams[%d]", i), "invalid upstream field type")
		}
		upstreams[u.ID] = true
	}
	return nil
}
