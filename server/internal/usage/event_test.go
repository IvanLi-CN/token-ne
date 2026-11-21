package usage

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const eventFixture = `{"protocol_version":1,"message_id":"0193f0c2-7b1a-7c3e-9f00-000000000006","sent_at":"2026-11-21T10:00:00Z","type":"usage_batch","payload":{"batch_id":"0193f0c2-7b1a-7c3e-9f00-000000000007","core_instance_id":"0193f0c2-7b1a-7c3e-9f00-000000000002","events":[{"event_id":"0193f0c2-7b1a-7c3e-9f00-000000000001","request_id":"req_1","occurred_at":"2026-11-21T09:59:59Z","core_version":"0.2.0","core_instance":"0193f0c2-7b1a-7c3e-9f00-000000000002","endpoint":"/v1/chat/completions","stream":true,"outcome":"success","attempt_count":1,"input_tokens":3,"cached_input_tokens":1,"output_tokens":2,"total_tokens":99,"duration_ms":1}]}}`

func TestDecodeUsageBatchAndEventPath(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2026-11-21T10:00:00Z")
	h := func(b []byte) (Batch, *Problem) {
		return Decode(b, "0193f0c2-7b1a-7c3e-9f00-000000000006", "0193f0c2-7b1a-7c3e-9f00-000000000007", "", now)
	}
	batch, p := h([]byte(eventFixture))
	if p != nil || len(batch.Events) != 1 {
		t.Fatalf("valid batch: batch=%#v problem=%#v", batch, p)
	}
	if !batch.Events[0].NormalizeTotal() || *batch.Events[0].TotalTokens != 5 {
		t.Fatal("input plus output must override inconsistent total")
	}
	invalid := strings.Replace(eventFixture, `"core_instance":"0193f0c2-7b1a-7c3e-9f00-000000000002"`, `"core_instance":"0193f0c2-7b1a-7c3e-9f00-000000000003"`, 1)
	_, p = h([]byte(invalid))
	if p == nil || p.Status != 422 || p.Path != "events[0].core_instance" {
		t.Fatalf("expected precise 422 path, got %#v", p)
	}
	_, p = Decode([]byte(eventFixture), "wrong", "0193f0c2-7b1a-7c3e-9f00-000000000007", "", now)
	if p == nil || p.Status != 400 {
		t.Fatal("header/message mismatch must be malformed")
	}
}

func TestDecodeLimitsAndBatchDuplicateIDs(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2026-11-21T10:00:00Z")
	var envelope map[string]any
	if err := json.Unmarshal([]byte(eventFixture), &envelope); err != nil {
		t.Fatal(err)
	}
	payload := envelope["payload"].(map[string]any)
	event := payload["events"].([]any)[0]
	payload["events"] = []any{event, event}
	body, _ := json.Marshal(envelope)
	_, p := Decode(body, "0193f0c2-7b1a-7c3e-9f00-000000000006", "0193f0c2-7b1a-7c3e-9f00-000000000007", "", now)
	if p == nil || p.Status != 422 || p.Path != "events[1].event_id" {
		t.Fatalf("duplicate path: %#v", p)
	}
	payload["events"] = make([]any, 1001)
	body, _ = json.Marshal(envelope)
	_, p = Decode(body, "0193f0c2-7b1a-7c3e-9f00-000000000006", "0193f0c2-7b1a-7c3e-9f00-000000000007", "", now)
	if p == nil || p.Status != 413 {
		t.Fatalf("expected event limit 413, got %#v", p)
	}
}
