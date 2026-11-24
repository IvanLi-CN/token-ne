package cost

import "math/big"

// Prices are PostgreSQL numeric(14,6) values in USD per million tokens. AudioIn
// and AudioOut price audio tokens; CacheWrite prices cache creation.
type Prices struct {
	Input, Cached, CacheWrite, AudioIn, AudioOut, Output *string
}

// Tier is a whole price set that replaces the base rates once the prompt
// exceeds ThresholdTokens, matching providers that charge a long-context rate.
// The JSON tags match the stored tier array so a decoded profile needs no
// conversion.
type Tier struct {
	ThresholdTokens int64   `json:"threshold_tokens"`
	Input           *string `json:"input_usd_per_mtok"`
	Cached          *string `json:"cached_input_usd_per_mtok"`
	CacheWrite      *string `json:"cache_write_usd_per_mtok"`
	AudioIn         *string `json:"audio_input_usd_per_mtok"`
	AudioOut        *string `json:"audio_output_usd_per_mtok"`
	Output          *string `json:"output_usd_per_mtok"`
}

// Usage is the token accounting of one request. Input includes cached and cache
// write tokens; Output includes audio and reasoning tokens, mirroring the
// OpenAI-compatible usage object.
type Usage struct {
	Input, Cached, CacheWrite, AudioIn, AudioOut, Output *int64
}

// Calculate returns decimal USD rounded to the nearest microdollar.
// Missing prices matter only for token categories with nonzero usage.
// Missing input or output counts leave the cost unknown but are not an error.
// The applicable tier is the highest threshold at or below the prompt size.
func Calculate(base Prices, tiers []Tier, u Usage) (*string, bool) {
	if u.Input == nil || u.Output == nil {
		return nil, true
	}
	input, cached, write, audioIn, audioOut, output := *u.Input, int64(0), int64(0), int64(0), int64(0), *u.Output
	if u.Cached != nil {
		cached = *u.Cached
	}
	if u.CacheWrite != nil {
		write = *u.CacheWrite
	}
	if u.AudioIn != nil {
		audioIn = *u.AudioIn
	}
	if u.AudioOut != nil {
		audioOut = *u.AudioOut
	}
	if cached < 0 || write < 0 || audioIn < 0 || audioOut < 0 || output < 0 {
		return nil, false
	}
	textInput := input - cached - write - audioIn
	textOutput := output - audioOut
	if textInput < 0 || textOutput < 0 {
		return nil, false
	}

	prices := base
	for _, tier := range tiers {
		if input >= tier.ThresholdTokens {
			prices = overlay(prices, Prices{
				Input: tier.Input, Cached: tier.Cached, CacheWrite: tier.CacheWrite,
				AudioIn: tier.AudioIn, AudioOut: tier.AudioOut, Output: tier.Output,
			})
		}
	}

	var sum big.Rat
	for _, part := range []struct {
		tokens int64
		price  *string
	}{
		{textInput, prices.Input},
		{cached, prices.Cached},
		{write, prices.CacheWrite},
		{audioIn, prices.AudioIn},
		{textOutput, prices.Output},
		{audioOut, prices.AudioOut},
	} {
		if part.tokens == 0 {
			continue
		}
		if part.price == nil {
			return nil, true
		}
		rate, ok := new(big.Rat).SetString(*part.price)
		if !ok {
			return nil, true
		}
		// Dollar/token = dollar/million tokens / one million.
		rate.Mul(rate, big.NewRat(part.tokens, 1_000_000))
		sum.Add(&sum, rate)
	}
	scaled := new(big.Rat).Mul(&sum, big.NewRat(1_000_000, 1))
	half := new(big.Int).Quo(scaled.Denom(), big.NewInt(2))
	rounded := new(big.Int).Quo(new(big.Int).Add(scaled.Num(), half), scaled.Denom())
	if rounded.Cmp(big.NewInt(99_999_999_999_999)) > 0 {
		return nil, false
	}
	value := new(big.Rat).SetFrac(rounded, big.NewInt(1_000_000)).FloatString(6)
	return &value, true
}

// overlay fills each unset tier rate from the base profile, so a tier that only
// states the context rate keeps charging base rates for the other dimensions.
func overlay(base, tier Prices) Prices {
	pick := func(primary, fallback *string) *string {
		if primary != nil {
			return primary
		}
		return fallback
	}
	return Prices{
		Input:      pick(tier.Input, base.Input),
		Cached:     pick(tier.Cached, base.Cached),
		CacheWrite: pick(tier.CacheWrite, base.CacheWrite),
		AudioIn:    pick(tier.AudioIn, base.AudioIn),
		AudioOut:   pick(tier.AudioOut, base.AudioOut),
		Output:     pick(tier.Output, base.Output),
	}
}
