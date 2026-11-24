package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestUpstreamPoliciesIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s, e := Open(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.Close)

	suffix := uuid.NewString()[:8]
	modelA, modelX := "p2p-a-"+suffix, "p2p-x-"+suffix
	upstreamID := uuid.NewString()
	models := []string{modelA, modelX}
	upstreams := []string{upstreamID}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		for _, step := range []struct {
			statement string
			arg       any
		}{
			{"delete from model_route_rules where target_model_id = any($1::text[])", models},
			{"delete from upstream_model_policies where upstream_id = any($1::uuid[])", upstreams},
			{"delete from upstream_models where upstream_id = any($1::uuid[])", upstreams},
			{"delete from models where model_id = any($1::text[])", models},
			{"delete from upstreams where id = any($1::uuid[])", upstreams},
		} {
			if _, execErr := s.Pool.Exec(cleanupCtx, step.statement, step.arg); execErr != nil {
				t.Logf("cleanup %q: %v", step.statement, execErr)
			}
		}
	})
	if _, e = s.Pool.Exec(ctx, "insert into upstreams(id,name,base_url,api_key_ciphertext,api_key_nonce,api_key_hint) values($1,$2,'https://example.test',$3,$4,'hint')", upstreamID, "policy-upstream-"+suffix, []byte("cipher"), make([]byte, 12)); e != nil {
		t.Fatalf("insert upstream: %v", e)
	}
	if _, e = s.Pool.Exec(ctx, "insert into models(model_id,enabled) values($1,true),($2,true)", modelA, modelX); e != nil {
		t.Fatalf("insert models: %v", e)
	}
	if _, e = s.Pool.Exec(ctx, "insert into upstream_models(upstream_id,model_id,upstream_model_name,enabled,priority,weight) values($1,$2,'vendor/a',true,10,100)", upstreamID, modelA); e != nil {
		t.Fatalf("insert capability: %v", e)
	}
	audit := func(action string) Audit { return Audit{Actor: "tester", Action: action, TargetType: "upstream"} }
	save := func(t *testing.T, p UpstreamPolicy) UpstreamPolicy {
		t.Helper()
		saved, err := s.SavePolicy(ctx, upstreamID, p, audit("upstream_policy.replace"))
		if err != nil {
			t.Fatalf("save policy %+v: %v", p, err)
		}
		return saved
	}

	t.Run("missing row means allow", func(t *testing.T) {
		p, err := s.Policy(ctx, upstreamID)
		if err != nil {
			t.Fatal(err)
		}
		if p.UpstreamID != upstreamID || p.DefaultAction != "allow" || p.AllowPatterns == nil || p.DenyPatterns == nil ||
			len(p.AllowPatterns) != 0 || len(p.DenyPatterns) != 0 || p.Updated != nil {
			t.Fatalf("implicit policy: %+v", p)
		}
		if _, err = s.Policy(ctx, uuid.NewString()); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unknown upstream: %v", err)
		}
	})

	t.Run("validation", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			policy UpstreamPolicy
			path   string
		}{
			{"action", UpstreamPolicy{DefaultAction: "maybe"}, "default_action"},
			{"allow glob", UpstreamPolicy{DefaultAction: "allow", AllowPatterns: []string{"ok", "bad-["}}, "allow_patterns[1]"},
			{"deny glob", UpstreamPolicy{DefaultAction: "deny", DenyPatterns: []string{"bad-{"}}, "deny_patterns[0]"},
			{"duplicate", UpstreamPolicy{DefaultAction: "allow", DenyPatterns: []string{"dup", "dup"}}, "deny_patterns[1]"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, err := s.SavePolicy(ctx, upstreamID, tc.policy, audit("upstream_policy.replace"))
				var field *FieldError
				if !errors.As(err, &field) || field.Path != tc.path || !errors.Is(err, ErrReference) {
					t.Fatalf("want 422 at %s, got %v", tc.path, err)
				}
			})
		}
		if _, err := s.SavePolicy(ctx, uuid.NewString(), UpstreamPolicy{DefaultAction: "allow"}, audit("upstream_policy.replace")); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unknown upstream save: %v", err)
		}
	})

	t.Run("policy filters preview and deny wins", func(t *testing.T) {
		route, err := s.SaveRoute(ctx, ModelRoute{Pattern: "policy-" + suffix, MatchType: "exact", TargetModelID: modelA, Priority: 10, Enabled: true}, true, Audit{Actor: "tester", Action: "model_route.create", TargetType: "model_route"})
		if err != nil {
			t.Fatal(err)
		}
		preview, err := s.PreviewRoute(ctx, "policy-"+suffix)
		if err != nil {
			t.Fatal(err)
		}
		if len(preview.Candidates) != 1 || !preview.Candidates[0].Allowed || preview.Candidates[0].Reason != nil {
			t.Fatalf("no policy must allow: %+v", preview.Candidates)
		}

		saved := save(t, UpstreamPolicy{DefaultAction: "allow", AllowPatterns: []string{modelA}, DenyPatterns: []string{"p2p-a-*"}})
		if saved.UpstreamID != upstreamID || saved.Updated == nil {
			t.Fatalf("saved policy: %+v", saved)
		}
		preview, err = s.PreviewRoute(ctx, "policy-"+suffix)
		if err != nil {
			t.Fatal(err)
		}
		if len(preview.Candidates) != 1 || preview.Candidates[0].Allowed || preview.Candidates[0].Reason == nil || *preview.Candidates[0].Reason != "policy_denied" {
			t.Fatalf("deny must win over allow: %+v", preview.Candidates)
		}

		save(t, UpstreamPolicy{DefaultAction: "deny", AllowPatterns: []string{modelA}, DenyPatterns: []string{}})
		preview, err = s.PreviewRoute(ctx, "policy-"+suffix)
		if err != nil {
			t.Fatal(err)
		}
		if len(preview.Candidates) != 1 || !preview.Candidates[0].Allowed {
			t.Fatalf("allow pattern must override default deny: %+v", preview.Candidates)
		}

		saved = save(t, UpstreamPolicy{DefaultAction: "deny", AllowPatterns: []string{}})
		preview, err = s.PreviewRoute(ctx, "policy-"+suffix)
		if err != nil {
			t.Fatal(err)
		}
		if len(preview.Candidates) != 1 || preview.Candidates[0].Allowed {
			t.Fatalf("default deny must filter the capability: %+v", preview.Candidates)
		}
		if len(saved.AllowPatterns) != 0 || len(saved.DenyPatterns) != 0 {
			t.Fatalf("replace merged lists: %+v", saved)
		}
		if route.ID == "" {
			t.Fatal("missing route ID")
		}
	})

	t.Run("policy never creates capabilities", func(t *testing.T) {
		// modelX has no capability on this upstream; granting it in the policy
		// must leave the routing candidate set and the capability table unchanged.
		save(t, UpstreamPolicy{DefaultAction: "allow", AllowPatterns: []string{"*"}})
		var count int
		if e := s.Pool.QueryRow(ctx, "select count(*) from upstream_models where upstream_id=$1", upstreamID).Scan(&count); e != nil {
			t.Fatal(e)
		}
		if count != 1 {
			t.Fatalf("policy created a capability: %d rows", count)
		}
		if _, err := s.SaveRoute(ctx, ModelRoute{Pattern: "policy-x-" + suffix, MatchType: "exact", TargetModelID: modelX, Priority: 10, Enabled: true}, true, Audit{Actor: "tester", Action: "model_route.create", TargetType: "model_route"}); err != nil {
			t.Fatal(err)
		}
		preview, err := s.PreviewRoute(ctx, "policy-x-"+suffix)
		if err != nil {
			t.Fatal(err)
		}
		if preview.ResolvedModel == nil || *preview.ResolvedModel != modelX || len(preview.Candidates) != 0 {
			t.Fatalf("policy granted a non-existent capability: %+v", preview)
		}

		rows, err := s.Audits(ctx, nil, 20)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, row := range rows {
			if row.Action == "upstream_policy.replace" && row.Actor == "tester" && row.TargetType == "upstream" {
				found = true
			}
		}
		if !found {
			t.Fatal("policy replacement did not write an audit fact")
		}
	})
}
