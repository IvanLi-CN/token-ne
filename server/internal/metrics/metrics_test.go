package metrics

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/usage"
)

func TestMetricsLabelsAreBoundedAndOmitIdentities(t *testing.T) {
	r := New()
	d := 100
	result := usage.Result{Inserted: []usage.AcceptedEvent{{Event: usage.Event{
		EventID: "0193f0c2-7b1a-7c3e-9f00-000000000001", RequestID: "private-request",
		CoreVersion: "0.2.0", CoreInstance: "0193f0c2-7b1a-7c3e-9f00-000000000002",
		RequestedModel: strPtr("gpt-5"), Endpoint: "/v1/chat/completions", Stream: true, Outcome: "success",
		AttemptCount: 2, Duration: &d,
	}, ModelLabel: "gpt-5", VersionLabel: "0.2.0", UpstreamLabel: "primary"}}}
	r.Committed(result, time.Millisecond, time.Now())
	r.OIDCLogin("success")
	r.OIDCDiscovery("failure")
	page := httptest.NewRecorder()
	r.Handler("secret", nil)(page, httptest.NewRequest("GET", "/metrics", nil))
	if page.Code != 401 {
		t.Fatalf("unauthenticated metrics returned %d", page.Code)
	}
	req := httptest.NewRequest("GET", "/metrics", nil)
	req.Header.Set("Authorization", "Bearer secret")
	page = httptest.NewRecorder()
	r.Handler("secret", nil)(page, req)
	body := page.Body.String()
	if !strings.Contains(body, `token_ne_proxy_requests_total{core_version="0.2.0",model="gpt-5",upstream="primary",status="unknown",stream="true"} 1`) {
		t.Fatal("missing proxy request sample")
	}
	if strings.Contains(body, "private-request") || strings.Contains(body, "event_id") || strings.Contains(body, "core_instance=") || strings.Contains(body, "api_key") {
		t.Fatalf("identity leaked into labels: %s", body)
	}
	if !strings.Contains(body, "token_ne_proxy_request_duration_seconds_bucket") {
		t.Fatal("histogram buckets absent")
	}
	if !strings.Contains(body, `token_ne_admin_oidc_login_total{result="success"} 1`) || !strings.Contains(body, `token_ne_admin_oidc_discovery_total{result="failure"} 1`) {
		t.Fatal("OIDC metrics absent")
	}
	if body == "" {
		t.Fatal("metrics body empty")
	}
}

func strPtr(s string) *string { return &s }

func TestCoreAndCaddyMetricsAreExported(t *testing.T) {
	r := New()
	r.ObserveCoreConfig(Core{Version: "0.2.0", Instance: "i1"}, "cache", "invalid", true)
	r.ObserveCaddyApply(false)
	cores := []Core{{Version: "0.2.0", Instance: "i1", State: "running", Weight: 10}}
	page := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	req.Header.Set("Authorization", "Bearer secret")
	r.Handler("secret", &staticSource{cores: cores})(page, req)
	body := page.Body.String()
	for _, want := range []string{
		`token_ne_core_config_source{version="0.2.0",instance="i1",source="cache"} 1`,
		`token_ne_core_config_override_active{version="0.2.0",instance="i1"} 1`,
		`token_ne_core_config_signature_invalid_total{version="0.2.0",instance="i1"} 1`,
		`token_ne_caddy_apply_total{result="error"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s in %s", want, body)
		}
	}
}

type staticSource struct{ cores []Core }

func (s *staticSource) UsageMetricCores(context.Context) ([]Core, error) { return s.cores, nil }
func (s *staticSource) UsageMetricPool() map[string]int32                { return nil }
