package store

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/IvanLi-CN/token-ne/server/internal/modelglob"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// globalScopeKey is the zero UUID the schema uses for the global route scope, so
// an unscoped rule compares and sorts like any other scope.
const globalScopeKey = "00000000-0000-0000-0000-000000000000"

// routeScope is the SQL expression naming a route row's scope.
const routeScope = "coalesce(upstream_id,'" + globalScopeKey + "'::uuid)"

// lockRouteScope serializes writers that compete for positions inside one
// scope. Positions are allocated as max+1 under this lock, so two concurrent
// creates cannot pick the same slot; unrelated scopes never wait on each other.
func lockRouteScope(ctx context.Context, tx pgx.Tx, upstreamID *string) error {
	_, e := tx.Exec(ctx, "select pg_advisory_xact_lock(hashtextextended(coalesce($1::uuid,'"+globalScopeKey+"'::uuid)::text,0))", upstreamID)
	return e
}

// FieldError reports a rejected write together with the request field that
// caused it. Kind is ErrConflict (409) or ErrReference (422) so callers map the
// status code without re-deriving the reason.
type FieldError struct {
	Path   string
	Reason string
	Kind   error
}

func (e *FieldError) Error() string { return e.Path + ": " + e.Reason }

// Unwrap exposes the kind so errors.Is(e, ErrConflict) and errors.Is(e,
// ErrReference) work.
func (e *FieldError) Unwrap() error { return e.Kind }

// ModelRoute is one exact or Glob rule resolving a requested name to a system
// model. Rules belong to a scope (global, or one upstream) and are tried in
// ascending position; the first match wins. The JSON shape is the management
// API contract; the configuration snapshot uses its own projection.
type ModelRoute struct {
	ID            string    `json:"id"`
	Pattern       string    `json:"pattern"`
	MatchType     string    `json:"match_type"`
	TargetModelID string    `json:"target_model_id"`
	UpstreamID    *string   `json:"upstream_id"`
	Position      int       `json:"position"`
	Enabled       bool      `json:"enabled"`
	Description   *string   `json:"description"`
	Created       time.Time `json:"created_at"`
	Updated       time.Time `json:"updated_at"`
}

// RouteCandidate is one upstream that can serve the resolved model together
// with the policy decision applied to it.
type RouteCandidate struct {
	UpstreamID      string  `json:"upstream_id"`
	UpstreamModelID string  `json:"upstream_model_id"`
	Allowed         bool    `json:"allowed"`
	Reason          *string `json:"reason"`
}

// RoutePreview is the read-only result of resolving a requested model name the
// same way the core does at request time.
type RoutePreview struct {
	RequestedModel string           `json:"requested_model"`
	ResolvedModel  *string          `json:"resolved_model"`
	MatchedRule    *string          `json:"matched_rule"`
	RuleKind       *string          `json:"rule_kind"`
	Candidates     []RouteCandidate `json:"candidates"`
}

const routeColumns = "id::text,pattern,match_type,target_model_id,upstream_id::text,position,enabled,description,created_at,updated_at"

func scanRoute(row pgx.Row) (ModelRoute, error) {
	var r ModelRoute
	e := row.Scan(&r.ID, &r.Pattern, &r.MatchType, &r.TargetModelID, &r.UpstreamID, &r.Position, &r.Enabled, &r.Description, &r.Created, &r.Updated)
	return r, normalize(e)
}

func scanRoutes(rows pgx.Rows) ([]ModelRoute, error) {
	out := []ModelRoute{}
	for rows.Next() {
		r, e := scanRoute(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Routes lists every rule in resolution order: scope key, then position. The
// unique (scope, position) index makes the order total without an ID tie-break.
func (s *Store) Routes(ctx context.Context) ([]ModelRoute, error) {
	rows, e := s.Pool.Query(ctx, "select "+routeColumns+" from model_route_rules order by "+routeScope+",position")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	return scanRoutes(rows)
}

// SaveRoute creates or updates one rule. A new rule always lands at the end of
// its scope; position is only ever changed by ReorderRoutes, so an update keeps
// the stored order even when the pattern or target changes.
func (s *Store) SaveRoute(ctx context.Context, route ModelRoute, create bool, a Audit) (ModelRoute, error) {
	if route.ID == "" {
		route.ID = uuid.NewString()
	}
	e := s.mutate(ctx, a, func(tx pgx.Tx) error {
		if e := validateRouteRule(route); e != nil {
			return e
		}
		if e := validateRouteState(ctx, tx, route); e != nil {
			return e
		}
		var upstream, description any
		if route.UpstreamID != nil {
			upstream = *route.UpstreamID
		}
		if route.Description != nil {
			description = *route.Description
		}
		var query string
		if create {
			if e := lockRouteScope(ctx, tx, route.UpstreamID); e != nil {
				return e
			}
			query = `insert into model_route_rules(id,pattern,match_type,target_model_id,upstream_id,position,enabled,description)
values($1,$2,$3,$4,$5::uuid,coalesce((select max(position)+1 from model_route_rules where ` + routeScope + ` = coalesce($5::uuid,'` + globalScopeKey + `'::uuid)),0),$6,$7)
returning ` + routeColumns
		} else {
			// The scope is part of a rule's identity, not an editable field: moving a
			// rule between scopes would leave a gap in the source scope and could
			// collide in the target, neither under that scope's lock.
			var stored *string
			if e := tx.QueryRow(ctx, "select upstream_id::text from model_route_rules where id=$1", route.ID).Scan(&stored); e != nil {
				return normalize(e)
			}
			if !sameScope(stored, route.UpstreamID) {
				return &FieldError{Path: "upstream_id", Reason: "cannot move a rule to another scope; create it there instead", Kind: ErrReference}
			}
			query = "update model_route_rules set pattern=$2,match_type=$3,target_model_id=$4,enabled=$5,description=$6,updated_at=now() where id=$1 returning " + routeColumns
		}
		args := []any{route.ID, route.Pattern, route.MatchType, route.TargetModelID, upstream, route.Enabled, description}
		if !create {
			args = []any{route.ID, route.Pattern, route.MatchType, route.TargetModelID, route.Enabled, description}
		}
		saved, err := scanRoute(tx.QueryRow(ctx, query, args...))
		route = saved
		return err
	})
	if e != nil {
		return ModelRoute{}, e
	}
	return route, nil
}

// ReorderRoutes rewrites one scope's positions to 0..n-1 in the given order.
// The scope is locked for the transaction and must match the submitted set
// exactly, so a stale editor cannot drop or duplicate a rule; any mismatch
// rolls the whole reorder back.
func (s *Store) ReorderRoutes(ctx context.Context, upstreamID *string, ids []string, a Audit) ([]ModelRoute, error) {
	if len(ids) == 0 {
		return nil, &FieldError{Path: "route_ids", Reason: "must list at least one route", Kind: ErrReference}
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if _, e := uuid.Parse(id); e != nil {
			return nil, &FieldError{Path: "route_ids", Reason: "every route id must be a UUID", Kind: ErrReference}
		}
		if seen[id] {
			return nil, &FieldError{Path: "route_ids", Reason: "route ids must be unique", Kind: ErrReference}
		}
		seen[id] = true
	}
	var out []ModelRoute
	e := s.mutate(ctx, a, func(tx pgx.Tx) error {
		if e := lockRouteScope(ctx, tx, upstreamID); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, "select id::text from model_route_rules where "+routeScope+" = coalesce($1,'"+globalScopeKey+"'::uuid) for update", upstreamID)
		if e != nil {
			return e
		}
		existing := make(map[string]bool, len(ids))
		for rows.Next() {
			var id string
			if e := rows.Scan(&id); e != nil {
				rows.Close()
				return e
			}
			existing[id] = true
		}
		rows.Close()
		if e := rows.Err(); e != nil {
			return e
		}
		if len(existing) != len(ids) {
			return &FieldError{Path: "route_ids", Reason: "must list every route in this scope exactly once", Kind: ErrReference}
		}
		for _, id := range ids {
			if !existing[id] {
				return &FieldError{Path: "route_ids", Reason: "route does not belong to this scope", Kind: ErrReference}
			}
		}
		// Offsetting first keeps the (scope, position) unique index satisfied
		// while the new positions are written one by one. The offset clears the
		// scope's real maximum, not len(ids): positions turn sparse when a
		// non-tail rule is deleted, and an offset of len(ids) would then collide
		// with a surviving position and make the scope unreorderable forever.
		if _, e := tx.Exec(ctx, `update model_route_rules set position=position+(select coalesce(max(position),0)+1 from model_route_rules where `+routeScope+` = coalesce($1,'`+globalScopeKey+`'::uuid)) where `+routeScope+` = coalesce($1,'`+globalScopeKey+`'::uuid)`, upstreamID); e != nil {
			return e
		}
		for i, id := range ids {
			if _, e := tx.Exec(ctx, "update model_route_rules set position=$2,updated_at=now() where id=$1", id, i); e != nil {
				return e
			}
		}
		out, e = s.routesInScope(ctx, tx, upstreamID)
		return e
	})
	if e != nil {
		return nil, e
	}
	return out, nil
}

func (s *Store) routesInScope(ctx context.Context, tx pgx.Tx, upstreamID *string) ([]ModelRoute, error) {
	rows, e := tx.Query(ctx, "select "+routeColumns+" from model_route_rules where "+routeScope+" = coalesce($1,'"+globalScopeKey+"'::uuid) order by position", upstreamID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	return scanRoutes(rows)
}

func (s *Store) DeleteRoute(ctx context.Context, id string, a Audit) error {
	return s.mutate(ctx, a, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, "delete from model_route_rules where id=$1", id)
		if e == nil && tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return e
	})
}

func validateRouteRule(r ModelRoute) error {
	if _, e := uuid.Parse(r.ID); e != nil {
		return &FieldError{Path: "id", Reason: "must be a UUID", Kind: ErrReference}
	}
	switch r.MatchType {
	case "glob":
		if e := modelglob.Validate(r.Pattern); e != nil {
			return &FieldError{Path: "pattern", Reason: "must be a valid restricted glob", Kind: ErrReference}
		}
	case "exact":
		if !validRouteName(r.Pattern) {
			return &FieldError{Path: "pattern", Reason: "must contain 1 to 256 characters", Kind: ErrReference}
		}
	default:
		return &FieldError{Path: "match_type", Reason: "must be exact or glob", Kind: ErrReference}
	}
	if !validRouteName(r.TargetModelID) {
		return &FieldError{Path: "target_model_id", Reason: "must contain 1 to 256 characters", Kind: ErrReference}
	}
	if r.Description != nil && utf8.RuneCountInString(*r.Description) > 1024 {
		return &FieldError{Path: "description", Reason: "maximum length is 1024", Kind: ErrReference}
	}
	if r.UpstreamID != nil {
		if _, e := uuid.Parse(*r.UpstreamID); e != nil {
			return &FieldError{Path: "upstream_id", Reason: "must be a UUID", Kind: ErrReference}
		}
	}
	return nil
}

// validateRouteState checks the references a table constraint cannot express.
// Patterns carry no uniqueness or overlap rule: order decides which one wins, so
// duplicates and overlapping Globs are legitimate.
func validateRouteState(ctx context.Context, tx pgx.Tx, r ModelRoute) error {
	var exists bool
	if e := tx.QueryRow(ctx, "select exists(select 1 from models where model_id=$1)", r.TargetModelID).Scan(&exists); e != nil {
		return e
	}
	if !exists {
		return &FieldError{Path: "target_model_id", Reason: "model does not exist", Kind: ErrReference}
	}
	if r.UpstreamID != nil {
		if e := tx.QueryRow(ctx, "select exists(select 1 from upstreams where id=$1)", *r.UpstreamID).Scan(&exists); e != nil {
			return e
		}
		if !exists {
			return &FieldError{Path: "upstream_id", Reason: "upstream does not exist", Kind: ErrReference}
		}
	}
	return nil
}

type previewModel struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
}

type previewRule struct {
	ID            string  `json:"id"`
	Pattern       string  `json:"pattern"`
	MatchType     string  `json:"match_type"`
	TargetModelID string  `json:"target_model_id"`
	UpstreamID    *string `json:"upstream_id"`
	Position      int     `json:"position"`
}

type previewUpstream struct {
	UpstreamID      string `json:"upstream_id"`
	ModelID         string `json:"model_id"`
	UpstreamModelID string `json:"upstream_model_id"`
}

type previewPolicy struct {
	UpstreamID    string   `json:"upstream_id"`
	DefaultAction string   `json:"default_action"`
	AllowPatterns []string `json:"allow_patterns"`
	DenyPatterns  []string `json:"deny_patterns"`
}

// PreviewRoute resolves a requested name the way the core does. The returned
// model is what a request that falls through to the global scope gets: the
// first matching global rule in position order. Candidates are computed per
// enabled upstream, each trying its own scope first and the global scope second.
// Unresolvable names still return a zero resolution.
func (s *Store) PreviewRoute(ctx context.Context, requested string) (RoutePreview, error) {
	if !validRouteName(requested) {
		return RoutePreview{}, &FieldError{Path: "requested_model", Reason: "must contain 1 to 256 characters", Kind: ErrReference}
	}
	var modelsRaw, rulesRaw, capabilitiesRaw, policiesRaw []byte
	e := s.Pool.QueryRow(ctx, `select
 coalesce((select jsonb_agg(jsonb_build_object('id',model_id,'enabled',enabled) order by model_id) from models),'[]'::jsonb),
 coalesce((select jsonb_agg(jsonb_build_object('id',id::text,'pattern',pattern,'match_type',match_type,'target_model_id',target_model_id,'upstream_id',upstream_id::text,'position',position) order by `+routeScope+`,position) from model_route_rules where enabled),'[]'::jsonb),
 coalesce((select jsonb_agg(jsonb_build_object('upstream_id',m.upstream_id::text,'model_id',m.model_id,'upstream_model_id',m.upstream_model_id) order by m.upstream_id,m.upstream_model_id) from upstream_models m join upstreams u on u.id=m.upstream_id where m.enabled and u.enabled),'[]'::jsonb),
 coalesce((select jsonb_agg(jsonb_build_object('upstream_id',upstream_id::text,'default_action',default_action,'allow_patterns',allow_patterns,'deny_patterns',deny_patterns) order by upstream_id) from upstream_model_policies),'[]'::jsonb)`).Scan(&modelsRaw, &rulesRaw, &capabilitiesRaw, &policiesRaw)
	if e != nil {
		return RoutePreview{}, e
	}
	var models []previewModel
	var rules []previewRule
	var capabilities []previewUpstream
	var policies []previewPolicy
	if json.Unmarshal(modelsRaw, &models) != nil || json.Unmarshal(rulesRaw, &rules) != nil ||
		json.Unmarshal(capabilitiesRaw, &capabilities) != nil || json.Unmarshal(policiesRaw, &policies) != nil {
		return RoutePreview{}, errors.New("invalid routing state")
	}
	preview := RoutePreview{RequestedModel: requested, Candidates: []RouteCandidate{}}
	// Upstream scopes are tried first in ascending upstream_id order, then the
	// global scope: the same order the core uses, so a preview never promises
	// resolution the data plane would not perform.
	rule := previewResolve(rules, requested)
	var target *previewModel
	if rule == nil {
		// Neither scope matched: the core falls back to the system model ID, so
		// the preview must too, or it would report 404 for a name the data plane
		// serves. No rule is reported in that case.
		target = findPreviewModel(models, requested)
	} else {
		target = findPreviewModel(models, rule.TargetModelID)
	}
	if target == nil || !target.Enabled {
		return preview, nil
	}
	preview.ResolvedModel = &target.ID
	if rule != nil {
		preview.MatchedRule = &rule.ID
		kind := rule.MatchType
		preview.RuleKind = &kind
	}
	for _, c := range capabilities {
		// Each enabled upstream resolves on its own: its scope first, then the
		// global scope. Another upstream's scoped rule never applies here, and a
		// double miss falls back to the system model ID, exactly like the core.
		scoped := previewScopeRule(rules, requested, &c.UpstreamID)
		if scoped == nil {
			scoped = previewScopeRule(rules, requested, nil)
		}
		scopedTarget := requested
		if scoped != nil {
			scopedTarget = scoped.TargetModelID
		}
		target := findPreviewModel(models, scopedTarget)
		if target == nil || !target.Enabled || target.ID != c.ModelID {
			continue
		}
		allowed := true
		var reason *string
		if !previewPolicyAllows(policies, c.UpstreamID, target.ID) {
			allowed = false
			reason = new("policy_denied")
		}
		preview.Candidates = append(preview.Candidates, RouteCandidate{UpstreamID: c.UpstreamID, UpstreamModelID: c.UpstreamModelID, Allowed: allowed, Reason: reason})
	}
	return preview, nil
}

// previewResolve picks the rule the core would use: each enabled upstream scope
// in ascending id order, then the global scope; within a scope the first match
// in position order wins. Rules arrive grouped per scope in position order.
func previewResolve(rules []previewRule, requested string) *previewRule {
	scopes := map[string]bool{}
	for i := range rules {
		if rules[i].UpstreamID != nil {
			scopes[*rules[i].UpstreamID] = true
		}
	}
	ordered := make([]string, 0, len(scopes))
	for id := range scopes {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	for _, id := range ordered {
		if rule := previewScopeRule(rules, requested, &id); rule != nil {
			return rule
		}
	}
	return previewScopeRule(rules, requested, nil)
}

// previewScopeRule returns the first enabled rule that matches requested inside
// one scope, or nil. Rules arrive in position order per scope.
func previewScopeRule(rules []previewRule, requested string, upstreamID *string) *previewRule {
	for i := range rules {
		r := &rules[i]
		if !sameScope(r.UpstreamID, upstreamID) {
			continue
		}
		if r.MatchType == "exact" && r.Pattern != requested {
			continue
		}
		if r.MatchType != "exact" && !modelglob.Match(r.Pattern, requested) {
			continue
		}
		return r
	}
	return nil
}

func sameScope(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func findPreviewModel(models []previewModel, id string) *previewModel {
	for i := range models {
		if models[i].ID == id {
			return &models[i]
		}
	}
	return nil
}

// previewPolicyAllows mirrors the core policy order: deny first, then allow,
// then the default action; an upstream without a row allows everything.
func previewPolicyAllows(policies []previewPolicy, upstreamID, modelID string) bool {
	for _, p := range policies {
		if p.UpstreamID != upstreamID {
			continue
		}
		for _, pattern := range p.DenyPatterns {
			if modelglob.Match(pattern, modelID) {
				return false
			}
		}
		for _, pattern := range p.AllowPatterns {
			if modelglob.Match(pattern, modelID) {
				return true
			}
		}
		return p.DefaultAction == "allow"
	}
	return true
}

func validRouteName(s string) bool {
	return strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= 256 && !strings.ContainsFunc(s, unicode.IsControl)
}
