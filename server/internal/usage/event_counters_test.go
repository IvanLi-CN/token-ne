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
