package cost

import "testing"

func TestCalculateUnknownAndFree(t *testing.T) {
	one := int64(1)
	cost, ok := Calculate(Prices{}, nil, Usage{Input: &one, Output: &one})
	if !ok || cost != nil {
		t.Fatalf("unknown pricing: %v %v", cost, ok)
	}
	zero := "0"
	cost, ok = Calculate(Prices{Input: &zero, Cached: &zero, Output: &zero}, nil, Usage{Input: &one, Output: &one})
	if !ok || cost == nil || *cost != "0.000000" {
		t.Fatalf("free model: %v %v", cost, ok)
	}
	input, cached, output := int64(10), int64(5), int64(10)
	a, b, c := "1", "0.5", "2"
	cost, ok = Calculate(Prices{Input: &a, Cached: &b, Output: &c}, nil, Usage{Input: &input, Cached: &cached, Output: &output})
	if !ok || cost == nil || *cost != "0.000028" {
		t.Fatalf("priced usage: %v %v", cost, ok)
	}
	invalidCached := new(int64(11))
	_, ok = Calculate(Prices{Input: &a, Cached: &b, Output: &c}, nil, Usage{Input: &input, Cached: invalidCached, Output: &output})
	if ok {
		t.Fatal("cached token count greater than input should not be priced")
	}
	cost, ok = Calculate(Prices{Input: &a, Cached: &b, Output: &c}, nil, Usage{Output: &output})
	if !ok || cost != nil {
		t.Fatalf("missing input tokens: %v %v", cost, ok)
	}
	cost, ok = Calculate(Prices{Input: &a, Cached: &b, Output: &c}, nil, Usage{Input: &input})
	if !ok || cost != nil {
		t.Fatalf("missing output tokens: %v %v", cost, ok)
	}
}

// TestCalculateDimensionsAndTiers covers the dimensions providers actually
// bill: cache writes, audio tokens and long-context tiers.
func TestCalculateDimensionsAndTiers(t *testing.T) {
	inputPrice, cachedPrice, writePrice := "3", "0.3", "3.75"
	audioIn, audioOut, outputPrice := "6", "12", "15"

	// Cache writes bill at their own rate.
	input, write, output := int64(1000), int64(200), int64(100)
	profile := Prices{Input: &inputPrice, Cached: &cachedPrice, CacheWrite: &writePrice, Output: &outputPrice}
	got, ok := Calculate(profile, nil, Usage{Input: &input, CacheWrite: &write, Output: &output})
	if !ok || got == nil {
		t.Fatalf("cache write cost: %v %v", got, ok)
	}
	// (1000-200)*3 + 200*3.75 + 100*15 = 2400 + 750 + 1500 = 4650 microdollars
	if *got != "0.004650" {
		t.Fatalf("cache write cost = %s", *got)
	}

	// Audio tokens bill at audio rates; text remainder at text rates.
	audioInTokens, audioOutTokens := int64(400), int64(40)
	textIn, textOut := int64(1000), int64(100)
	audioProfile := Prices{Input: &inputPrice, AudioIn: &audioIn, AudioOut: &audioOut, Output: &outputPrice}
	got, ok = Calculate(audioProfile, nil, Usage{
		Input: &textIn, AudioIn: &audioInTokens, Output: &textOut, AudioOut: &audioOutTokens,
	})
	if !ok || got == nil {
		t.Fatalf("audio cost: %v %v", got, ok)
	}
	// (1000-400)*3 + 400*6 + (100-40)*15 + 40*12 = 1800+2400+900+480 = 5580
	if *got != "0.005580" {
		t.Fatalf("audio cost = %s", *got)
	}

	// A context tier replaces the base rates above its threshold.
	bigInput, bigOutput := int64(250_000), int64(1_000)
	tierInput, tierOutput := "4", "18"
	got, ok = Calculate(Prices{Input: &inputPrice, Output: &outputPrice},
		[]Tier{{ThresholdTokens: 200_000, Input: &tierInput, Output: &tierOutput}},
		Usage{Input: &bigInput, Output: &bigOutput})
	if !ok || got == nil {
		t.Fatalf("tier cost: %v %v", got, ok)
	}
	// 250000*4 + 1000*18 = 1,018,000 microdollars
	if *got != "1.018000" {
		t.Fatalf("tier cost = %s", *got)
	}

	// Below the threshold the base rates apply.
	small := int64(1000)
	got, ok = Calculate(Prices{Input: &inputPrice, Output: &outputPrice},
		[]Tier{{ThresholdTokens: 200_000, Input: &tierInput, Output: &tierOutput}},
		Usage{Input: &small, Output: &small})
	if !ok || got == nil || *got != "0.018000" {
		t.Fatalf("base cost = %v %v", got, ok)
	}
}

// TestCalculateReasoningRate covers providers that price reasoning tokens
// separately from completion tokens.
func TestCalculateReasoningRate(t *testing.T) {
	input := int64(1000)
	output := int64(1000)
	reasoning := int64(400)
	in, out, reason := "1", "1.2", "4"
	got, ok := Calculate(Prices{Input: &in, Output: &out, Reasoning: &reason}, nil,
		Usage{Input: &input, Output: &output, Reasoning: &reasoning})
	if !ok || got == nil {
		t.Fatalf("reasoning cost: %v %v", got, ok)
	}
	// 1000*1 + (1000-400)*1.2 + 400*4 = 1000+720+1600 = 3320 microdollars
	if *got != "0.003320" {
		t.Fatalf("reasoning cost = %s", *got)
	}

	// Without a separate reasoning rate the output rate applies.
	got, ok = Calculate(Prices{Input: &in, Output: &out}, nil,
		Usage{Input: &input, Output: &output, Reasoning: &reasoning})
	if !ok || got == nil || *got != "0.002200" {
		t.Fatalf("reasoning-as-output cost = %v %v", got, ok)
	}
}
