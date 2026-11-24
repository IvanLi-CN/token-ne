package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/auth"
	"github.com/IvanLi-CN/token-ne/server/internal/config"
	"github.com/IvanLi-CN/token-ne/server/internal/store"
	"github.com/google/uuid"
)

type priceFake struct {
	*fakeRepo
	profiles           []store.PriceProfile
	saveErr, deleteErr error
	lastModel          string
	lastUpstream       string
	lastBilling        store.Billing
	lastLocked         bool
	lastConfirm        bool
}

func (f *priceFake) Prices(context.Context) ([]store.PriceProfile, error) {
	return f.profiles, nil
}

func (f *priceFake) SaveUpstreamPrice(_ context.Context, modelID, upstreamID string, b store.Billing, locked, confirm bool, _ store.Audit) (store.PriceProfile, error) {
	f.lastModel, f.lastUpstream, f.lastBilling, f.lastLocked, f.lastConfirm = modelID, upstreamID, b, locked, confirm
	if f.saveErr != nil {
		return store.PriceProfile{}, f.saveErr
	}
	now := time.Now().UTC()
	return store.PriceProfile{
		Billing: b,
		PriceRevisionState: store.PriceRevisionState{
			Source: "manual", Locked: locked, PriceRevisionID: uuid.NewString(),
			RevisionCreatedAt: now, UpdatedAt: now,
		},
		ModelID: modelID, UpstreamID: upstreamID, Scope: "upstream",
	}, nil
}

func (f *priceFake) DeleteUpstreamPrice(_ context.Context, _, _ string, confirm bool, _ store.Audit) error {
	f.lastConfirm = confirm
	return f.deleteErr
}

func priceTestAPI(t *testing.T, secondPhase bool) (http.Handler, *priceFake, config.Config, *http.Cookie) {
	t.Helper()
	cfg := config.Config{
		PublicOrigin: "https://admin.example", SessionSecret: strings.Repeat("s", 32), MasterKey: make([]byte, 32),
		LocalLoginEnabled: true, SessionTTLHours: 720, SecondPhaseModelManagement: secondPhase,
	}
	hash, err := auth.HashPassword("password")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRepo{
		admin:  store.Admin{ID: uuid.NewString(), Username: "admin", PasswordHash: hash, SessionVersion: 1},
		models: map[string]store.Model{}, upstreams: map[string]store.Upstream{}, keys: map[string]store.Key{},
	}
	pf := &priceFake{fakeRepo: f}
	token, err := auth.EncodeSession([]byte(cfg.SessionSecret), auth.Session{AdminID: f.admin.ID, Expires: time.Now().Add(time.Hour).Unix(), Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(cfg, pf), pf, cfg, &http.Cookie{Name: auth.CookieName, Value: token}
}

func modelWithBillingState(id string) store.Model {
	now := time.Now().UTC().Add(-time.Hour)
	revision := uuid.NewString()
	return store.Model{
		ID: id, Enabled: true,
		Billing: store.Billing{Input: new(1.5), Cached: new(0.25), Output: new(3.0)},
		BillingState: &store.PriceRevisionState{
			Source: "manual", Locked: false, PriceRevisionID: revision,
			RevisionCreatedAt: now, UpdatedAt: now.Add(time.Minute),
		},
		Created: now, Updated: now.Add(time.Minute),
	}
}

func TestModelBillingStateFlag(t *testing.T) {
	for _, tc := range []struct {
		name        string
		secondPhase bool
		wantState   bool
	}{
		{"phase one response", false, false},
		{"phase two response", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, repo, cfg, cookie := priceTestAPI(t, tc.secondPhase)
			repo.models["model-a"] = modelWithBillingState("model-a")

			w := request(h, cfg, cookie, "GET", "/api/models/model-a", "")
			if w.Code != 200 {
				t.Fatalf("get model: %d %s", w.Code, w.Body.String())
			}
			contract(t, "/api/models/{model_id}", "GET", w)
			if strings.Contains(w.Body.String(), "billing_state") != tc.wantState {
				t.Fatalf("billing_state presence: %s", w.Body.String())
			}
			if tc.wantState {
				var got store.Model
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if got.BillingState == nil || got.BillingState.PriceRevisionID == "" || got.BillingState.RevisionCreatedAt.IsZero() || got.BillingState.UpdatedAt.IsZero() {
					t.Fatalf("incomplete billing_state: %+v", got.BillingState)
				}
			}

			w = request(h, cfg, cookie, "GET", "/api/models", "")
			if w.Code != 200 {
				t.Fatalf("list models: %d %s", w.Code, w.Body.String())
			}
			contract(t, "/api/models", "GET", w)
			if strings.Contains(w.Body.String(), "billing_state") != tc.wantState {
				t.Fatalf("list billing_state presence: %s", w.Body.String())
			}

			w = request(h, cfg, cookie, "PUT", "/api/models/model-a", `{"enabled":true,"billing":{"input_usd_per_mtok":2,"cached_input_usd_per_mtok":null,"output_usd_per_mtok":3},"billing_locked":true}`)
			if w.Code != 200 {
				t.Fatalf("update model: %d %s", w.Code, w.Body.String())
			}
			contract(t, "/api/models/{model_id}", "PUT", w)
			if strings.Contains(w.Body.String(), "billing_state") != tc.wantState {
				t.Fatalf("update billing_state presence: %s", w.Body.String())
			}
			if strings.Contains(w.Body.String(), "billing_locked") || strings.Contains(w.Body.String(), "confirm_locked") {
				t.Fatalf("request-only fields leaked: %s", w.Body.String())
			}
			if !tc.secondPhase && !strings.Contains(w.Body.String(), `"billing":{"input_usd_per_mtok":2`) {
				t.Fatalf("billing input was not applied: %s", w.Body.String())
			}
		})
	}
}

func TestPriceEndpointContracts(t *testing.T) {
	h, repo, cfg, cookie := priceTestAPI(t, true)
	upstreamID := uuid.NewString()
	now := time.Now().UTC().Add(-time.Minute)
	repo.profiles = []store.PriceProfile{{
		Billing:            store.Billing{Input: new(10.0), Cached: nil, Output: new(20.0)},
		PriceRevisionState: store.PriceRevisionState{Source: "manual", Locked: true, PriceRevisionID: uuid.NewString(), RevisionCreatedAt: now, UpdatedAt: now},
		ModelID:            "model-a", UpstreamID: upstreamID, Scope: "upstream",
	}}

	w := request(h, cfg, cookie, "GET", "/api/prices", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"scope":"upstream"`) {
		t.Fatalf("list prices: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/prices", "GET", w)

	body := `{"input_usd_per_mtok":10,"cached_input_usd_per_mtok":null,"output_usd_per_mtok":20,"locked":false}`
	w = request(h, cfg, cookie, "PUT", "/api/upstreams/"+upstreamID+"/models/model-a/price", body)
	if w.Code != 200 {
		t.Fatalf("put price: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/upstreams/{upstream_id}/models/{model_id}/price", "PUT", w)
	if repo.lastModel != "model-a" || repo.lastUpstream != upstreamID || repo.lastLocked || repo.lastBilling.Input == nil || *repo.lastBilling.Input != 10 {
		t.Fatalf("put payload = %+v", repo)
	}

	w = request(h, cfg, cookie, "PUT", "/api/upstreams/"+upstreamID+"/models/model-a/price", `{"input_usd_per_mtok":-1,"cached_input_usd_per_mtok":null,"output_usd_per_mtok":20,"locked":false}`)
	if w.Code != 422 || !strings.Contains(w.Body.String(), "input_usd_per_mtok") {
		t.Fatalf("negative price: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/upstreams/{upstream_id}/models/{model_id}/price", "PUT", w)

	w = request(h, cfg, cookie, "PUT", "/api/upstreams/"+upstreamID+"/models/model-a/price", `{"input_usd_per_mtok":10,"cached_input_usd_per_mtok":null,"output_usd_per_mtok":20}`)
	if w.Code != 422 || !strings.Contains(w.Body.String(), `"path":"locked"`) {
		t.Fatalf("missing lock: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/upstreams/{upstream_id}/models/{model_id}/price", "PUT", w)

	repo.saveErr = store.ErrPriceProfileLocked
	w = request(h, cfg, cookie, "PUT", "/api/upstreams/"+upstreamID+"/models/model-a/price", body)
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"code":"price_profile_locked"`) {
		t.Fatalf("locked put: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/upstreams/{upstream_id}/models/{model_id}/price", "PUT", w)

	repo.saveErr = store.ErrNotFound
	w = request(h, cfg, cookie, "PUT", "/api/upstreams/"+upstreamID+"/models/model-a/price", body)
	if w.Code != 404 {
		t.Fatalf("unknown model: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/upstreams/{upstream_id}/models/{model_id}/price", "PUT", w)
	repo.saveErr = nil

	repo.deleteErr = store.ErrPriceProfileLocked
	w = request(h, cfg, cookie, "DELETE", "/api/upstreams/"+upstreamID+"/models/model-a/price", "")
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"code":"price_profile_locked"`) {
		t.Fatalf("locked delete: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/upstreams/{upstream_id}/models/{model_id}/price", "DELETE", w)

	repo.deleteErr = nil
	w = request(h, cfg, cookie, "DELETE", "/api/upstreams/"+upstreamID+"/models/model-a/price?confirm_locked=true", "")
	if w.Code != 204 || !repo.lastConfirm {
		t.Fatalf("confirmed delete: %d confirm=%v %s", w.Code, repo.lastConfirm, w.Body.String())
	}
	contract(t, "/api/upstreams/{upstream_id}/models/{model_id}/price", "DELETE", w)

	w = request(h, cfg, cookie, "DELETE", "/api/upstreams/not-a-uuid/models/model-a/price", "")
	if w.Code != 422 || !strings.Contains(w.Body.String(), `"path":"upstream_id"`) {
		t.Fatalf("invalid upstream: %d %s", w.Code, w.Body.String())
	}

	repo.deleteErr = store.ErrNotFound
	w = request(h, cfg, cookie, "DELETE", "/api/upstreams/"+uuid.NewString()+"/models/model-a/price", "")
	if w.Code != 404 {
		t.Fatalf("missing override: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/upstreams/{upstream_id}/models/{model_id}/price", "DELETE", w)
}
