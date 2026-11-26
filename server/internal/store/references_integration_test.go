package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestDeleteReportsReferencesIntegration proves a delete is refused with every
// blocking reference instead of a bare foreign-key failure, and that removing
// those references unblocks it.
func TestDeleteReportsReferencesIntegration(t *testing.T) {
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
	defer s.Close()
	suffix := uuid.NewString()[:8]
	modelID := "ref-model-" + suffix
	upstreamA := uuid.NewString()
	upstreamB := uuid.NewString()
	routeID := uuid.NewString()
	audit := Audit{Actor: "tester", Action: "delete", Payload: []byte(`{}`)}
	cleanup := func() {
		for _, statement := range []string{
			"delete from model_route_rules where id='" + routeID + "'",
			"delete from model_prices where model_id='" + modelID + "'",
			"delete from upstream_models where model_id='" + modelID + "'",
			"delete from models where model_id='" + modelID + "'",
			"delete from upstreams where id in ('" + upstreamA + "','" + upstreamB + "')",
		} {
			_, _ = s.Pool.Exec(ctx, statement)
		}
	}
	t.Cleanup(cleanup)
	cleanup()
	for _, id := range []string{upstreamA, upstreamB} {
		if _, e = s.Pool.Exec(ctx, "insert into upstreams(id,name,base_url,api_key_ciphertext,api_key_nonce,api_key_hint) values($1,$2,'https://example.test',$3,$4,'hint')", id, "ref-upstream-"+id[:8], []byte("cipher"), make([]byte, 12)); e != nil {
			t.Fatalf("insert upstream: %v", e)
		}
	}
	if _, e = s.Pool.Exec(ctx, `with r as (insert into price_revisions(id,model_id,scope,source,locked) values(gen_random_uuid(),$1,'global','manual',false) returning id)
insert into models(model_id,enabled,billing_revision_id) select $1,true,id from r`, modelID); e != nil {
		t.Fatalf("insert model: %v", e)
	}
	if _, e = s.Pool.Exec(ctx, "insert into upstream_models(upstream_id,upstream_model_id,model_id,enabled) values($1,$2,$2,true)", upstreamA, modelID); e != nil {
		t.Fatalf("insert capability: %v", e)
	}
	if _, e = s.Pool.Exec(ctx, "insert into model_route_rules(id,pattern,match_type,target_model_id,upstream_id,priority,enabled) values($1,$2,'exact',$3,$4,10,true)", routeID, "alias-"+suffix, modelID, upstreamB); e != nil {
		t.Fatalf("insert route: %v", e)
	}
	if _, e = s.Pool.Exec(ctx, `with r as (insert into price_revisions(id,model_id,upstream_id,scope,source,locked) values(gen_random_uuid(),$1,$2,'upstream','manual',false) returning id)
insert into model_prices(id,model_id,upstream_id,price_revision_id) select gen_random_uuid(),$1,$2,id from r`, modelID, upstreamB); e != nil {
		t.Fatalf("insert price override: %v", e)
	}

	var conflict *ReferenceConflict
	if e = s.Delete(ctx, "model", modelID, audit); !errors.As(e, &conflict) {
		t.Fatalf("delete model = %v, want ReferenceConflict", e)
	}
	want := map[string]string{"capability": upstreamA, "route_rule": routeID, "price_override": upstreamB}
	if len(conflict.Refs) != len(want) {
		t.Fatalf("references = %+v, want %d", conflict.Refs, len(want))
	}
	for _, ref := range conflict.Refs {
		if want[ref.Kind] != ref.ID {
			t.Fatalf("reference %s=%s, want %s", ref.Kind, ref.ID, want[ref.Kind])
		}
	}

	if e = s.Delete(ctx, "upstream", upstreamB, audit); !errors.As(e, &conflict) {
		t.Fatalf("delete upstream = %v, want ReferenceConflict", e)
	}
	if len(conflict.Refs) != 2 {
		t.Fatalf("upstream references = %+v, want route rule and price override", conflict.Refs)
	}
	for _, ref := range conflict.Refs {
		if ref.Kind != "route_rule" && ref.Kind != "price_override" {
			t.Fatalf("unexpected upstream reference %+v", ref)
		}
	}

	if _, e = s.Pool.Exec(ctx, "delete from model_route_rules where id=$1", routeID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Pool.Exec(ctx, "delete from model_prices where model_id=$1", modelID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Pool.Exec(ctx, "delete from upstream_models where model_id=$1", modelID); e != nil {
		t.Fatal(e)
	}
	if e = s.Delete(ctx, "model", modelID, audit); e != nil {
		t.Fatalf("delete unreferenced model: %v", e)
	}
	if e = s.Delete(ctx, "upstream", upstreamB, audit); e != nil {
		t.Fatalf("delete unreferenced upstream: %v", e)
	}
}

// TestModelCreateRejectsRoutePatternIntegration keeps the shared name
// namespace enforced for POST /api/models, not only for catalog import.
func TestModelCreateRejectsRoutePatternIntegration(t *testing.T) {
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
	defer s.Close()
	suffix := uuid.NewString()[:8]
	alias := "create-alias-" + suffix
	routeID := uuid.NewString()
	target := "create-target-" + suffix
	audit := Audit{Actor: "tester", Action: "test", Payload: []byte(`{}`)}
	t.Cleanup(func() {
		_, _ = s.Pool.Exec(ctx, "delete from model_route_rules where id=$1", routeID)
		_, _ = s.Pool.Exec(ctx, "delete from models where model_id=$1", target)
	})
	if _, e = s.Pool.Exec(ctx, `with r as (insert into price_revisions(id,model_id,scope,source,locked) values(gen_random_uuid(),$1,'global','manual',false) returning id)
insert into models(model_id,enabled,billing_revision_id) select $1,true,id from r`, target); e != nil {
		t.Fatalf("insert target model: %v", e)
	}
	if _, e = s.Pool.Exec(ctx, "insert into model_route_rules(id,pattern,match_type,target_model_id,priority,enabled) values($1,$2,'glob',$3,10,true)", routeID, alias, target); e != nil {
		t.Fatal(e)
	}
	_, e = s.SaveModel(ctx, Model{ID: alias, Enabled: true}, true, audit)
	if !errors.Is(e, ErrModelNameTaken) {
		t.Fatalf("create model named by a route pattern = %v, want ErrModelNameTaken", e)
	}
}
