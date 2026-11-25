package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/modelglob"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// UpstreamPolicy is the stored allow/deny filter for one upstream. A missing
// row means default_action=allow with empty lists, never a 404.
type UpstreamPolicy struct {
	UpstreamID    string     `json:"upstream_id"`
	DefaultAction string     `json:"default_action"`
	AllowPatterns []string   `json:"allow_patterns"`
	DenyPatterns  []string   `json:"deny_patterns"`
	Updated       *time.Time `json:"updated_at,omitempty"`
}

const policyColumns = "upstream_id::text,default_action,allow_patterns,deny_patterns,updated_at"

func scanPolicy(row pgx.Row) (UpstreamPolicy, error) {
	var p UpstreamPolicy
	var allow, deny []byte
	if e := row.Scan(&p.UpstreamID, &p.DefaultAction, &allow, &deny, &p.Updated); e != nil {
		return p, normalize(e)
	}
	p.AllowPatterns, p.DenyPatterns = []string{}, []string{}
	if e := json.Unmarshal(allow, &p.AllowPatterns); e != nil {
		return p, e
	}
	if e := json.Unmarshal(deny, &p.DenyPatterns); e != nil {
		return p, e
	}
	return p, nil
}

// Policy returns the stored policy or the implicit allow policy when the
// upstream exists but has no row. Unknown upstreams return ErrNotFound.
func (s *Store) Policy(ctx context.Context, upstreamID string) (UpstreamPolicy, error) {
	if _, e := uuid.Parse(upstreamID); e != nil {
		return UpstreamPolicy{}, ErrNotFound
	}
	return scanPolicy(s.Pool.QueryRow(ctx, `select u.id::text,coalesce(p.default_action,'allow'),coalesce(p.allow_patterns,'[]'::jsonb),coalesce(p.deny_patterns,'[]'::jsonb),p.updated_at
 from upstreams u left join upstream_model_policies p on p.upstream_id=u.id where u.id=$1`, upstreamID))
}

// SavePolicy atomically replaces the upstream policy. Patterns only filter
// existing capabilities; saving a policy never creates or enables one.
func (s *Store) SavePolicy(ctx context.Context, upstreamID string, p UpstreamPolicy, a Audit) (UpstreamPolicy, error) {
	if _, e := uuid.Parse(upstreamID); e != nil {
		return UpstreamPolicy{}, ErrNotFound
	}
	if p.AllowPatterns == nil {
		p.AllowPatterns = []string{}
	}
	if p.DenyPatterns == nil {
		p.DenyPatterns = []string{}
	}
	if e := validatePolicy(p); e != nil {
		return UpstreamPolicy{}, e
	}
	p.UpstreamID = upstreamID
	allow, e := json.Marshal(p.AllowPatterns)
	if e != nil {
		return UpstreamPolicy{}, e
	}
	deny, e := json.Marshal(p.DenyPatterns)
	if e != nil {
		return UpstreamPolicy{}, e
	}
	var saved UpstreamPolicy
	e = s.mutate(ctx, a, func(tx pgx.Tx) error {
		var exists bool
		if e := tx.QueryRow(ctx, "select exists(select 1 from upstreams where id=$1)", upstreamID).Scan(&exists); e != nil {
			return e
		}
		if !exists {
			return ErrNotFound
		}
		var err error
		saved, err = scanPolicy(tx.QueryRow(ctx, `insert into upstream_model_policies(upstream_id,default_action,allow_patterns,deny_patterns)
 values($1,$2,$3::jsonb,$4::jsonb)
 on conflict (upstream_id) do update set default_action=excluded.default_action,allow_patterns=excluded.allow_patterns,deny_patterns=excluded.deny_patterns,updated_at=now()
 returning `+policyColumns, upstreamID, p.DefaultAction, allow, deny))
		return err
	})
	if errors.Is(e, ErrReference) {
		// The upstream disappeared between the existence check and the upsert.
		return UpstreamPolicy{}, ErrNotFound
	}
	return saved, e
}

func validatePolicy(p UpstreamPolicy) error {
	if p.DefaultAction != "allow" && p.DefaultAction != "deny" {
		return &FieldError{Path: "default_action", Reason: "must be allow or deny", Kind: ErrReference}
	}
	for _, list := range []struct {
		field    string
		patterns []string
	}{{"allow_patterns", p.AllowPatterns}, {"deny_patterns", p.DenyPatterns}} {
		seen := map[string]bool{}
		for i, pattern := range list.patterns {
			path := list.field + "[" + strconv.Itoa(i) + "]"
			if e := modelglob.Validate(pattern); e != nil {
				return &FieldError{Path: path, Reason: "must be a valid restricted glob", Kind: ErrReference}
			}
			if seen[pattern] {
				return &FieldError{Path: path, Reason: "must not repeat a pattern", Kind: ErrReference}
			}
			seen[pattern] = true
		}
	}
	return nil
}
