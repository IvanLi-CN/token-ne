package cores

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/auth"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	contractFixturePath = "../../../tests/e2e/fixtures/config.snapshot.phase2.json"
	contractNegativeDir = "../../../tests/e2e/fixtures/negative"
	contractInstance    = "0193f0c2-7b1a-7c3e-9f00-0000000000b1"
	contractUpstream    = "0193f0c2-7b1a-7c3e-9f00-000000000004"
)

// contractDB returns the exact database view the committed cross-implementation
// fixture was generated from. The Go builder, the Go validator and the Rust
// parser all consume the same bytes, so drift on any side fails a test.
func contractDB(t *testing.T) *fakeDB {
	t.Helper()
	master := make([]byte, 32)
	ciphertext, nonce, err := auth.Encrypt(master, "sk-mock")
	if err != nil {
		t.Fatal(err)
	}
	keys := `[{"id":"0193f0c2-7b1a-7c3e-9f00-000000000003","name":"e2e","sha256":"9f2b2f9f3d0d6f4c0f9e8a7b6c5d4e3f2a1b0c9d8e7f6a5b4c3d2e1f0a9b8c7d","enabled":true,"allowed_models":["*"],"max_inflight":64}]`
	models := `[{"model_id":"gpt-4o-mini","enabled":true,"shutdown_date":null,"created_at":1764806400,"billing":{"input_usd_per_mtok":0.15,"cached_input_usd_per_mtok":0.075,"output_usd_per_mtok":0.6}}]`
	entries := []byte(`[{"id":"` + contractUpstream + `","name":"mock","base_url":"https://mock.example.com","auth_style":"bearer","enabled":true,"connect_timeout_ms":10000,"first_byte_timeout_ms":90000,"idle_timeout_ms":300000,"cipher":"` + base64.StdEncoding.EncodeToString(ciphertext) + `","nonce":"` + base64.StdEncoding.EncodeToString(nonce) + `"}]`)
	capabilities := `[{"upstream_id":"` + contractUpstream + `","upstream_model_id":"mock-model","model_id":"gpt-4o-mini","enabled":true}]`
	db := &fakeDB{}
	db.row = func(q string, _ []any) fakeRow {
		switch {
		case strings.Contains(q, "core_config_generation"):
			return fakeRow{err: pgx.ErrNoRows}
		case strings.Contains(q, "config_schema_min"):
			return fakeRow{values: []any{2, 2, 1}}
		case strings.Contains(q, "jsonb_agg"):
			return fakeRow{values: []any{json.RawMessage(keys), json.RawMessage(models), entries, json.RawMessage(capabilities)}}
		case strings.Contains(q, "control_status->'features'"):
			return fakeRow{values: []any{json.RawMessage(`["chat_completions","responses","embeddings","stream_usage_injection","status_rewrite","model_name_routing","upstream_model_policy"]`)}}
		}
		return fakeRow{err: pgx.ErrNoRows}
	}
	db.rows = func(q string, _ []any) [][]any {
		switch {
		case strings.Contains(q, "from model_route_rules"):
			return [][]any{
				{"0193f0c2-7b1a-7c3e-9f00-000000000011", "gpt-*", "glob", "gpt-4o-mini", nil, 0, true, "primary"},
				{"0193f0c2-7b1a-7c3e-9f00-000000000012", "chat-fast", "exact", "gpt-4o-mini", nil, 1, true, nil},
			}
		case strings.Contains(q, "from upstream_model_policies"):
			return [][]any{{contractUpstream, "allow", []byte(`["gpt-*"]`), []byte(`["gpt-4o-mini-experimental"]`)}}
		}
		return nil
	}
	return db
}

func contractSnapshot(t *testing.T) []byte {
	t.Helper()
	s := Snapshots{DB: contractDB(t), Master: make([]byte, 32), Token: "control-secret", BaseURL: "http://127.0.0.1:18090"}
	snapshot, err := s.Build(context.Background(), contractInstance, false)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.Body
}

func canonical(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatal(err)
	}
	// Only the generation-time metadata may differ between the committed
	// fixture and a fresh build.
	delete(value, "issued_at")
	delete(value, "revision")
	return value
}

// TestPhaseTwoSnapshotContractFixture proves the control-plane builder, the
// publisher validator and the frozen core schema agree on one shared bytes.
func TestPhaseTwoSnapshotContractFixture(t *testing.T) {
	body, err := os.ReadFile(contractFixturePath)
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateSnapshot(body); err != nil {
		t.Fatalf("committed snapshot rejected by publisher validator: %v", err)
	}
	validateSnapshotSchema(t, body)
	required, err := SnapshotFeatures(body)
	if err != nil || len(required) != 2 {
		t.Fatalf("feature probe: %v %v", required, err)
	}
	built := contractSnapshot(t)
	if !reflect.DeepEqual(canonical(t, body), canonical(t, built)) {
		t.Fatalf("committed fixture drifted from the control-plane builder:\nfixture=%s\nbuilt=%s", body, built)
	}
	if err = ValidateSnapshot(built); err != nil {
		t.Fatalf("builder output rejected by the publisher validator: %v", err)
	}
}

type negativeFixture struct {
	Name       string          `json:"name"`
	ExpectPath string          `json:"expect_path"`
	Snapshot   json.RawMessage `json:"snapshot"`
}

func negativeFixtures(t *testing.T) []negativeFixture {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(contractNegativeDir, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("negative fixtures: %v", err)
	}
	out := make([]negativeFixture, 0, len(files))
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var fixture negativeFixture
		if err = json.Unmarshal(raw, &fixture); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if fixture.Name == "" || fixture.ExpectPath == "" || len(fixture.Snapshot) == 0 {
			t.Fatalf("%s: incomplete fixture", file)
		}
		out = append(out, fixture)
	}
	return out
}

// TestPhaseTwoNegativeFixturesRejected checks that every documented negative
// snapshot is rejected by the publisher with the same field path the core
// reports.
func TestPhaseTwoNegativeFixturesRejected(t *testing.T) {
	for _, fixture := range negativeFixtures(t) {
		t.Run(fixture.Name, func(t *testing.T) {
			err := ValidateSnapshot(fixture.Snapshot)
			var details *SnapshotValidationError
			if !errors.As(err, &details) || len(details.Details) == 0 {
				t.Fatalf("publisher accepted or misreported the fixture: %v", err)
			}
			if details.Details[0].Path != fixture.ExpectPath {
				t.Fatalf("publisher path = %q, want %q", details.Details[0].Path, fixture.ExpectPath)
			}
		})
	}
}

// TestPhaseTwoNegativeFixturesReturn422 proves the push endpoint exposes the
// rejection through the protocol envelope before any state change.
func TestPhaseTwoNegativeFixturesReturn422(t *testing.T) {
	for _, fixture := range negativeFixtures(t) {
		t.Run(fixture.Name, func(t *testing.T) {
			var envelope struct {
				Instance string `json:"instance_id"`
				Revision int64  `json:"revision"`
			}
			if err := json.Unmarshal(fixture.Snapshot, &envelope); err != nil {
				t.Fatal(err)
			}
			h := &HTTP{Token: "control-secret"}
			req := httptest.NewRequest(http.MethodPut, "/internal/v1/config", strings.NewReader(string(fixture.Snapshot)))
			req.Header.Set("Authorization", "Bearer control-secret")
			req.Header.Set(ProtocolHeader, "1")
			req.Header.Set(RequestHeader, uuid.Must(uuid.NewV7()).String())
			req.Header.Set(InstanceHeader, envelope.Instance)
			req.Header.Set(RevisionHeader, "1")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.push(rec, req)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			var reply struct {
				Error struct {
					Code    string           `json:"code"`
					Details []SnapshotDetail `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
				t.Fatal(err)
			}
			if reply.Error.Code != "validation_failed" || len(reply.Error.Details) == 0 || reply.Error.Details[0].Path != fixture.ExpectPath {
				t.Fatalf("reply = %s, want path %q", rec.Body.String(), fixture.ExpectPath)
			}
		})
	}
}

// TestNonPhaseTwoCoreStaysOutOfTraffic proves an instance that cannot enforce
// the snapshot's routing semantics is never admitted, so its weight never
// reaches Caddy.
func TestNonPhaseTwoCoreStaysOutOfTraffic(t *testing.T) {
	status := validStatus()
	status.Features = append([]string{}, coreFeaturesOnly...)
	now := time.Now()
	instance := instanceRow{
		ID: status.Instance, Version: status.Version, ContainerID: ptr("container"),
		State: "running", Weight: 100, Heartbeat: &now, Address: "core:8080",
	}
	db := routingSnapshotDB(routingRoutes(), routingPolicies(), coreFeaturesOnly, true)
	peer := &fakePeer{status: status}
	snapshots := &Snapshots{DB: db, Master: make([]byte, 32), Token: "control-secret", BaseURL: "http://127.0.0.1:18090"}
	runtime := Runtime{Pool: db, Peer: peer, Config: snapshots}
	pool, err := runtime.Admit(context.Background(), []instanceRow{instance})
	if len(pool) != 0 {
		t.Fatalf("incompatible core admitted with weight: %+v", pool)
	}
	if !errors.Is(err, ErrFeature) {
		t.Fatalf("admission error = %v, want ErrFeature", err)
	}
	if peer.pushes != 0 {
		t.Fatalf("incompatible core received %d snapshot pushes", peer.pushes)
	}
}
