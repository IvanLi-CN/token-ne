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
		_, _ = s.Pool.Exec(cleanupCtx, "delete from model_route_rules where target_model_id = any($1)", models)
		_, _ = s.Pool.Exec(cleanupCtx, "delete from upstream_model_policies where upstream_id = any($1::uuid[])", upstreams)
		_, _ = s.Pool.Exec(cleanupCtx, "delete from upstream_models where upstream_id = any($1::uuid[])", upstreams)
		_, _ = s.Pool.Exec(cleanupCtx, "delete from models where model_id = any($1)", models)
		_, _ = s.Pool.Exec(cleanupCtx, "delete from upstreams where id = any($1::uuid[])", upstreams)
	})
	for _, id := range upstreams {
		if _, e = s.Pool.Exec(ctx, "insert into upstreams(id,name,base_url,api_key_ciphertext,api_key_nonce,api_key_hint) values($1,$2,'https://example.test',$3,$4,'hint')", id, "route-upstream-"+id[:8], []byte("cipher"), make([]byte, 12)); e != nil {
			t.Fatalf("insert upstream: %v", e)
		}
	}
	if _, e = s.Pool.Exec(ctx, `with m(model_id,enabled) as (values($1,true),($2,true),($3,true),($4,false)),
r as (insert into price_revisions(id,model_id,scope,source,locked) select gen_random_uuid(),model_id,'global','manual',false from m returning model_id,id)
insert into models(model_id,enabled,billing_revision_id) select m.model_id,m.enabled,r.id from m join r using(model_id)`, modelA, modelB, modelC, modelD); e != nil {
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
			{"invalid glob", ModelRoute{Pattern: "bad-[a]", MatchType: "glob", TargetModelID: modelA, Enabled: true}, "pattern", ErrReference},
			{"missing target", ModelRoute{Pattern: "target-" + suffix, MatchType: "exact", TargetModelID: "ghost-" + suffix, Enabled: true}, "target_model_id", ErrReference},
			{"missing upstream", ModelRoute{Pattern: "upstream-" + suffix, MatchType: "exact", TargetModelID: modelA, UpstreamID: new(uuid.NewString()), Enabled: true}, "upstream_id", ErrReference},
			{"bad match type", ModelRoute{Pattern: "kind-" + suffix, MatchType: "prefix", TargetModelID: modelA, Enabled: true}, "match_type", ErrReference},
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

	t.Run("duplicate patterns are ordered, not rejected", func(t *testing.T) {
		pattern := "dup-" + suffix
		first := save(t, ModelRoute{Pattern: pattern, MatchType: "exact", TargetModelID: modelA, Enabled: true})
		second := save(t, ModelRoute{Pattern: pattern, MatchType: "exact", TargetModelID: modelB, Enabled: true})
		if second.Position != first.Position+1 {
			t.Fatalf("second create did not append: %d then %d", first.Position, second.Position)
		}
		// The lower position wins until a reorder puts the other rule first.
		preview, err := s.PreviewRoute(ctx, pattern)
		if err != nil {
			t.Fatal(err)
		}
		if preview.ResolvedModel == nil || *preview.ResolvedModel != modelA || preview.MatchedRule == nil || *preview.MatchedRule != first.ID {
			t.Fatalf("position order not applied: %+v", preview)
		}
		// Reorder submits a whole scope, so resolve the current global order first.
		rows, err := s.Routes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var global []string
		for _, row := range rows {
			if row.UpstreamID == nil {
				global = append(global, row.ID)
			}
		}
		reordered := []string{second.ID, first.ID}
		for _, id := range global {
			if id != second.ID && id != first.ID {
				reordered = append(reordered, id)
			}
		}
		if _, err = s.ReorderRoutes(ctx, nil, reordered, audit("model_route.reorder")); err != nil {
			t.Fatal(err)
		}
		preview, err = s.PreviewRoute(ctx, pattern)
		if err != nil {
			t.Fatal(err)
		}
		if preview.ResolvedModel == nil || *preview.ResolvedModel != modelB || preview.MatchedRule == nil || *preview.MatchedRule != second.ID {
			t.Fatalf("reorder did not change resolution: %+v", preview)
		}
		// Updating keeps the stored position, so an edit never jumps the queue.
		updated, err := s.SaveRoute(ctx, ModelRoute{ID: first.ID, Pattern: pattern, MatchType: "exact", TargetModelID: modelC, Enabled: true}, false, audit("model_route.update"))
		if err != nil || updated.TargetModelID != modelC {
			t.Fatalf("update existing rule: %v", err)
		}
		if updated.Position != 1 {
			t.Fatalf("update moved the rule to position %d", updated.Position)
		}
		if _, err = s.SaveRoute(ctx, ModelRoute{ID: uuid.NewString(), Pattern: "missing-" + suffix, MatchType: "exact", TargetModelID: modelA, Enabled: true}, false, audit("model_route.update")); !errors.Is(err, ErrNotFound) {
			t.Fatalf("update missing rule: %v", err)
		}
		// A pattern equal to a system model ID is a plain pattern now.
		md := save(t, ModelRoute{Pattern: modelD, MatchType: "exact", TargetModelID: modelA, Enabled: true})
		t.Logf("DEBUG saved modelD rule=%+v", md)
	})

	t.Run("position is allocated per scope", func(t *testing.T) {
		upstream := new(upstream1)
		scoped := save(t, ModelRoute{Pattern: "scope-up-" + suffix, MatchType: "exact", TargetModelID: modelA, UpstreamID: upstream, Enabled: true})
		if scoped.Position != 0 {
			t.Fatalf("first rule of a fresh scope landed at %d", scoped.Position)
		}
		scopedSecond := save(t, ModelRoute{Pattern: "scope-up-2-" + suffix, MatchType: "exact", TargetModelID: modelA, UpstreamID: upstream, Enabled: true})
		if scopedSecond.Position != 1 {
			t.Fatalf("second rule of the scope landed at %d", scopedSecond.Position)
		}
		rows, err := s.Routes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		lastScope, lastPosition := "", -1
		for _, row := range rows {
			scope := ""
			if row.UpstreamID != nil {
				scope = *row.UpstreamID
			}
			if scope < lastScope || scope == lastScope && row.Position <= lastPosition {
				t.Fatalf("routes are not ordered by scope then position: %+v", rows)
			}
			lastScope, lastPosition = scope, row.Position
		}
	})

	t.Run("reorder rewrites one scope and rejects a mismatched set", func(t *testing.T) {
		upstream := new(upstream2)
		a := save(t, ModelRoute{Pattern: "ord-a-" + suffix, MatchType: "exact", TargetModelID: modelA, UpstreamID: upstream, Enabled: true})
		b := save(t, ModelRoute{Pattern: "ord-b-" + suffix, MatchType: "exact", TargetModelID: modelB, UpstreamID: upstream, Enabled: true})
		c := save(t, ModelRoute{Pattern: "ord-c-" + suffix, MatchType: "exact", TargetModelID: modelC, UpstreamID: upstream, Enabled: true})
		ordered, err := s.ReorderRoutes(ctx, upstream, []string{c.ID, a.ID, b.ID}, audit("model_route.reorder"))
		if err != nil {
			t.Fatal(err)
		}
		if len(ordered) != 3 || ordered[0].ID != c.ID || ordered[1].ID != a.ID || ordered[2].ID != b.ID {
			t.Fatalf("reorder result = %+v", ordered)
		}
		for i, row := range ordered {
			if row.Position != i {
				t.Fatalf("position %d = %d", i, row.Position)
			}
		}
		for _, tc := range []struct {
			name  string
			scope *string
			ids   []string
		}{
			{"empty", upstream, nil},
			{"duplicate id", upstream, []string{a.ID, a.ID, b.ID}},
			{"foreign rule", upstream, []string{a.ID, b.ID, uuid.NewString()}},
			{"partial set", upstream, []string{a.ID, b.ID}},
			{"wrong scope", nil, []string{a.ID, b.ID, c.ID}},
			{"not a uuid", upstream, []string{a.ID, b.ID, "nope"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, err := s.ReorderRoutes(ctx, tc.scope, tc.ids, audit("model_route.reorder"))
				var field *FieldError
				if !errors.As(err, &field) || field.Path != "route_ids" || !errors.Is(err, ErrReference) {
					t.Fatalf("want route_ids 422, got %v", err)
				}
			})
		}
		// A rejected reorder leaves the stored order untouched.
		rows, err := s.Routes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		positions := map[string]int{}
		for _, row := range rows {
			positions[row.ID] = row.Position
		}
		if positions[c.ID] != 0 || positions[a.ID] != 1 || positions[b.ID] != 2 {
			t.Fatalf("failed reorder mutated positions: %v", positions)
		}
	})

	t.Run("preview resolution and candidates", func(t *testing.T) {
		exact := save(t, ModelRoute{Pattern: "alias-" + suffix, MatchType: "exact", TargetModelID: modelA, Enabled: true})
		glob := save(t, ModelRoute{Pattern: "alias-" + suffix + "-*", MatchType: "glob", TargetModelID: modelB, Enabled: true})
		_ = glob

		preview, err := s.PreviewRoute(ctx, "alias-"+suffix)
		if err != nil {
			t.Fatal(err)
		}
		if preview.ResolvedModel == nil || *preview.ResolvedModel != modelA || preview.MatchedRule == nil || *preview.MatchedRule != exact.ID ||
			preview.RuleKind == nil || *preview.RuleKind != "exact" {
			t.Fatalf("exact rule did not resolve: %+v", preview)
		}
		// Both upstreams bind modelA to their own vendor/a, so both are candidates;
		// order follows upstream_id, which is a generated UUID and not asserted.
		served := map[string]string{}
		for _, candidate := range preview.Candidates {
			served[candidate.UpstreamID] = candidate.UpstreamModelID
			if !candidate.Allowed {
				t.Fatalf("candidate unexpectedly denied: %+v", candidate)
			}
		}
		if len(served) != 2 || served[upstream1] != "vendor/a" || served[upstream2] != "vendor/a" {
			t.Fatalf("candidate upstream model ids: %+v", preview.Candidates)
		}

		preview, err = s.PreviewRoute(ctx, "alias-"+suffix+"-1")
		if err != nil {
			t.Fatal(err)
		}
		if preview.ResolvedModel == nil || *preview.ResolvedModel != modelB || preview.RuleKind == nil || *preview.RuleKind != "glob" {
			t.Fatalf("glob rule resolution: %+v", preview)
		}

		// A system model ID no longer short-circuits routing: with no matching
		// rule the name simply stays unresolved, even though the model exists.
		preview, err = s.PreviewRoute(ctx, modelC)
		if err != nil {
			t.Fatal(err)
		}
		if preview.ResolvedModel != nil || preview.MatchedRule != nil || len(preview.Candidates) != 0 {
			t.Fatalf("unrouted system model ID resolved: %+v", preview)
		}

		// A pattern equal to a system model ID resolves through its rule.
		if all, e := s.Routes(ctx); e == nil {
			for _, r := range all {
				scope := "global"
				if r.UpstreamID != nil {
					scope = *r.UpstreamID
				}
				t.Logf("DEBUG rule %s %s pos=%d scope=%s target=%s enabled=%v", r.Pattern, r.MatchType, r.Position, scope, r.TargetModelID, r.Enabled)
			}
		}
		preview, err = s.PreviewRoute(ctx, modelD)
		if err != nil {
			t.Fatal(err)
		}
		if preview.ResolvedModel == nil || *preview.ResolvedModel != modelA || preview.RuleKind == nil || *preview.RuleKind != "exact" {
			t.Fatalf("rule matching a system model ID: %+v", preview)
		}

		// An upstream-scoped rule only affects that upstream's candidates.
		restricted := save(t, ModelRoute{Pattern: "restrict-" + suffix, MatchType: "exact", TargetModelID: modelA, UpstreamID: new(upstream1), Enabled: true})
		_ = restricted
		preview, err = s.PreviewRoute(ctx, "restrict-"+suffix)
		if err != nil {
			t.Fatal(err)
		}
		if preview.ResolvedModel == nil || *preview.ResolvedModel != modelA || len(preview.Candidates) != 1 || preview.Candidates[0].UpstreamID != upstream1 {
			t.Fatalf("upstream restriction not applied: %+v", preview)
		}

		// A scoped rule that misses falls back to the global scope per upstream.
		global := save(t, ModelRoute{Pattern: "fallback-" + suffix, MatchType: "exact", TargetModelID: modelB, Enabled: true})
		save(t, ModelRoute{Pattern: "fallback-other-" + suffix, MatchType: "exact", TargetModelID: modelA, UpstreamID: new(upstream1), Enabled: true})
		_ = global
		preview, err = s.PreviewRoute(ctx, "fallback-"+suffix)
		if err != nil {
			t.Fatal(err)
		}
		// Only upstream2 binds modelB (vendor/b); upstream1 misses its own scope and
		// then the global rule, but has no binding for modelB, so it offers nothing.
		if preview.ResolvedModel == nil || *preview.ResolvedModel != modelB || len(preview.Candidates) != 1 ||
			preview.Candidates[0].UpstreamID != upstream2 || preview.Candidates[0].UpstreamModelID != "vendor/b" {
			t.Fatalf("global fallback per upstream: %+v", preview)
		}
	})

	t.Run("preview unresolved shape", func(t *testing.T) {
		save(t, ModelRoute{Pattern: "disabled-target-" + suffix, MatchType: "exact", TargetModelID: modelD, Enabled: true})
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
		saved := save(t, ModelRoute{Pattern: "delete-" + suffix, MatchType: "exact", TargetModelID: modelC, Enabled: true})
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
