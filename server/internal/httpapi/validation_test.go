package httpapi

import (
	"context"
	"math"
	"net"
	"testing"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/store"
	"github.com/google/uuid"
)

func TestValidation(t *testing.T) {
	m := store.Model{ID: "model"}
	if len(ValidateModel(m)) != 0 {
		t.Fatal("valid model rejected")
	}
	m.ID = ""
	if len(ValidateModel(m)) == 0 {
		t.Fatal("blank model accepted")
	}
	m.ID = "model"
	bad := "2026-02-30"
	m.Shutdown = &bad
	if len(ValidateModel(m)) == 0 {
		t.Fatal("bad date")
	}
	m.Shutdown = nil
	n := math.Inf(1)
	m.Billing.Input = &n
	if len(ValidateModel(m)) == 0 {
		t.Fatal("bad price")
	}
	u := store.Upstream{Name: "u", Kind: "api_key", BaseURL: "https://api.example.com/v1", AuthStyle: "bearer", Connect: 10000, FirstByte: 90000, Idle: 300000, Models: []store.Capability{{ModelID: "model", Priority: 100, Weight: 100}}}
	if len(ValidateUpstream(u, false)) != 0 {
		t.Fatal("valid upstream rejected")
	}
	for _, url := range []string{"http://example.com", "https://user:secret@example.com", "https://127.0.0.1", "https://[::1]", "https://169.254.169.254", "https://10.0.0.1", "https://100.64.0.1"} {
		u.BaseURL = url
		if len(ValidateUpstream(u, false)) == 0 {
			t.Errorf("unsafe URL accepted: %s", url)
		}
	}
	u.BaseURL = "https://example.com"
	u.Models = append(u.Models, u.Models[0])
	if len(ValidateUpstream(u, false)) == 0 {
		t.Fatal("duplicate capability")
	}
	k := store.Key{Name: "key", MaxInflight: 64, Allowed: []string{"*"}}
	if len(ValidateKey(k)) != 0 {
		t.Fatal("valid key rejected")
	}
	k.MaxInflight = 0
	if len(ValidateKey(k)) == 0 {
		t.Fatal("unlimited inflight accepted")
	}
	k.MaxInflight = 1
	k.Allowed = []string{"m", "m"}
	if len(ValidateKey(k)) == 0 {
		t.Fatal("duplicate permission")
	}
	if publicIP(net.ParseIP("::ffff:127.0.0.1")) {
		t.Fatal("mapped private IP accepted")
	}
}

func TestNormalizeUpstreamBaseURLRemovesOnlyTrailingVersionSegment(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"https://api.example/v1", "https://api.example"},
		{"https://api.example/v1/", "https://api.example"},
		{"https://api.example/step_plan/v1", "https://api.example/step_plan"},
		{"https://api.example/gateway%2Fv1", "https://api.example/gateway%2Fv1"},
		{"https://api.example/gateway%2Fv1/v1", "https://api.example/gateway%2Fv1"},
		{"https://api.example/v1/compatible", "https://api.example/v1/compatible"},
	} {
		if got := normalizeUpstreamBaseURL(tc.input); got != tc.want {
			t.Errorf("normalizeUpstreamBaseURL(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestPagination(t *testing.T) {
	c := store.Cursor{Time: time.Now().UTC(), ID: uuid.NewString()}
	v, e := DecodeCursor(EncodeCursor(c))
	if e != nil || *v != c {
		t.Fatalf("cursor roundtrip %v", e)
	}
	for _, s := range []string{"bad", "e30", "bnVsbA"} {
		if _, e = DecodeCursor(s); e == nil {
			t.Fatal("bad cursor accepted")
		}
	}
	if v, e := DecodeCursor(""); v != nil || e != nil {
		t.Fatal("empty cursor")
	}
	for _, n := range []string{"0", "201", "-1", "oops"} {
		if _, e = pageLimit(n); e == nil {
			t.Fatal("bad page limit accepted")
		}
	}
	if n, e := pageLimit(""); n != 50 || e != nil {
		t.Fatal("default limit")
	}
}

func TestPublicDNSValidation(t *testing.T) {
	for _, tc := range []struct {
		ips []string
		ok  bool
	}{{[]string{"8.8.8.8"}, true}, {[]string{"8.8.8.8", "127.0.0.1"}, false}, {[]string{"10.0.0.1"}, false}, {nil, false}} {
		lookup := func(context.Context, string) ([]net.IPAddr, error) {
			var out []net.IPAddr
			for _, s := range tc.ips {
				out = append(out, net.IPAddr{IP: net.ParseIP(s)})
			}
			return out, nil
		}
		if e := validatePublicHost(context.Background(), "https://upstream.example/v1", lookup); (e == nil) != tc.ok {
			t.Fatalf("ips=%v: %v", tc.ips, e)
		}
	}
	if _, e := safeDial(false)(context.Background(), "tcp", "127.0.0.1:1"); e == nil {
		t.Fatal("private dial accepted")
	}
}
