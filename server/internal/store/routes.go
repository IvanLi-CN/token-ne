package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/IvanLi-CN/token-ne/server/internal/modelglob"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// routeCatalogLock serializes route-set mutations. The per-pattern advisory
// lock only serializes writers that reserve the same name; Glob overlap is a
// property of a pair of different patterns, so the catalog lock keeps the
// overlap check and the insert/update in one critical section.
const routeCatalogLock = 4927816043

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

// ModelRoute is one exact or Glob rule resolving a requested name to a global
// model. The JSON shape is the management API contract; the configuration
// snapshot uses its own projection.
type ModelRoute struct {
	ID            string    `json:"id"`
	Pattern       string    `json:"pattern"`
	MatchType     string    `json:"match_type"`
	TargetModelID string    `json:"target_model_id"`
	UpstreamID    *string   `json:"upstream_id"`
	Priority      int       `json:"priority"`
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

const routeColumns = "id::text,pattern,match_type,target_model_id,upstream_id::text,priority,enabled,description,created_at,updated_at"

func scanRoute(row pgx.Row) (ModelRoute, error) {
	var r ModelRoute
	e := row.Scan(&r.ID, &r.Pattern, &r.MatchType, &r.TargetModelID, &r.UpstreamID, &r.Priority, &r.Enabled, &r.Description, &r.Created, &r.Updated)
	return r, normalize(e)
}

func (s *Store) Routes(ctx context.Context) ([]ModelRoute, error) {
	rows, e := s.Pool.Query(ctx, "select "+routeColumns+" from model_route_rules order by priority,id")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
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

func (s *Store) SaveRoute(ctx context.Context, route ModelRoute, create bool, a Audit) (ModelRoute, error) {
	if route.ID == "" {
		route.ID = uuid.NewString()
	}
	e := s.mutate(ctx, a, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, "select pg_advisory_xact_lock($1)", routeCatalogLock); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, "select pg_advisory_xact_lock($1)", modelglob.AdvisoryKey(route.Pattern)); e != nil {
			return e
		}
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
		query := "update model_route_rules set pattern=$2,match_type=$3,target_model_id=$4,upstream_id=$5,priority=$6,enabled=$7,description=$8,updated_at=now() where id=$1 returning " + routeColumns
		if create {
			query = "insert into model_route_rules(id,pattern,match_type,target_model_id,upstream_id,priority,enabled,description) values($1,$2,$3,$4,$5,$6,$7,$8) returning " + routeColumns
		}
		var err error
		route, err = scanRoute(tx.QueryRow(ctx, query, route.ID, route.Pattern, route.MatchType, route.TargetModelID, upstream, route.Priority, route.Enabled, description))
		return err
	})
	if e != nil {
		var field *FieldError
		if errors.Is(e, ErrConflict) && !errors.As(e, &field) {
			// A concurrent writer reserved the same name between validation and
			// insert; the unique index is the final guard and still carries a path.
			return ModelRoute{}, &FieldError{Path: "pattern", Reason: "conflicts with an existing route rule", Kind: ErrConflict}
		}
		return ModelRoute{}, e
	}
	return route, nil
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
	if r.Priority < 0 || r.Priority > 100000 {
		return &FieldError{Path: "priority", Reason: "must be between 0 and 100000", Kind: ErrReference}
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

// validateRouteState checks the cross-row invariants that a table constraint
// cannot express: the model-ID namespace, dangling references, unique enabled
// exact patterns and same-priority Glob overlap.
func validateRouteState(ctx context.Context, tx pgx.Tx, r ModelRoute) error {
	var exists bool
	if e := tx.QueryRow(ctx, "select exists(select 1 from models where model_id=$1)", r.Pattern).Scan(&exists); e != nil {
		return e
	}
	if exists {
		return &FieldError{Path: "pattern", Reason: "pattern is reserved by a global model ID", Kind: ErrConflict}
	}
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
	if !r.Enabled {
		return nil
	}
	if r.MatchType == "exact" {
		var conflict bool
		if e := tx.QueryRow(ctx, "select exists(select 1 from model_route_rules where pattern=$1 and match_type='exact' and enabled and id<>$2)", r.Pattern, r.ID).Scan(&conflict); e != nil {
			return e
		}
		if conflict {
			return &FieldError{Path: "pattern", Reason: "an enabled exact rule already uses this pattern", Kind: ErrConflict}
		}
		return nil
	}
	rows, e := tx.Query(ctx, "select pattern,target_model_id,coalesce(upstream_id::text,'') from model_route_rules where match_type='glob' and enabled and priority=$1 and id<>$2", r.Priority, r.ID)
	if e != nil {
		return e
	}
	defer rows.Close()
	own := ""
	if r.UpstreamID != nil {
		own = *r.UpstreamID
	}
	for rows.Next() {
		var pattern, target, upstream string
		if e := rows.Scan(&pattern, &target, &upstream); e != nil {
			return e
		}
		overlap, e := modelglob.Overlaps(r.Pattern, pattern)
		if e != nil {
			return e
		}
		if overlap && (target != r.TargetModelID || upstream != own) {
			return &FieldError{Path: "pattern", Reason: "overlaps an enabled glob rule with the same priority but a different target or upstream", Kind: ErrConflict}
		}
	}
	return rows.Err()
}

type previewModel struct {
	ID      string  `json:"id"`
	Enabled bool    `json:"enabled"`
	Default *string `json:"default_upstream_model"`
}

type previewRule struct {
	ID            string  `json:"id"`
	Pattern       string  `json:"pattern"`
	MatchType     string  `json:"match_type"`
	TargetModelID string  `json:"target_model_id"`
	UpstreamID    *string `json:"upstream_id"`
	Priority      int     `json:"priority"`
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

// PreviewRoute resolves requested exactly like the core: global model ID,
// then the highest-priority enabled exact rule, then the highest-priority
// enabled Glob rule. Unresolvable names still return a zero resolution.
func (s *Store) PreviewRoute(ctx context.Context, requested string) (RoutePreview, error) {
	if !validRouteName(requested) {
		return RoutePreview{}, &FieldError{Path: "requested_model", Reason: "must contain 1 to 256 characters", Kind: ErrReference}
	}
	var modelsRaw, rulesRaw, capabilitiesRaw, policiesRaw []byte
	e := s.Pool.QueryRow(ctx, `select
 coalesce((select jsonb_agg(jsonb_build_object('id',model_id,'enabled',enabled,'default_upstream_model',default_upstream_model) order by model_id) from models),'[]'::jsonb),
 coalesce((select jsonb_agg(jsonb_build_object('id',id::text,'pattern',pattern,'match_type',match_type,'target_model_id',target_model_id,'upstream_id',upstream_id::text,'priority',priority) order by priority,id) from model_route_rules where enabled),'[]'::jsonb),
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
	target := findPreviewModel(models, requested)
	var rule *previewRule
	if target != nil {
		// A reserved global ID never falls through to Glob rules, even when the
		// model is disabled.
		if !target.Enabled {
			return preview, nil
		}
	} else {
		for i := range rules {
			if rules[i].MatchType == "exact" && rules[i].Pattern == requested {
				rule = &rules[i]
				break
			}
		}
		if rule == nil {
			for i := range rules {
				if rules[i].MatchType == "glob" && modelglob.Match(rules[i].Pattern, requested) {
					rule = &rules[i]
					break
				}
			}
		}
		if rule == nil {
			return preview, nil
		}
		target = findPreviewModel(models, rule.TargetModelID)
		if target == nil || !target.Enabled {
			return preview, nil
		}
	}
	preview.ResolvedModel = &target.ID
	if rule != nil {
		preview.MatchedRule = &rule.ID
		kind := rule.MatchType
		preview.RuleKind = &kind
	}
	for _, c := range capabilities {
		if c.ModelID != target.ID || rule != nil && rule.UpstreamID != nil && *rule.UpstreamID != c.UpstreamID {
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
