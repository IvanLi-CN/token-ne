package cost

import "testing"

func ptr[T any](v T) *T { return &v }

func TestCalculateUnknownAndFree(t *testing.T) {
	one := int64(1)
	cost, ok := Calculate(Prices{}, &one, nil, &one)
	if !ok || cost != nil {
		t.Fatalf("unknown pricing: %v %v", cost, ok)
	}
	zero := "0"
	cost, ok = Calculate(Prices{&zero, &zero, &zero}, &one, nil, &one)
	if !ok || cost == nil || *cost != "0.000000" {
		t.Fatalf("free model: %v %v", cost, ok)
	}
	input, cached, output := int64(10), int64(5), int64(10)
	a, b, c := "1", "0.5", "2"
	cost, ok = Calculate(Prices{&a, &b, &c}, &input, &cached, &output)
	if !ok || cost == nil || *cost != "0.000028" {
		t.Fatalf("priced usage: %v %v", cost, ok)
	}
	invalidCached := ptr(int64(11))
	_, ok = Calculate(Prices{&a, &b, &c}, &input, invalidCached, &output)
	if ok {
		t.Fatal("cached token count greater than input should not be priced")
	}
}
