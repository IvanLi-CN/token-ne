package cores

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/auth"
	"github.com/IvanLi-CN/token-ne/server/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

var coreFeaturesOnly = []string{"chat_completions", "responses", "embeddings", "stream_usage_injection", "status_rewrite"}

// The fake database returns one consistent world: the referenced model and
// upstream exist, so the publisher validator accepts the built snapshot.
const (
	routingUpstreamID = "0193f0c2-7b1a-7c3e-9f00-000000000014"
	routingModelID    = "gpt-4o"
)

func routingSnapshotRows() (keys, models, entries, capabilities json.RawMessage) {
	ciphertext, nonce, err := auth.Encrypt(make([]byte, 32), "sk-mock")
	if err != nil {
		panic(err)
	}
	keys = json.RawMessage(`[]`)
	models = json.RawMessage(`[{"model_id":"gpt-4o","enabled":true,"default_upstream_model":null,"shutdown_date":null,"created_at":1764806400,"billing":{"input_usd_per_mtok":1,"cached_input_usd_per_mtok":null,"output_usd_per_mtok":2}}]`)
	entries = []byte(`[{"id":"` + routingUpstreamID + `","name":"mock","base_url":"https://mock.example.com","auth_style":"bearer","enabled":true,"connect_timeout_ms":10000,"first_byte_timeout_ms":90000,"idle_timeout_ms":300000,"cipher":"` +
		base64.StdEncoding.EncodeToString(ciphertext) + `","nonce":"` + base64.StdEncoding.EncodeToString(nonce) + `"}]`)
	capabilities = json.RawMessage(`[{"upstream_id":"` + routingUpstreamID + `","upstream_model_id":"upstream-gpt-4o","model_id":"gpt-4o","enabled":true}]`)
	return keys, models, entries, capabilities
}

func routingSnapshotDB(routes, policies [][]any, declared []string, hasDeclaration bool) *fakeDB {
	db := &fakeDB{}
	db.row = func(q string, _ []any) fakeRow {
		switch {
		case strings.Contains(q, "core_config_generation"):
			return fakeRow{err: pgx.ErrNoRows}
		case strings.Contains(q, "config_schema_min"):
			return fakeRow{values: []any{1, 1, 1}}
		case strings.Contains(q, "jsonb_agg"):
			keys, models, entries, capabilities := routingSnapshotRows()
			return fakeRow{values: []any{keys, models, entries, capabilities}}
		case strings.Contains(q, "control_status->'features'"):
			if !hasDeclaration {
				return fakeRow{err: pgx.ErrNoRows}
			}
			raw, _ := json.Marshal(declared)
			return fakeRow{values: []any{json.RawMessage(raw)}}
		}
		return fakeRow{err: pgx.ErrNoRows}
	}
	db.rows = func(q string, _ []any) [][]any {
		switch {
		case strings.Contains(q, "from model_route_rules"):
			return routes
		case strings.Contains(q, "from upstream_model_policies"):
			return policies
		}
		return nil
	}
	return db
}

func routingRoutes() [][]any {
	// The SQL query orders by priority,id; the fake returns rows as given.
	return [][]any{
		{uuid.NewString(), "gpt-*", "glob", routingModelID, routingUpstreamID, 100, true, "primary"},
		{uuid.NewString(), "alias", "exact", routingModelID, nil, 300, true, nil},
	}
}

func routingPolicies() [][]any {
	return [][]any{
		{routingUpstreamID, "deny", []byte(`["gpt-*"]`), []byte(`["gpt-4o-mini"]`)},
	}
}

func buildRoutingSnapshot(t *testing.T, db *fakeDB, override bool) (Snapshot, error) {
	t.Helper()
	s := Snapshots{DB: db, Master: make([]byte, 32), Token: "token", BaseURL: "http://api:8080"}
	return s.Build(context.Background(), uuid.NewString(), override)
}

func TestSnapshotRoutingContentAndAdmission(t *testing.T) {
	t.Run("empty content is omitted and requires nothing", func(t *testing.T) {
		db := routingSnapshotDB(nil, nil, coreFeaturesOnly, true)
		snapshot, err := buildRoutingSnapshot(t, db, false)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		if err = json.Unmarshal(snapshot.Body, &body); err != nil {
			t.Fatal(err)
		}
		if _, ok := body["model_routes"]; ok {
			t.Fatal("empty route set was published")
		}
		if _, ok := body["upstream_policies"]; ok {
			t.Fatal("empty policy set was published")
		}
		features := body["features"].(map[string]any)
		for _, name := range []string{FeatureModelNameRouting, FeatureUpstreamModelPolicy} {
			if _, ok := features[name]; ok {
				t.Fatalf("%s set without content", name)
			}
		}
		validateSnapshotSchema(t, snapshot.Body)
	})

	t.Run("content is published with feature flags", func(t *testing.T) {
		routes, policies := routingRoutes(), routingPolicies()
		declared := append(append([]string{}, coreFeaturesOnly...), FeatureModelNameRouting, FeatureUpstreamModelPolicy)
		db := routingSnapshotDB(routes, policies, declared, true)
		snapshot, err := buildRoutingSnapshot(t, db, false)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("snapshot: %s", snapshot.Body)
		var body struct {
			Features map[string]bool  `json:"features"`
			Routes   []map[string]any `json:"model_routes"`
			Policies []map[string]any `json:"upstream_policies"`
		}
		if err = json.Unmarshal(snapshot.Body, &body); err != nil {
			t.Fatal(err)
		}
		if !body.Features[FeatureModelNameRouting] || !body.Features[FeatureUpstreamModelPolicy] {
			t.Fatalf("missing feature flags: %v", body.Features)
		}
		if len(body.Routes) != 2 || body.Routes[0]["priority"].(float64) != 100 || body.Routes[1]["pattern"] != "alias" {
			t.Fatalf("route ordering or content: %+v", body.Routes)
		}
		if body.Routes[0]["description"] != "primary" || body.Routes[0]["upstream_id"] == nil {
			t.Fatalf("route projection: %+v", body.Routes[0])
		}
		if len(body.Policies) != 1 || body.Policies[0]["default_action"] != "deny" || len(body.Policies[0]["allow_patterns"].([]any)) != 1 {
			t.Fatalf("policy projection: %+v", body.Policies)
		}
		validateSnapshotSchema(t, snapshot.Body)
	})

	t.Run("old core is refused", func(t *testing.T) {
		db := routingSnapshotDB(routingRoutes(), routingPolicies(), coreFeaturesOnly, true)
		_, err := buildRoutingSnapshot(t, db, false)
		if !errors.Is(err, ErrFeature) || !strings.Contains(err.Error(), FeatureModelNameRouting) {
			t.Fatalf("old core admission: %v", err)
		}
		for _, write := range db.writes {
			if strings.Contains(write, "insert into system_state") || strings.Contains(write, "update core_instances") {
				t.Fatalf("refused snapshot was published: %s", write)
			}
		}
	})

	t.Run("policy alone requires only the policy feature", func(t *testing.T) {
		declared := append(append([]string{}, coreFeaturesOnly...), FeatureModelNameRouting)
		db := routingSnapshotDB(nil, routingPolicies(), declared, true)
		_, err := buildRoutingSnapshot(t, db, false)
		if !errors.Is(err, ErrFeature) || !strings.Contains(err.Error(), FeatureUpstreamModelPolicy) || strings.Contains(err.Error(), FeatureModelNameRouting) {
			t.Fatalf("policy admission: %v", err)
		}
	})

	t.Run("manual override bypasses admission", func(t *testing.T) {
		db := routingSnapshotDB(routingRoutes(), nil, coreFeaturesOnly, true)
		snapshot, err := buildRoutingSnapshot(t, db, true)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(snapshot.Body), `"model_routes"`) {
			t.Fatal("override dropped routes")
		}
	})

	t.Run("disabled route target stays in the snapshot", func(t *testing.T) {
		models := `[{"model_id":"enabled-model","enabled":true,"billing":{"input_usd_per_mtok":null,"cached_input_usd_per_mtok":null,"output_usd_per_mtok":null}},{"model_id":"disabled-model","enabled":false,"billing":{"input_usd_per_mtok":null,"cached_input_usd_per_mtok":null,"output_usd_per_mtok":null}}]`
		declared := append(append([]string{}, coreFeaturesOnly...), FeatureModelNameRouting)
		db := routingSnapshotDB([][]any{{uuid.NewString(), "legacy-*", "glob", "disabled-model", nil, 10, true, nil}}, nil, declared, true)
		base := db.row
		db.row = func(q string, args []any) fakeRow {
			if strings.Contains(q, "jsonb_agg") {
				return fakeRow{values: []any{json.RawMessage(`[]`), json.RawMessage(models), []byte(`[]`), json.RawMessage(`[]`)}}
			}
			return base(q, args)
		}
		snapshot, err := buildRoutingSnapshot(t, db, false)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(snapshot.Body), `"model_id":"disabled-model"`) || !strings.Contains(string(snapshot.Body), `"target_model_id":"disabled-model"`) {
			t.Fatalf("disabled route target was dropped: %s", snapshot.Body)
		}
		validateSnapshotSchema(t, snapshot.Body)
	})

	t.Run("missing declaration is refused", func(t *testing.T) {
		db := routingSnapshotDB(routingRoutes(), nil, nil, false)
		if _, err := buildRoutingSnapshot(t, db, false); !errors.Is(err, ErrUnknown) {
			t.Fatalf("missing instance declaration: %v", err)
		}
	})
}

func TestSnapshotFeatureRequirements(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want []string
	}{
		{"empty", `{}`, nil},
		{"empty arrays", `{"model_routes":[],"upstream_policies":[]}`, nil},
		{"routes", `{"model_routes":[{"id":"x"}]}`, []string{FeatureModelNameRouting}},
		{"policies", `{"upstream_policies":[{"upstream_id":"x"}]}`, []string{FeatureUpstreamModelPolicy}},
		{"both", `{"model_routes":[{}],"upstream_policies":[{}]}`, []string{FeatureModelNameRouting, FeatureUpstreamModelPolicy}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SnapshotFeatures([]byte(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
	if _, err := SnapshotFeatures([]byte(`[`)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid body: %v", err)
	}
	body := []byte(`{"model_routes":[{}]}`)
	if err := RequireSnapshotFeatures(append(append([]string{}, coreFeaturesOnly...), FeatureModelNameRouting), body); err != nil {
		t.Fatalf("declared routing: %v", err)
	}
	if err := RequireSnapshotFeatures(coreFeaturesOnly, body); !errors.Is(err, ErrFeature) || !strings.Contains(err.Error(), FeatureModelNameRouting) {
		t.Fatalf("missing routing: %v", err)
	}
	if err := RequireSnapshotFeatures(nil, []byte(`{}`)); err != nil {
		t.Fatalf("empty body required features: %v", err)
	}
}

func validateSnapshotSchema(t *testing.T, body []byte) {
	t.Helper()
	raw, err := os.ReadFile("../../../docs/schemas/config.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err = compiler.AddResource("https://token-ne.test/config.schema.json", doc); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("https://token-ne.test/config.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if err = schema.Validate(value); err != nil {
		t.Fatalf("snapshot violates config.schema.json: %v\n%s", err, body)
	}
}

func TestSnapshotRoutingIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	suffix := uuid.NewString()[:8]
	instance, version := uuid.NewString(), uuid.NewString()
	upstreamID := uuid.NewString()
	modelID := "p2s-" + suffix
	full := `["chat_completions","responses","embeddings","stream_usage_injection","status_rewrite","model_name_routing","upstream_model_policy"]`
	old := `["chat_completions","responses","embeddings","stream_usage_injection","status_rewrite"]`
	if _, err = s.Pool.Exec(ctx, "insert into core_versions(id,version,image_ref,protocol_version,config_schema_min,config_schema_max) values($1,$2,'image',1,1,1)", version, "v-"+suffix); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `insert into core_instances(id,version_id,container_id,container_name,address,state,control_status)
 values($1,$2,$3,$4,$5,'running',$6::jsonb)`, instance, version, "container-"+suffix, "core-"+suffix, "core-"+suffix+":8080", `{"features":`+full+`}`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "insert into upstreams(id,name,base_url,api_key_ciphertext,api_key_nonce,api_key_hint) values($1,$2,'https://example.test',$3,$4,'hint')", upstreamID, "snapshot-upstream-"+suffix, []byte("cipher"), make([]byte, 12)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `with r as (insert into price_revisions(id,model_id,scope,source,locked) values(gen_random_uuid(),$1,'global','manual',false) returning id)
insert into models(model_id,enabled,billing_revision_id) select $1,false,id from r`, modelID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		for _, step := range []string{
			"delete from model_route_rules where target_model_id='" + modelID + "'",
			"delete from upstream_model_policies where upstream_id='" + upstreamID + "'",
			"delete from upstream_models where upstream_id='" + upstreamID + "'",
			"delete from core_instances where id='" + instance + "'",
			"delete from core_versions where id='" + version + "'",
			"delete from models where model_id='" + modelID + "'",
			"delete from upstreams where id='" + upstreamID + "'",
		} {
			if _, execErr := s.Pool.Exec(cleanupCtx, step); execErr != nil {
				t.Logf("cleanup %q: %v", step, execErr)
			}
		}
	})
	audit := func(action, kind string) store.Audit {
		return store.Audit{Actor: "tester", Action: action, TargetType: kind}
	}
	pattern := "snapshot-" + suffix
	if _, err = s.SaveRoute(ctx, store.ModelRoute{Pattern: pattern, MatchType: "exact", TargetModelID: modelID, Priority: 10, Enabled: true}, true, audit("model_route.create", "model_route")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SavePolicy(ctx, upstreamID, store.UpstreamPolicy{DefaultAction: "deny", AllowPatterns: []string{modelID}}, audit("upstream_policy.replace", "upstream")); err != nil {
		t.Fatal(err)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snapshots := &Snapshots{}
	routes, err := snapshots.modelRoutes(ctx, tx)
	if err != nil {
		tx.Rollback(ctx)
		t.Fatal(err)
	}
	policies, err := snapshots.upstreamPolicies(ctx, tx)
	tx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var routeFound bool
	for _, r := range routes {
		if r.Pattern == pattern {
			routeFound = r.TargetModelID == modelID && r.UpstreamID == nil && r.Enabled && r.MatchType == "exact"
		}
	}
	if !routeFound {
		t.Fatalf("published routes: %+v", routes)
	}
	var policyFound bool
	for _, p := range policies {
		if p.UpstreamID == upstreamID {
			policyFound = p.DefaultAction == "deny" && len(p.AllowPatterns) == 1 && p.AllowPatterns[0] == modelID && len(p.DenyPatterns) == 0
		}
	}
	if !policyFound {
		t.Fatalf("published policies: %+v", policies)
	}
	body := []byte(`{"model_routes":[{"id":"` + uuid.NewString() + `"}],"upstream_policies":[{"upstream_id":"` + upstreamID + `"}]}`)
	tx, err = s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = snapshots.admitSnapshot(ctx, tx, instance, body)
	tx.Rollback(ctx)
	if err != nil {
		t.Fatalf("full-feature admission: %v", err)
	}
	if _, err = s.Pool.Exec(ctx, "update core_instances set control_status=$2::jsonb where id=$1", instance, `{"features":`+old+`}`); err != nil {
		t.Fatal(err)
	}
	tx, err = s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = snapshots.admitSnapshot(ctx, tx, instance, body)
	tx.Rollback(ctx)
	if !errors.Is(err, ErrFeature) || !strings.Contains(err.Error(), FeatureModelNameRouting) {
		t.Fatalf("old core admission: %v", err)
	}
}
