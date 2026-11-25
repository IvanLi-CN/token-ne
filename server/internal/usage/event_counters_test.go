package usage

import (
	"strings"
	"testing"
	"time"
)

// The new token counters must be rejected as negative like every other counter,
// otherwise a bad core value is stored or misreported as an input_tokens error.
func TestDecodeRejectsNegativeExtendedCounters(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2026-11-21T10:00:00Z")
	for _, tc := range []struct {
		name  string
		field string
	}{
		{"cache_write_tokens", "cache_write_tokens"},
		{"audio_input_tokens", "audio_input_tokens"},
		{"audio_output_tokens", "audio_output_tokens"},
	} {
		body := strings.Replace(eventFixture, `"input_tokens":3`, `"input_tokens":3,"`+tc.name+`":-1`, 1)
		_, problem := Decode([]byte(body), "0193f0c2-7b1a-7c3e-9f00-000000000006", "0193f0c2-7b1a-7c3e-9f00-000000000007", "", now)
		if problem == nil || problem.Path != "events[0]."+tc.field {
			t.Fatalf("%s: %#v", tc.name, problem)
		}
	}
}

// Cache reads are a subset of input_tokens for the OpenAI-compatible shape the
// core reports; only cache writes may exceed the reported total and be billed
// as separate counters. Pin the boundary Decode enforces so the reconciliation
// tests stay on the reachable side of it.
func TestDecodeRejectsCachedInputAboveInput(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2026-11-21T10:00:00Z")
	body := strings.Replace(eventFixture, `"cached_input_tokens":1`, `"cached_input_tokens":9`, 1)
	_, problem := Decode([]byte(body), "0193f0c2-7b1a-7c3e-9f00-000000000006", "0193f0c2-7b1a-7c3e-9f00-000000000007", "", now)
	if problem == nil || problem.Status != 422 || problem.Path != "events[0].cached_input_tokens" {
		t.Fatalf("cached above input: %#v", problem)
	}
}
