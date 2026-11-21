package cost

import "math/big"

// Prices are PostgreSQL numeric(14,6) values in USD per million tokens.
type Prices struct {
	Input, Cached, Output *string
}

// Calculate returns decimal USD rounded to the nearest microdollar.
// Missing prices matter only for token categories with nonzero usage.
func Calculate(p Prices, input, cached, output *int64) (*string, bool) {
	if input == nil || output == nil || cached != nil && *cached > *input {
		return nil, false
	}
	c := int64(0)
	if cached != nil {
		c = *cached
	}
	var sum big.Rat
	for _, part := range []struct {
		tokens int64
		price  *string
	}{{*input - c, p.Input}, {c, p.Cached}, {*output, p.Output}} {
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
	rounded := new(big.Int).Quo(new(big.Int).Add(scaled.Num(), new(big.Int).Quo(scaled.Denom(), big.NewInt(2))), scaled.Denom())
	if rounded.Cmp(big.NewInt(99_999_999_999_999)) > 0 {
		return nil, false
	}
	value := new(big.Rat).SetFrac(rounded, big.NewInt(1_000_000)).FloatString(6)
	return &value, true
}
