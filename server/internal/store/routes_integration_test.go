package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRouteRulesIntegration(t *testing.T) {
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
	modelA, modelB, modelC, modelD := "p2r-a-"+suffix, "p2r-b-"+suffix, "p2r-c-"+suffix, "p2r-d-"+suffix
	upstream1, upstream2 := uuid.NewString(), uuid.NewString()
	models := []string{modelA, modelB, modelC, modelD}
	upstreams := []string{upstream1, upstream2}
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
	for _, id := range upstreams {
		if _, e = s.Pool.Exec(ctx, "insert into upstreams(id,name,base_url,api_key_ciphertext,api_key_nonce,api_key_hint) values($1,$2,'https://example.test',$3,$4,'hint')", id, "route-upstream-"+id[:8], []byte("cipher"), make([]byte, 12)); e != nil {
			t.Fatalf("insert upstream: %v", e)
		}
	}
	if _, e = s.Pool.Exec(ctx, `with m(model_id,enabled) as (values($1,true),($2,true),($3,true),($4,false)),
r as (insert into price_revisions(id,model_id,scope,source,locked) select gen_random_uuid(),model_id,'global','manual',false from m returning model_id,id)
insert into models(model_id,enabled,default_upstream_model,billing_revision_id) select m.model_id,m.enabled,null,r.id from m join r using(model_id)`, modelA, modelB, modelC, modelD); e != nil {
		t.Fatalf("insert models: %v", e)
	}
	if _, e = s.Pool.Exec(ctx, `insert into upstream_models(upstream_id,upstream_model_id,model_id,enabled) values
 ($1,'vendor/a',$2,true),($3,'vendor/a',$2,true),($3,'vendor/b',$4,true)`, upstream1, modelA, upstream2, modelB); e != nil {
		t.Fatalf("insert capabilities: %v", e)
	}
	audit := func(action string) Audit { return Audit{Actor: "tester", Action: action, TargetType: "model_route"} }
	save := func(t *testing.T, r ModelRoute) ModelRoute {
		t.Helper()
		saved, err := s.SaveRoute(ctx, r, true, audit("model_route.create"))
		if err != nil {
			t.Fatalf("save route %+v: %v", r, err)
		}
		return saved
	}

	t.Run("validation matrix", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			route ModelRoute
			path  string
			kind  error
		}{
			{"invalid glob", ModelRoute{Pattern: "bad-[a]", MatchType: "glob", TargetModelID: modelA, Priority: 10, Enabled: true}, "pattern", ErrReference},
			{"reserved model id", ModelRoute{Pattern: modelA, MatchType: "exact", TargetModelID: modelA, Priority: 10, Enabled: true}, "pattern", ErrConflict},
			{"reserved model id via glob", ModelRoute{Pattern: modelA, MatchType: "glob", TargetModelID: modelA, Priority: 10, Enabled: true}, "pattern", ErrConflict},
			{"missing target", ModelRoute{Pattern: "target-" + suffix, MatchType: "exact", TargetModelID: "ghost-" + suffix, Priority: 10, Enabled: true}, "target_model_id", ErrReference},
			{"missing upstream", ModelRoute{Pattern: "upstream-" + suffix, MatchType: "exact", TargetModelID: modelA, UpstreamID: new(uuid.NewString()), Priority: 10, Enabled: true}, "upstream_id", ErrReference},
			{"bad match type", ModelRoute{Pattern: "kind-" + suffix, MatchType: "prefix", TargetModelID: modelA, Priority: 10, Enabled: true}, "match_type", ErrReference},
			{"priority range", ModelRoute{Pattern: "prio-" + suffix, MatchType: "exact", TargetModelID: modelA, Priority: 100001, Enabled: true}, "priority", ErrReference},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, err := s.SaveRoute(ctx, tc.route, true, audit("model_route.create"))
				var field *FieldError
				if !errors.As(err, &field) || field.Path != tc.path || !errors.Is(err, tc.kind) {
					t.Fatalf("want %s at %s, got %v", tc.kind, tc.path, err)
				}
			})
		}
	})

	t.Run("exact uniqueness", func(t *testing.T) {
		pattern := "dup-" + suffix
		first := save(t, ModelRoute{Pattern: pattern, MatchType: "exact", TargetModelID: modelA, Priority: 10, Enabled: true})
		_, err := s.SaveRoute(ctx, ModelRoute{Pattern: pattern, MatchType: "exact", TargetModelID: modelA, Priority: 11, Enabled: true}, true, audit("model_route.create"))
		var field *FieldError
		if !errors.As(err, &field) || field.Path != "pattern" || !errors.Is(err, ErrConflict) {
			t.Fatalf("duplicate enabled exact: %v", err)
		}
		disabled := save(t, ModelRoute{Pattern: pattern, MatchType: "exact", TargetModelID: modelA, Priority: 12, Enabled: false})
		if disabled.ID == "" {
			t.Fatal("disabled duplicate was not stored")
		}
		updated, err := s.SaveRoute(ctx, ModelRoute{ID: first.ID, Pattern: pattern, MatchType: "exact", TargetModelID: modelB, Priority: 10, Enabled: true}, false, audit("model_route.update"))
		if err != nil || updated.TargetModelID != modelB {
			t.Fatalf("update existing rule: %v", err)
		}
		_, err = s.SaveRoute(ctx, ModelRoute{ID: uuid.NewString(), Pattern: "missing-" + suffix, MatchType: "exact", TargetModelID: modelA, Priority: 10, Enabled: true}, false, audit("model_route.update"))
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("update missing rule: %v", err)
		}
	})

	t.Run("glob overlap", func(t *testing.T) {
		save(t, ModelRoute{Pattern: "ov-*", MatchType: "glob", TargetModelID: modelA, Priority: 50, Enabled: true})
		save(t, ModelRoute{Pattern: "ov-1*", MatchType: "glob", TargetModelID: modelA, Priority: 50, Enabled: true})
		_, err := s.SaveRoute(ctx, ModelRoute{Pattern: "ov-2*", MatchType: "glob", TargetModelID: modelB, Priority: 50, Enabled: true}, true, audit("model_route.create"))
		var field *FieldError
		if !errors.As(err, &field) || field.Path != "pattern" || !errors.Is(err, ErrConflict) {
			t.Fatalf("different target overlap: %v", err)
		}
		_, err = s.SaveRoute(ctx, ModelRoute{Pattern: "ov-3*", MatchType: "glob", TargetModelID: modelA, UpstreamID: new(upstream2), Priority: 50, Enabled: true}, true, audit("model_route.create"))
		if !errors.As(err, &field) || !errors.Is(err, ErrConflict) {
			t.Fatalf("different upstream overlap: %v", err)
		}
		if _, err = s.SaveRoute(ctx, ModelRoute{Pattern: "ov-*", MatchType: "glob", TargetModelID: modelB, Priority: 60, Enabled: true}, true, audit("model_route.create")); err != nil {
			t.Fatalf("overlap at another priority must be allowed: %v", err)
		}
		if _, err = s.SaveRoute(ctx, ModelRoute{Pattern: "ov-9*", MatchType: "glob", TargetModelID: modelB, Priority: 50, Enabled: false}, true, audit("model_route.create")); err != nil {
			t.Fatalf("disabled overlap must be allowed: %v", err)
		}
	})

	t.Run("list ordering", func(t *testing.T) {
		rows, err := s.Routes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for i := 1; i < len(rows); i++ {
			if rows[i-1].Priority > rows[i].Priority {
				t.Fatalf("routes not ordered by priority: %+v", rows)
			}
		}
	})

	t.Run("preview resolution and candidates", func(t *testing.T) {
		exact := save(t, ModelRoute{Pattern: "alias-" + suffix, MatchType: "exact", TargetModelID: modelA, Priority: 500, Enabled: true})
		glob := save(t, ModelRoute{Pattern: "alias-" + suffix + "-*", MatchType: "glob", TargetModelID: modelB, Priority: 1, Enabled: true})
		_ = glob

		preview, err := s.PreviewRoute(ctx, "alias-"+suffix)
		if err != nil {
			t.Fatal(err)
		}
		if preview.ResolvedModel == nil || *preview.ResolvedModel != modelA || preview.MatchedRule == nil || *preview.MatchedRule != exact.ID ||
			preview.RuleKind == nil || *preview.RuleKind != "exact" {
			t.Fatalf("exact rule must win over a higher-ranked glob: %+v", preview)
		}
		if len(preview.Candidates) != 2 || preview.Candidates[0].UpstreamID != upstream1 ||
			preview.Candidates[0].UpstreamModelID != "vendor/a" || preview.Candidates[0].Allowed != true ||
			preview.Candidates[1].UpstreamModelID != "vendor/b" {
			t.Fatalf("candidate order or effective names: %+v", preview.Candidates)
		}

		preview, err = s.PreviewRoute(ctx, "alias-"+suffix+"-1")
		if err != nil {
			t.Fatal(err)
		}
		if preview.ResolvedModel == nil || *preview.ResolvedModel != modelB || preview.RuleKind == nil || *preview.RuleKind != "glob" {
			t.Fatalf("glob rule resolution: %+v", preview)
		}

		preview, err = s.PreviewRoute(ctx, modelA)
		if err != nil {
			t.Fatal(err)
		}
		if preview.ResolvedModel == nil || *preview.ResolvedModel != modelA || preview.MatchedRule != nil || preview.RuleKind != nil {
			t.Fatalf("global ID must resolve without a rule: %+v", preview)
		}

		restricted := save(t, ModelRoute{Pattern: "restrict-" + suffix, MatchType: "exact", TargetModelID: modelA, UpstreamID: new(upstream1), Priority: 10, Enabled: true})
		_ = restricted
		preview, err = s.PreviewRoute(ctx, "restrict-"+suffix)
		if err != nil {
			t.Fatal(err)
		}
		if len(preview.Candidates) != 1 || preview.Candidates[0].UpstreamID != upstream1 {
			t.Fatalf("upstream restriction not applied: %+v", preview.Candidates)
		}
	})

	t.Run("preview unresolved shape", func(t *testing.T) {
		if _, err := s.SaveRoute(ctx, ModelRoute{Pattern: modelD, MatchType: "glob", TargetModelID: modelA, Priority: 70, Enabled: true}, true, audit("model_route.create")); !errors.Is(err, ErrConflict) {
			t.Fatalf("glob equal to a model ID must be reserved even when disabled target: %v", err)
		}
		save(t, ModelRoute{Pattern: "disabled-target-" + suffix, MatchType: "exact", TargetModelID: modelD, Priority: 10, Enabled: true})
		preview, err := s.PreviewRoute(ctx, "disabled-target-"+suffix)
		if err != nil {
			t.Fatal(err)
		}
		if preview.ResolvedModel != nil || preview.MatchedRule != nil || preview.RuleKind != nil || preview.Candidates == nil || len(preview.Candidates) != 0 {
			t.Fatalf("disabled target must stay unresolved: %+v", preview)
		}
		preview, err = s.PreviewRoute(ctx, "nobody-"+suffix)
		if err != nil {
			t.Fatal(err)
		}
		if preview.ResolvedModel != nil || preview.MatchedRule != nil || preview.RuleKind != nil || len(preview.Candidates) != 0 {
			t.Fatalf("unknown name must stay unresolved: %+v", preview)
		}
		if _, err = s.PreviewRoute(ctx, "  "); !errors.Is(err, ErrReference) {
			t.Fatalf("blank requested model: %v", err)
		}
	})

	t.Run("delete and audit", func(t *testing.T) {
		saved := save(t, ModelRoute{Pattern: "delete-" + suffix, MatchType: "exact", TargetModelID: modelC, Priority: 10, Enabled: true})
		if err := s.DeleteRoute(ctx, saved.ID, audit("model_route.delete")); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteRoute(ctx, saved.ID, audit("model_route.delete")); !errors.Is(err, ErrNotFound) {
			t.Fatalf("double delete: %v", err)
		}
		rows, err := s.Audits(ctx, nil, 20)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, row := range rows {
			if row.Action == "model_route.delete" && row.Actor == "tester" && row.TargetType == "model_route" {
				found = true
			}
		}
		if !found {
			t.Fatal("route mutation did not write an audit fact")
		}
	})
}
