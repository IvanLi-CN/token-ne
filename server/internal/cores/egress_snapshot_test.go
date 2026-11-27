package cores

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/auth"
	"github.com/IvanLi-CN/token-ne/server/internal/metrics"
	"github.com/IvanLi-CN/token-ne/server/internal/store"
	"github.com/IvanLi-CN/token-ne/server/internal/xray"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	egressProfileID   = "0193f0c2-7b1a-7c3e-9f00-0000000000e1"
	egressUpstreamID  = "0193f0c2-7b1a-7c3e-9f00-0000000000e2"
	egressOtherID     = "0193f0c2-7b1a-7c3e-9f00-0000000000e3"
	egressInboundPort = 15001
)

// egressRows describes the fake database world for one bound upstream. A nil
// proxy binds nothing; proxyState is the stored runtime_state with enabled.
type egressWorld struct {
	proxyID    string
	proxyState string
	enabled    bool
	inbound    int
	bound      bool
}

// egressDB serves both the routing/policy queries and the egress projection.
func egressDB(world egressWorld, declared []string) *fakeDB {
	db := routingSnapshotDB(nil, nil, declared, true)
	base := db.row
	db.row = func(q string, args []any) fakeRow {
		switch {
		case strings.Contains(q, "from egress_proxies"):
			return fakeRow{err: pgx.ErrNoRows}
		case strings.Contains(q, "jsonb_agg"):
			keys, models, entries, capabilities := egressSnapshotRows(world)
			return fakeRow{values: []any{keys, models, entries, capabilities}}
		}
		return base(q, args)
	}
	baseRows := db.rows
	db.rows = func(q string, args []any) [][]any {
		if strings.Contains(q, "from egress_proxies") {
			return [][]any{{world.proxyID, world.enabled, world.proxyState, world.inbound}}
		}
		return baseRows(q, args)
	}
	return db
}

func egressSnapshotRows(world egressWorld) (keys, models, entries, capabilities json.RawMessage) {
	ciphertext, nonce, err := auth.Encrypt(make([]byte, 32), "sk-mock")
	if err != nil {
		panic(err)
	}
	keys = json.RawMessage(`[]`)
	models = json.RawMessage(`[{"model_id":"gpt-4o","enabled":true,"shutdown_date":null,"created_at":1764806400,"billing":{"input_usd_per_mtok":1,"cached_input_usd_per_mtok":null,"output_usd_per_mtok":2}}]`)
	binding := "null"
	if world.bound {
		binding = `"` + world.proxyID + `"`
	}
	entries = []byte(`[{"id":"` + egressUpstreamID + `","name":"mock","base_url":"https://mock.example.com","auth_style":"bearer","enabled":true,"connect_timeout_ms":10000,"first_byte_timeout_ms":90000,"idle_timeout_ms":300000,"egress_proxy_id":` + binding + `,"cipher":"` +
		base64.StdEncoding.EncodeToString(ciphertext) + `","nonce":"` + base64.StdEncoding.EncodeToString(nonce) + `"}]`)
	capabilities = json.RawMessage(`[{"upstream_id":"` + egressUpstreamID + `","upstream_model_id":"upstream-gpt-4o","model_id":"gpt-4o","enabled":true}]`)
	return keys, models, entries, capabilities
}

func buildEgressSnapshot(t *testing.T, db *fakeDB) Snapshot {
	t.Helper()
	s := Snapshots{DB: db, Master: make([]byte, 32), Token: "token", BaseURL: "http://api:8080"}
	snapshot, err := s.Build(context.Background(), uuid.NewString(), false)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func egressBody(t *testing.T, body []byte) struct {
	Features map[string]bool `json:"features"`
	Proxies  []struct {
		ID       string `json:"id"`
		Endpoint string `json:"endpoint"`
		Enabled  bool   `json:"enabled"`
	} `json:"egress_proxies"`
	Upstreams []struct {
		ID            string  `json:"id"`
		EgressProxyID *string `json:"egress_proxy_id"`
	} `json:"upstreams"`
	Capabilities []struct {
		UpstreamID string `json:"upstream_id"`
	} `json:"upstream_models"`
	Policies []json.RawMessage `json:"upstream_policies"`
	Routes   []json.RawMessage `json:"model_routes"`
} {
	t.Helper()
	var parsed struct {
		Features map[string]bool `json:"features"`
		Proxies  []struct {
			ID       string `json:"id"`
			Endpoint string `json:"endpoint"`
			Enabled  bool   `json:"enabled"`
		} `json:"egress_proxies"`
		Upstreams []struct {
			ID            string  `json:"id"`
			EgressProxyID *string `json:"egress_proxy_id"`
		} `json:"upstreams"`
		Capabilities []struct {
			UpstreamID string `json:"upstream_id"`
		} `json:"upstream_models"`
		Policies []json.RawMessage `json:"upstream_policies"`
		Routes   []json.RawMessage `json:"model_routes"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	return parsed
}

var egressDeclared = append(append([]string{}, coreFeaturesOnly...), FeatureEgressProxy)

// A declaring core receives the profile next to the bound upstream, and the
// snapshot stays schema-valid.
func TestSnapshotEgressForDeclaringCore(t *testing.T) {
	world := egressWorld{proxyID: egressProfileID, proxyState: "ready", enabled: true, inbound: egressInboundPort, bound: true}
	snapshot := buildEgressSnapshot(t, egressDB(world, egressDeclared))
	body := egressBody(t, snapshot.Body)

	if !body.Features[FeatureEgressProxy] {
		t.Fatal("egress_proxy feature missing")
	}
	if len(body.Proxies) != 1 || body.Proxies[0].ID != egressProfileID || !body.Proxies[0].Enabled {
		t.Fatalf("proxy projection = %+v", body.Proxies)
	}
	want := "socks5h://xray:15001"
	if body.Proxies[0].Endpoint != want {
		t.Fatalf("endpoint = %q, want %q", body.Proxies[0].Endpoint, want)
	}
	if len(body.Upstreams) != 1 || body.Upstreams[0].EgressProxyID == nil || *body.Upstreams[0].EgressProxyID != egressProfileID {
		t.Fatalf("upstream binding = %+v", body.Upstreams)
	}
	if len(body.Capabilities) != 1 {
		t.Fatalf("capability relation dropped: %+v", body.Capabilities)
	}
	// Neither the third-party protocol, the server address nor a credential may
	// appear anywhere in the snapshot.
	text := string(snapshot.Body)
	for _, forbidden := range []string{"proxy.example.test", "socks5://", `"protocol":"vmess"`, `"credentials"`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("snapshot leaked %q: %s", forbidden, text)
		}
	}
	validateSnapshotSchema(t, snapshot.Body)
	if !snapshot.ProxyBound {
		t.Fatal("snapshot did not report the proxy binding")
	}
}

// A non-declaring core must never receive the field: the upstream and its
// capability relations are removed, and no proxy entry survives.
func TestSnapshotEgressOmittedForNonDeclaringCore(t *testing.T) {
	world := egressWorld{proxyID: egressProfileID, proxyState: "ready", enabled: true, inbound: egressInboundPort, bound: true}
	snapshot := buildEgressSnapshot(t, egressDB(world, coreFeaturesOnly))
	body := egressBody(t, snapshot.Body)

	if _, ok := body.Features[FeatureEgressProxy]; ok {
		t.Fatal("non-declaring core received the egress_proxy feature")
	}
	if !strings.Contains(string(snapshot.Body), `"egress_proxies"`) {
		// An absent array is the phase-one shape; an empty one is also allowed but
		// the byte-compatibility rule prefers absent.
	} else {
		t.Fatal("non-declaring core received egress_proxies")
	}
	if strings.Contains(string(snapshot.Body), "egress_proxy_id") {
		t.Fatalf("non-declaring core received a binding: %s", snapshot.Body)
	}
	if len(body.Upstreams) != 0 || len(body.Capabilities) != 0 {
		t.Fatalf("bound upstream survived: upstreams=%+v capabilities=%+v", body.Upstreams, body.Capabilities)
	}
	if !snapshot.ProxyBound {
		t.Fatal("snapshot must still report the deployment binding for the weight gate")
	}
	validateSnapshotSchema(t, snapshot.Body)
}

// A profile that is bound but not effectively enabled stays listed with
// enabled=false, so the reference never dangles.
func TestSnapshotEgressDisabledProfileStaysListed(t *testing.T) {
	for _, tc := range []struct {
		state   string
		enabled bool
	}{
		{"degraded", true},
		{"pending", true},
		{"ready", false},
		{"disabled", false},
	} {
		t.Run(tc.state, func(t *testing.T) {
			world := egressWorld{proxyID: egressProfileID, proxyState: tc.state, enabled: tc.enabled, inbound: egressInboundPort, bound: true}
			body := egressBody(t, buildEgressSnapshot(t, egressDB(world, egressDeclared)).Body)
			if len(body.Proxies) != 1 || body.Proxies[0].Enabled {
				t.Fatalf("%s/%v projected as %+v", tc.state, tc.enabled, body.Proxies)
			}
		})
	}
	// swapping keeps the route live, so it stays enabled.
	world := egressWorld{proxyID: egressProfileID, proxyState: "swapping", enabled: true, inbound: egressInboundPort, bound: true}
	body := egressBody(t, buildEgressSnapshot(t, egressDB(world, egressDeclared)).Body)
	if len(body.Proxies) != 1 || !body.Proxies[0].Enabled {
		t.Fatal("swapping profile lost its eligibility")
	}
}

// A profile no upstream binds is omitted even when it exists in the database.
func TestSnapshotEgressOmitsUnboundProfile(t *testing.T) {
	world := egressWorld{proxyID: egressProfileID, proxyState: "ready", enabled: true, inbound: egressInboundPort, bound: false}
	snapshot := buildEgressSnapshot(t, egressDB(world, egressDeclared))
	if strings.Contains(string(snapshot.Body), "egress_proxies") {
		t.Fatalf("unbound profile was published: %s", snapshot.Body)
	}
	if snapshot.ProxyBound {
		t.Fatal("unbound deployment reported as bound")
	}
}

// A deployment with no egress row at all keeps the phase-one bytes exactly.
func TestSnapshotWithoutEgressKeepsPhaseOneBytes(t *testing.T) {
	world := egressWorld{proxyID: egressProfileID, proxyState: "ready", enabled: true, inbound: egressInboundPort, bound: false}
	db := egressDB(world, coreFeaturesOnly)
	db.rows = func(q string, _ []any) [][]any { return nil }
	body := buildEgressSnapshot(t, db).Body
	for _, absent := range []string{"egress_proxies", "egress_proxy_id", `"egress_proxy"`} {
		if strings.Contains(string(body), absent) {
			t.Fatalf("phase-one snapshot gained %q: %s", absent, body)
		}
	}
	// The upstream list must be exactly the previous shape: no null binding.
	var parsed struct {
		Upstreams []map[string]json.RawMessage `json:"upstreams"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	for _, u := range parsed.Upstreams {
		if _, ok := u["egress_proxy_id"]; ok {
			t.Fatalf("phase-one upstream carries the binding key: %+v", u)
		}
	}
}

// egressValidationBody builds a minimal but otherwise valid snapshot carrying
// the given proxies and bindings.
func egressValidationBody(features string, proxies string, upstreams string) []byte {
	return []byte(`{
		"features": ` + features + `,
		"models": [{"model_id":"gpt-5"}],
		"upstreams": ` + upstreams + `,
		"egress_proxies": ` + proxies + `,
		"model_routes": [],
		"upstream_policies": []
	}`)
}

func TestValidateSnapshotEgressRejections(t *testing.T) {
	const (
		proxyA = `{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000e1","endpoint":"socks5h://xray:15001","enabled":true}`
		upA    = `{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000e2","name":"mock","base_url":"https://mock.example.com","auth_style":"bearer","api_key":"sk","enabled":true,"egress_proxy_id":"0193f0c2-7b1a-7c3e-9f00-0000000000e1"}`
		upNoB  = `{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000e2","name":"mock","base_url":"https://mock.example.com","auth_style":"bearer","api_key":"sk","enabled":true}`
	)
	on := `{"egress_proxy":true}`
	off := `{"egress_proxy":false}`
	absent := `{}`

	valid := egressValidationBody(on, `[`+proxyA+`]`, `[`+upA+`]`)
	if err := ValidateSnapshot(valid); err != nil {
		t.Fatalf("valid egress snapshot rejected: %v", err)
	}

	for _, tc := range []struct {
		name string
		body []byte
		path string
	}{
		{"bad endpoint scheme", egressValidationBody(on, `[{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000e1","endpoint":"socks5://xray:15001","enabled":true}]`, `[`+upA+`]`), "egress_proxies[0].endpoint"},
		{"uppercase scheme", egressValidationBody(on, `[{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000e1","endpoint":"SOCKS5H://xray:15001","enabled":true}]`, `[`+upA+`]`), "egress_proxies[0].endpoint"},
		{"leading zero port", egressValidationBody(on, `[{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000e1","endpoint":"socks5h://xray:015001","enabled":true}]`, `[`+upA+`]`), "egress_proxies[0].endpoint"},
		{"low port", egressValidationBody(on, `[{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000e1","endpoint":"socks5h://xray:1023","enabled":true}]`, `[`+upA+`]`), "egress_proxies[0].endpoint"},
		{"high port", egressValidationBody(on, `[{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000e1","endpoint":"socks5h://xray:65536","enabled":true}]`, `[`+upA+`]`), "egress_proxies[0].endpoint"},
		{"credentials in endpoint", egressValidationBody(on, `[{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000e1","endpoint":"socks5h://user:pass@xray:15001","enabled":true}]`, `[`+upA+`]`), "egress_proxies[0].endpoint"},
		{"path in endpoint", egressValidationBody(on, `[{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000e1","endpoint":"socks5h://xray:15001/socks","enabled":true}]`, `[`+upA+`]`), "egress_proxies[0].endpoint"},
		{"bare ipv6 host", egressValidationBody(on, `[{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000e1","endpoint":"socks5h://::1:15001","enabled":true}]`, `[`+upA+`]`), "egress_proxies[0].endpoint"},
		{"non canonical id", egressValidationBody(on, `[{"id":"0193F0C2-7B1A-7C3E-9F00-0000000000E1","endpoint":"socks5h://xray:15001","enabled":true}]`, `[`+upA+`]`), "egress_proxies[0].id"},
		{"duplicate id", egressValidationBody(on, `[`+proxyA+`,`+proxyA+`]`, `[`+upA+`]`), "egress_proxies.id"},
		{"unknown proxy field", egressValidationBody(on, `[{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000e1","endpoint":"socks5h://xray:15001","enabled":true,"protocol":"vmess"}]`, `[`+upA+`]`), "egress_proxies[0].protocol"},
		{"dangling binding", egressValidationBody(on, `[`+proxyA+`]`, `[{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000e2","name":"mock","base_url":"https://mock.example.com","auth_style":"bearer","api_key":"sk","enabled":true,"egress_proxy_id":"0193f0c2-7b1a-7c3e-9f00-0000000000e9"}]`), "upstreams[0].egress_proxy_id"},
		{"feature false", egressValidationBody(off, `[`+proxyA+`]`, `[`+upA+`]`), "egress_proxies"},
		{"feature absent", egressValidationBody(absent, `[`+proxyA+`]`, `[`+upA+`]`), "egress_proxies"},
		{"binding without feature", egressValidationBody(absent, `[]`, `[`+upA+`]`), "upstreams[0].egress_proxy_id"},
		{"binding feature false", egressValidationBody(off, `[]`, `[`+upA+`]`), "upstreams[0].egress_proxy_id"},
		{"misspelled binding", egressValidationBody(on, `[]`, `[{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000e2","name":"mock","base_url":"https://mock.example.com","auth_style":"bearer","api_key":"sk","enabled":true,"egressProxyId":"0193f0c2-7b1a-7c3e-9f00-0000000000e1"}]`), "upstreams[0].egressProxyId"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSnapshot(tc.body)
			var rejection *SnapshotValidationError
			if !errors.As(err, &rejection) || len(rejection.Details) == 0 {
				t.Fatalf("accepted or misreported: %v", err)
			}
			if rejection.Details[0].Path != tc.path {
				t.Fatalf("path = %q, want %q", rejection.Details[0].Path, tc.path)
			}
		})
	}

	// An absent array with all-null bindings is the valid non-declaring shape.
	if err := ValidateSnapshot(egressValidationBody(absent, `[]`, `[`+upNoB+`]`)); err != nil {
		t.Fatalf("non-declaring shape rejected: %v", err)
	}
}

func TestSnapshotFeaturesRequiresEgressProxy(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
		want bool
	}{
		{"empty required nothing", []byte(`{"upstreams":[],"features":{}}`), false},
		{"binding requires the feature", []byte(`{"features":{},"upstreams":[{"id":"a","egress_proxy_id":"p"}]}`), true},
		{"null binding requires nothing", []byte(`{"features":{},"upstreams":[{"id":"a","egress_proxy_id":null}]}`), true},
		{"proxies require the feature", []byte(`{"features":{},"egress_proxies":[{"id":"p"}]}`), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			required, err := SnapshotFeatures(tc.body)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, f := range required {
				if f == FeatureEgressProxy {
					found = true
				}
			}
			if tc.name == "null binding requires nothing" {
				if found {
					t.Fatal("a null binding must not require the capability")
				}
				return
			}
			if found != tc.want {
				t.Fatalf("required = %v", required)
			}
		})
	}
	// A non-declaring core is refused the moment the snapshot carries a binding.
	declared := append([]string{}, coreFeaturesOnly...)
	body := []byte(`{"features":{"egress_proxy":true},"upstreams":[{"id":"a","egress_proxy_id":"0193f0c2-7b1a-7c3e-9f00-0000000000e1"}],"egress_proxies":[{"id":"0193f0c2-7b1a-7c3e-9f00-0000000000e1","endpoint":"socks5h://xray:15001","enabled":true}]}`)
	if err := RequireSnapshotFeatures(declared, body); !errors.Is(err, ErrFeature) {
		t.Fatalf("non-declaring core accepted the snapshot: %v", err)
	}
}

// egressFakeReconciler records what the loop handed to Xray.
type egressFakeReconciler struct {
	summary xray.Summary
	targets []xray.Target
	calls   int
}

func (f *egressFakeReconciler) StartupReconcile(_ context.Context, targets []xray.Target) xray.Summary {
	f.calls++
	f.targets = targets
	return f.summary
}

// A non-declaring core with weight never reaches Caddy once a bound upstream
// exists, and the reason is persisted.
func TestAdmitDeniesWeightForNonDeclaringCore(t *testing.T) {
	status := validStatus()
	status.Features = append([]string{}, coreFeaturesOnly...)
	now := time.Now()
	instance := instanceRow{ID: status.Instance, Version: status.Version, ContainerID: ptr("container"),
		State: "running", Weight: 100, Heartbeat: &now, Address: "core:8080"}
	db := &fakeDB{}
	r := Runtime{Pool: db, Peer: &fakePeer{status: status},
		Config: fixedSnapshot{Snapshot{Revision: 1, Hash: status.Config.Hash, ProxyBound: true}}}
	pool, err := r.Admit(context.Background(), []instanceRow{instance})
	if len(pool) != 0 {
		t.Fatalf("non-declaring core admitted: %+v", pool)
	}
	if !errors.Is(err, ErrFeature) {
		t.Fatalf("admission error = %v, want ErrFeature", err)
	}
	writes := strings.Join(db.writes, "\n")
	if !strings.Contains(writes, "lifecycle_error='egress proxy bindings require the egress_proxy capability'") {
		t.Fatalf("denial reason not recorded: %v", db.writes)
	}
	// A declaring core is admitted under the same snapshot shape.
	status.Features = append([]string{}, egressDeclared...)
	r.Peer = &fakePeer{status: status}
	if pool, err = r.Admit(context.Background(), []instanceRow{instance}); err != nil || len(pool) != 1 {
		t.Fatalf("declaring core denied: %v %v", pool, err)
	}
	// Without a bound upstream the same core keeps its weight.
	r.Config = fixedSnapshot{Snapshot{Revision: 1, Hash: status.Config.Hash}}
	status.Features = append([]string{}, coreFeaturesOnly...)
	r.Peer = &fakePeer{status: status}
	if pool, err = r.Admit(context.Background(), []instanceRow{instance}); err != nil || len(pool) != 1 {
		t.Fatalf("unbound deployment denied: %v %v", pool, err)
	}
}

// The reconcile loop hands Xray the full target set and persists the projection,
// and it never treats an unreachable Xray as a per-profile failure.
func TestReconcileEgressPersistsProjection(t *testing.T) {
	profile := store.EgressProxy{ID: egressProfileID, Protocol: "vmess", Server: "proxy.example.test", Port: 443,
		Transport: json.RawMessage(`{}`), TLS: json.RawMessage(`{"mode":"tls"}`), Enabled: true, Effective: true,
		RuntimeState: "ready", InboundPort: egressInboundPort}
	repo := &store.Store{Pool: &egressStoreDB{profiles: []store.EgressProxy{profile}}}
	engine := &egressFakeReconciler{summary: xray.Summary{Results: []xray.Result{{
		ProfileID: egressProfileID, Status: xray.StatusSwapping, Phase: xray.PhaseRouteStaged,
		Error: "route not confirmed", EffectiveEnabled: true,
	}}}}
	r := Runtime{DB: repo, Egress: engine, Master: make([]byte, 32)}
	if err := r.ReconcileEgress(context.Background()); err != nil {
		t.Fatal(err)
	}
	if engine.calls != 1 || len(engine.targets) != 1 {
		t.Fatalf("engine calls=%d targets=%d", engine.calls, len(engine.targets))
	}
	target := engine.targets[0]
	if !target.PreviouslyEffectiveEnabled || !target.Desired.Profile.Enabled || target.Desired.Profile.InboundPort != egressInboundPort {
		t.Fatalf("target projection = %+v", target)
	}
	if target.Desired.Credentials.UUID != "" {
		t.Fatal("credentials leaked into the assertion path")
	}
	writes := strings.Join(repo.Pool.(*egressStoreDB).writes, "\n")
	if !strings.Contains(writes, "update egress_proxies set runtime_state=$2,swap_phase=$3,last_error=$4") {
		t.Fatalf("state projection not written: %v", writes)
	}

	// Unreachable Xray keeps each profile's stored state and never restarts it.
	unreachable := &egressFakeReconciler{summary: xray.Summary{Unreachable: true, Error: "xray api unreachable"}}
	repo.Pool.(*egressStoreDB).writes = nil
	r.Egress = unreachable
	if err := r.ReconcileEgress(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repo.Pool.(*egressStoreDB).writes) != 0 {
		t.Fatalf("unreachable Xray wrote state: %v", repo.Pool.(*egressStoreDB).writes)
	}
}

// egressStoreDB is the minimal store.DB the reconcile test needs: it serves the
// profile rows and records the state writes and audit rows.
type egressStoreDB struct {
	store.DB
	profiles []store.EgressProxy
	writes   []string
}

func (d *egressStoreDB) Query(_ context.Context, q string, _ ...any) (pgx.Rows, error) {
	if !strings.Contains(q, "from egress_proxies") {
		return &fakeRows{}, nil
	}
	data := make([][]any, 0, len(d.profiles))
	for _, p := range d.profiles {
		var swapPhase *string
		data = append(data, []any{p.ID, p.Name, p.Protocol, p.Server, p.Port, string(p.Transport), string(p.TLS),
			p.Enabled, p.RuntimeState, swapPhase, p.InboundPort, p.LastError, p.Created, p.Updated, p.Ciphertext, p.Nonce})
	}
	return &fakeRows{data: data}, nil
}

func (d *egressStoreDB) QueryRow(_ context.Context, q string, _ ...any) pgx.Row {
	if strings.Contains(q, "credentials_ciphertext,credentials_nonce") {
		return fakeRow{err: pgx.ErrNoRows}
	}
	return fakeRow{err: pgx.ErrNoRows}
}

func (d *egressStoreDB) Exec(_ context.Context, q string, _ ...any) (pgconn.CommandTag, error) {
	d.writes = append(d.writes, q)
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func (d *egressStoreDB) Begin(context.Context) (pgx.Tx, error) {
	return &egressStoreTx{db: d}, nil
}

type egressStoreTx struct {
	pgx.Tx
	db *egressStoreDB
}

func (t *egressStoreTx) Exec(c context.Context, q string, a ...any) (pgconn.CommandTag, error) {
	return t.db.Exec(c, q, a...)
}
func (t *egressStoreTx) QueryRow(c context.Context, q string, a ...any) pgx.Row {
	return t.db.QueryRow(c, q, a...)
}
func (t *egressStoreTx) Query(c context.Context, q string, a ...any) (pgx.Rows, error) {
	return t.db.Query(c, q, a...)
}
func (t *egressStoreTx) Commit(context.Context) error   { return nil }
func (t *egressStoreTx) Rollback(context.Context) error { return nil }

// revisionHarness rebuilds a snapshot across a mutable profile world so the
// computed-value revision trigger can be observed directly.
type revisionHarness struct {
	// saved mirrors system_state: the shared counter plus one fingerprint per
	// instance, because two instances can legitimately publish different bodies.
	saved map[string]generation
	// instance is fixed so repeated builds describe the same core.
	instance string
	db       *fakeDB
	world    *egressWorld
	// egressQuery is the SQL the projection ran; the revision can only follow
	// the columns it reads.
	egressQuery string
}

func newRevisionHarness(world egressWorld) *revisionHarness {
	h := &revisionHarness{world: &world, saved: map[string]generation{}, instance: uuid.NewString()}
	db := &fakeDB{}
	db.row = func(q string, args []any) fakeRow {
		switch {
		case strings.Contains(q, "core_config_generation"):
			// The shared counter query filters on the literal key, so it carries
			// no argument; the per-instance fingerprint query carries its key as
			// $1 and must be matched on that, not on the literal.
			key := "core_config_generation"
			if len(args) > 0 {
				key, _ = args[0].(string)
			}
			value, ok := h.saved[key]
			if !ok || value.Revision == 0 {
				return fakeRow{err: pgx.ErrNoRows}
			}
			return fakeRow{values: []any{value}}
		case strings.Contains(q, "select value from system_state where key=$1"):
			key, _ := args[0].(string)
			value, ok := h.saved[key]
			if !ok || value.Revision == 0 {
				return fakeRow{err: pgx.ErrNoRows}
			}
			return fakeRow{values: []any{value}}
		case strings.Contains(q, "config_schema_min"):
			return fakeRow{values: []any{2, 2, 1}}
		case strings.Contains(q, "control_status->'features'"):
			raw, _ := json.Marshal(egressDeclared)
			return fakeRow{values: []any{json.RawMessage(raw)}}
		case strings.Contains(q, "jsonb_agg"):
			keys, models, entries, capabilities := egressSnapshotRows(*h.world)
			return fakeRow{values: []any{keys, models, entries, capabilities}}
		}
		return fakeRow{err: pgx.ErrNoRows}
	}
	db.rows = func(q string, _ []any) [][]any {
		if strings.Contains(q, "from egress_proxies") {
			h.egressQuery = q
			return [][]any{{h.world.proxyID, h.world.enabled, h.world.proxyState, h.world.inbound}}
		}
		return nil
	}
	db.exec = func(q string, args []any) error {
		if strings.Contains(q, "insert into system_state") {
			// Two arguments means the per-instance fingerprint; one means the
			// shared counter the wire revision comes from.
			if len(args) == 2 {
				key, _ := args[0].(string)
				h.saved[key] = args[1].(generation)
			} else {
				h.saved["core_config_generation"] = args[0].(generation)
			}
		}
		return nil
	}
	h.db = db
	return h
}

func (h *revisionHarness) build(t *testing.T) int64 {
	t.Helper()
	s := Snapshots{DB: h.db, Master: make([]byte, 32), Token: "token", BaseURL: "http://api:8080"}
	snapshot, err := s.Build(context.Background(), h.instance, false)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.Revision
}

// The revision follows only the computed values: enabled and the endpoint.
// ready <-> swapping keeps enabled true, and server/credential edits never move
// the internal endpoint, so none of them may bump it.
func TestSnapshotRevisionFollowsOnlyComputedEgressValues(t *testing.T) {
	h := newRevisionHarness(egressWorld{proxyID: egressProfileID, proxyState: "ready", enabled: true, inbound: egressInboundPort, bound: true})
	base := h.build(t)

	// ready -> swapping -> ready: enabled stays true, so no revision.
	h.world.proxyState = "swapping"
	if got := h.build(t); got != base {
		t.Fatalf("swapping bumped the revision: %d != %d", got, base)
	}
	h.world.proxyState = "ready"
	if got := h.build(t); got != base {
		t.Fatalf("returning to ready bumped the revision: %d != %d", got, base)
	}
	// A credential edit changes neither the endpoint nor the eligibility. The
	// proof is that the projection cannot see one: it reads the eligibility and
	// the inbound port only, never a credential, transport, TLS or server column.
	for _, absent := range []string{"credentials", "transport", "tls", "server"} {
		if strings.Contains(h.egressQuery, absent) {
			t.Fatalf("egress projection reads %q, so editing it could bump the revision: %s", absent, h.egressQuery)
		}
	}

	// enabled true -> false (runtime leaves the live states).
	h.world.proxyState = "degraded"
	disabled := h.build(t)
	if disabled <= base {
		t.Fatalf("enabled flip did not bump the revision: %d", disabled)
	}
	// enabled false -> true.
	h.world.proxyState = "ready"
	back := h.build(t)
	if back <= disabled {
		t.Fatalf("re-enable did not bump the revision: %d", back)
	}
	// The endpoint changing bumps it even while enabled stays true.
	h.world.inbound = egressInboundPort + 1
	moved := h.build(t)
	if moved <= back {
		t.Fatalf("endpoint change did not bump the revision: %d", moved)
	}
	// A repeat build of an unchanged world is stable.
	if got := h.build(t); got != moved {
		t.Fatalf("stable world bumped the revision: %d != %d", got, moved)
	}
}

// TestSnapshotEgressProjectionIntegration proves the projection against the
// real schema. It drives egressSection inside a transaction instead of Build:
// Build aggregates every upstream row in the database, and a shared test database
// holds fixtures encrypted with another master key, so a full Build there proves
// nothing about this projection.
func TestSnapshotEgressProjectionIntegration(t *testing.T) {
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
	instance, version, profileID, upstreamID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	full := `["chat_completions","responses","embeddings","stream_usage_injection","status_rewrite","egress_proxy"]`
	old := `["chat_completions","responses","embeddings","stream_usage_injection","status_rewrite"]`
	if _, err = s.Pool.Exec(ctx, "insert into core_versions(id,version,image_ref,protocol_version,config_schema_min,config_schema_max) values($1,$2,'image',1,2,2)", version, "v-"+suffix); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `insert into core_instances(id,version_id,container_id,container_name,address,state,control_status)
 values($1,$2,$3,$4,$5,'running',$6::jsonb)`, instance, version, "container-"+suffix, "core-"+suffix, "core-"+suffix+":8080", `{"features":`+full+`}`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `insert into egress_proxies(id,name,protocol,server,port,inbound_port,enabled,runtime_state)
 values($1,$2,'vmess','proxy.example.test',443,15901,true,'ready')`, profileID, "egress-"+suffix); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		for _, step := range []string{
			"delete from egress_proxies where id='" + profileID + "'",
			"delete from core_instances where id='" + instance + "'",
			"delete from core_versions where id='" + version + "'",
		} {
			if _, execErr := s.Pool.Exec(cleanupCtx, step); execErr != nil {
				t.Logf("cleanup %q: %v", step, execErr)
			}
		}
	})

	// boundBody is the pre-gate snapshot state: one bound upstream, its
	// capability relation, plus an unrelated direct upstream.
	boundBody := func() configBody {
		caps := json.RawMessage(`[{"upstream_id":"` + upstreamID + `","upstream_model_id":"upstream-gpt-4o","model_id":"gpt-4o","enabled":true},{"upstream_id":"` + egressOtherID + `","upstream_model_id":"other-gpt-4o","model_id":"gpt-4o","enabled":true}]`)
		return configBody{
			Features:     map[string]bool{},
			Upstreams:    []boundUpstream{{upstream: upstream{ID: upstreamID}, EgressProxyID: ptr(profileID)}, {upstream: upstream{ID: egressOtherID}}},
			Capabilities: caps,
		}
	}
	snapshots := &Snapshots{DB: s.Pool, Master: make([]byte, 32), Token: "token", BaseURL: "http://api:8080"}

	// A declaring core receives the profile with the computed effective value.
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	body := boundBody()
	bound, err := snapshots.egressSection(ctx, tx, instance, &body)
	tx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bound || len(body.EgressProxies) != 1 || body.EgressProxies[0].ID != profileID || !body.EgressProxies[0].Enabled {
		t.Fatalf("declaring projection = %+v", body.EgressProxies)
	}
	if body.EgressProxies[0].Endpoint != "socks5h://xray:15901" {
		t.Fatalf("endpoint = %q", body.EgressProxies[0].Endpoint)
	}
	var relations []struct {
		UpstreamID string `json:"upstream_id"`
	}
	if err = json.Unmarshal(body.Capabilities, &relations); err != nil {
		t.Fatal(err)
	}
	if len(body.Upstreams) != 2 || len(relations) != 2 {
		t.Fatalf("declaring core lost an upstream: %+v / %+v", body.Upstreams, relations)
	}

	// An effectively disabled profile stays listed with enabled=false.
	if _, err = s.Pool.Exec(ctx, "update egress_proxies set runtime_state='degraded' where id=$1", profileID); err != nil {
		t.Fatal(err)
	}
	tx, err = s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	body = boundBody()
	if _, err = snapshots.egressSection(ctx, tx, instance, &body); err != nil {
		tx.Rollback(ctx)
		t.Fatal(err)
	}
	tx.Rollback(ctx)
	if len(body.EgressProxies) != 1 || body.EgressProxies[0].Enabled {
		t.Fatalf("degraded profile = %+v", body.EgressProxies)
	}

	// A non-declaring core loses the upstream, its relation and every egress
	// field; the direct upstream survives.
	if _, err = s.Pool.Exec(ctx, "update core_instances set control_status=$2::jsonb where id=$1", instance, `{"features":`+old+`}`); err != nil {
		t.Fatal(err)
	}
	tx, err = s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	body = boundBody()
	bound, err = snapshots.egressSection(ctx, tx, instance, &body)
	tx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bound || len(body.EgressProxies) != 0 {
		t.Fatalf("non-declaring core projection = %+v", body.EgressProxies)
	}
	if len(body.Upstreams) != 1 || body.Upstreams[0].ID != egressOtherID {
		t.Fatalf("non-declaring upstreams = %+v", body.Upstreams)
	}
	var caps []struct {
		UpstreamID string `json:"upstream_id"`
	}
	if err = json.Unmarshal(body.Capabilities, &caps); err != nil {
		t.Fatal(err)
	}
	if len(caps) != 1 || caps[0].UpstreamID != egressOtherID {
		t.Fatalf("non-declaring relations = %+v", caps)
	}
}

// The reconcile pass is the only place that knows both the outcome and the
// persisted projection, so it must publish the metrics the PRD asks for. A
// missing call would leave the Grafana panels empty with no failing test.
func TestReconcileEgressPublishesMetrics(t *testing.T) {
	profile := store.EgressProxy{ID: egressProfileID, Protocol: "vmess", Server: "proxy.example.test", Port: 443,
		Transport: json.RawMessage(`{}`), TLS: json.RawMessage(`{"mode":"tls"}`), Enabled: true, Effective: true,
		RuntimeState: "ready", InboundPort: egressInboundPort}
	repo := &store.Store{Pool: &egressStoreDB{profiles: []store.EgressProxy{profile}}}
	engine := &egressFakeReconciler{summary: xray.Summary{Results: []xray.Result{
		{ProfileID: egressProfileID, Status: xray.StatusDegraded, Error: "rejected", EffectiveEnabled: false},
	}}}
	registry := metrics.New()
	r := Runtime{DB: repo, Egress: engine, Master: make([]byte, 32), Metrics: registry}
	if err := r.ReconcileEgress(context.Background()); err != nil {
		t.Fatal(err)
	}
	page := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	req.Header.Set("Authorization", "Bearer token")
	registry.Handler("token", nil)(page, req)
	body := page.Body.String()
	for _, want := range []string{
		`token_ne_xray_reconcile_total{result="rejected"} 1`,
		`token_ne_egress_proxy_state{state="degraded"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s in %s", want, body)
		}
	}

	// An unreachable Xray is its own outcome and must not be counted as a
	// per-profile rejection.
	unreachableRegistry := metrics.New()
	r.Metrics = unreachableRegistry
	r.Egress = &egressFakeReconciler{summary: xray.Summary{Unreachable: true, Error: "down"}}
	if err := r.ReconcileEgress(context.Background()); err != nil {
		t.Fatal(err)
	}
	page = httptest.NewRecorder()
	unreachableRegistry.Handler("token", nil)(page, req)
	if !strings.Contains(page.Body.String(), `token_ne_xray_reconcile_total{result="unreachable"} 1`) {
		t.Fatalf("unreachable outcome missing: %s", page.Body.String())
	}
}

// Two instances can legitimately build different bodies from the same database:
// a core that does not declare egress_proxy gets the proxy-bound upstreams
// removed. A single shared fingerprint made such a fleet bump the revision on
// every pass (A publishes, B does not match A, B publishes, A no longer matches
// B, ...), which pushed a new revision to every core every few seconds.
func TestMixedFleetDoesNotChurnTheRevision(t *testing.T) {
	// One shared system_state (one database), two independent worlds: A declares
	// egress_proxy and sees the bound upstream, B does not and sees it removed.
	shared := map[string]generation{}
	a := newRevisionHarness(egressWorld{proxyID: egressProfileID, proxyState: "ready", enabled: true, inbound: egressInboundPort, bound: true})
	a.saved = shared
	b := newRevisionHarness(egressWorld{proxyID: egressProfileID, proxyState: "ready", enabled: true, inbound: egressInboundPort, bound: true})
	b.saved = shared

	a1 := a.build(t)
	b1 := b.build(t)
	// Alternating builds of unchanged worlds must not move either revision.
	for range 3 {
		if got := a.build(t); got != a1 {
			t.Fatalf("instance A revision churned: %d != %d", got, a1)
		}
		if got := b.build(t); got != b1 {
			t.Fatalf("instance B revision churned: %d != %d", got, b1)
		}
	}
	// A real change bumps the instance that caused it and leaves the other alone.
	b.world.proxyState = "degraded"
	b2 := b.build(t)
	if b2 <= b1 {
		t.Fatalf("a real change did not bump B: %d", b2)
	}
	if got := a.build(t); got != a1 {
		t.Fatalf("B's change bumped A: %d != %d", got, a1)
	}
}

// buildFor builds with the second harness but a fixed instance id, so the two
// harnesses model two distinct cores sharing one database.
func buildFor(t *testing.T, h *revisionHarness, instance string) int64 {
	t.Helper()
	db := h.db
	s := Snapshots{DB: db, Master: make([]byte, 32), Token: "token", BaseURL: "http://api:8080"}
	snapshot, err := s.Build(context.Background(), instance, false)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.Revision
}
