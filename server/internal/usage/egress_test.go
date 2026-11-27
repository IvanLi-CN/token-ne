package usage

import (
	"encoding/json"
	"testing"
	"time"
)

// egressBatch builds a batch whose single event is decodable, so each case only
// has to change the egress pair.
func egressBatch(t *testing.T, mutate func(map[string]any)) (Event, *Problem) {
	t.Helper()
	now, _ := time.Parse(time.RFC3339, "2026-11-21T10:00:00Z")
	var envelope map[string]any
	if err := json.Unmarshal([]byte(eventFixture), &envelope); err != nil {
		t.Fatal(err)
	}
	payload := envelope["payload"].(map[string]any)
	event := payload["events"].([]any)[0].(map[string]any)
	mutate(event)
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	batch, problem := Decode(body, "0193f0c2-7b1a-7c3e-9f00-000000000006", "0193f0c2-7b1a-7c3e-9f00-000000000007", "", now)
	if problem != nil {
		return Event{}, problem
	}
	return batch.Events[0], nil
}

func TestDecodeEgressPairMatchesTheContract(t *testing.T) {
	const proxyID = "0193f0c2-7b1a-7c3e-9f00-000000000008"
	accepted := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"neither field", func(map[string]any) {}},
		{"direct without a proxy", func(e map[string]any) { e["egress_mode"] = "direct" }},
		{"proxy with an id", func(e map[string]any) {
			e["egress_mode"] = "proxy"
			e["egress_proxy_id"] = proxyID
		}},
	}
	for _, tc := range accepted {
		t.Run("accepted/"+tc.name, func(t *testing.T) {
			event, problem := egressBatch(t, tc.mutate)
			if problem != nil {
				t.Fatalf("unexpected rejection: %#v", problem)
			}
			_ = event
		})
	}

	rejected := []struct {
		name   string
		mutate func(map[string]any)
		path   string
	}{
		{"id without a mode", func(e map[string]any) { e["egress_proxy_id"] = proxyID }, "egress_proxy_id"},
		{"direct with an id", func(e map[string]any) {
			e["egress_mode"] = "direct"
			e["egress_proxy_id"] = proxyID
		}, "egress_proxy_id"},
		{"proxy without an id", func(e map[string]any) { e["egress_mode"] = "proxy" }, "egress_proxy_id"},
		{"unknown mode", func(e map[string]any) { e["egress_mode"] = "tunnel" }, "egress_mode"},
		{"non-canonical id", func(e map[string]any) {
			e["egress_mode"] = "proxy"
			e["egress_proxy_id"] = "0193F0C2-7B1A-7C3E-9F00-000000000008"
		}, "egress_proxy_id"},
		{"local error claiming an attempt", func(e map[string]any) {
			e["attempt_count"] = 0
			e["outcome"] = "local_error"
			e["egress_mode"] = "direct"
		}, "egress_mode"},
	}
	for _, tc := range rejected {
		t.Run("rejected/"+tc.name, func(t *testing.T) {
			_, problem := egressBatch(t, tc.mutate)
			if problem == nil {
				t.Fatal("expected a rejection")
			}
			if problem.Status != 422 || problem.Path != "events[0]."+tc.path {
				t.Fatalf("problem = %#v, want 422 at events[0].%s", problem, tc.path)
			}
		})
	}
}

// A phase-one core never sends the pair, and that must stay indistinguishable
// from an explicit omission rather than becoming a validation error.
func TestDecodeEgressAbsentStaysCompatible(t *testing.T) {
	event, problem := egressBatch(t, func(map[string]any) {})
	if problem != nil {
		t.Fatalf("unexpected rejection: %#v", problem)
	}
	if event.EgressMode != nil || event.EgressProxyID != nil {
		t.Fatalf("egress = %v/%v, want both absent", event.EgressMode, event.EgressProxyID)
	}
}
