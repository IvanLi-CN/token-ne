package metrics

import (
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
	if body == "" {
		t.Fatal("metrics body empty")
	}
}

func strPtr(s string) *string { return &s }
