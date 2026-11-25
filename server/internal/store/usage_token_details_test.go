package store

import (
	"context"
	"testing"

	"github.com/IvanLi-CN/token-ne/server/internal/cost"
	"github.com/IvanLi-CN/token-ne/server/internal/usage"
)

func TestTokenUsageReconcilesDetailShapes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		event  usage.Event
		input  *int64
		output *int64
	}{
		{
			name:  "openai subsets stay inside the reported totals",
			event: usage.Event{InputTokens: int64p(1000), CachedInputTokens: int64p(800), OutputTokens: int64p(50), ReasoningTokens: int64p(20)},
			input: int64p(1000), output: int64p(50),
		},
		{
			name:  "cache creation reported outside input_tokens is added",
			event: usage.Event{InputTokens: int64p(100), CachedInputTokens: int64p(800), CacheWriteTokens: int64p(100)},
			input: int64p(1000),
		},
		{
			name:  "audio and reasoning reported outside output_tokens are added",
			event: usage.Event{InputTokens: int64p(10), OutputTokens: int64p(10), AudioOutputTokens: int64p(5), ReasoningTokens: int64p(15)},
			input: int64p(10), output: int64p(30),
		},
		{
			name:  "equal detail sum is still the subset shape",
			event: usage.Event{InputTokens: int64p(800), CachedInputTokens: int64p(800)},
			input: int64p(800),
		},
		{
			name:  "missing totals stay unknown",
			event: usage.Event{CachedInputTokens: int64p(800)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tokenUsage(context.Background(), tc.event)
			if !sameInt64(got.Input, tc.input) || !sameInt64(got.Output, tc.output) {
				t.Fatalf("input=%v output=%v, want input=%v output=%v", value(got.Input), value(got.Output), value(tc.input), value(tc.output))
			}
		})
	}
}

// A provider that reports cache creation outside prompt_tokens must still be
// billable; before the reconciliation the negative text input rejected the
// whole usage batch.
func TestTokenUsageKeepsAnthropicShapedInputBillable(t *testing.T) {
	event := usage.Event{
		EventID:     "0193f0c2-7b1a-7c3e-9f00-000000000001",
		InputTokens: int64p(100), CachedInputTokens: int64p(800), CacheWriteTokens: int64p(100),
		OutputTokens: int64p(50),
	}
	u := tokenUsage(context.Background(), event)
	price := "1"
	got, fits := cost.Calculate(cost.Prices{Input: &price, Cached: &price, CacheWrite: &price, Output: &price}, nil, u)
	if !fits || got == nil {
		t.Fatalf("cost = %v fits=%v", got, fits)
	}
	// (100 text + 800 cached + 100 cache write + 50 output) / 1e6 USD at $1/Mtok.
	if *got != "0.001050" {
		t.Fatalf("cost = %s, want 0.001050", *got)
	}
}

func sameInt64(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func value(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}
