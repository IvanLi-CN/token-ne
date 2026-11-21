package caddy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

type Backend struct {
	Address string
	Weight  int
}
type Settings struct{ Socket, Domain, TLSMode, APIAddress string }
type object = map[string]any

func Generate(s Settings, pool []Backend) ([]byte, error) {
	if s.TLSMode != "" && s.TLSMode != "off" && s.TLSMode != "auto" {
		return nil, errors.New("invalid Caddy TLS mode")
	}
	if !filepath.IsAbs(s.Socket) || s.TLSMode == "auto" && s.Domain == "" {
		return nil, errors.New("Caddy requires an absolute socket path and a domain for TLS auto")
	}
	if s.APIAddress == "" {
		s.APIAddress = "api:8080"
	}
	sorted := append([]Backend{}, pool...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Address < sorted[j].Address })
	ups := []object{}
	weights := []int{}
	positive := false
	for i, b := range sorted {
		if _, _, err := net.SplitHostPort(b.Address); err != nil || b.Weight < 0 || b.Weight > 10000 || i > 0 && b.Address == sorted[i-1].Address {
			return nil, errors.New("invalid Caddy backend")
		}
		ups = append(ups, object{"dial": b.Address})
		weights = append(weights, b.Weight)
		positive = positive || b.Weight > 0
	}
	route := func(id string, paths []string, handler object, matchHost bool) object {
		r := object{"@id": "token-ne-" + id, "terminal": true, "handle": []object{handler}}
		m := object{}
		if len(paths) > 0 {
			m["path"] = paths
		}
		if matchHost && s.Domain != "" {
			m["host"] = []string{s.Domain}
		}
		if len(m) > 0 {
			r["match"] = []object{m}
		}
		return r
	}
	static := func(code int) object { return object{"handler": "static_response", "status_code": code} }
	apiProxy := object{"handler": "reverse_proxy", "upstreams": []object{{"dial": s.APIAddress}}}
	data := static(503)
	if positive {
		data = object{"handler": "reverse_proxy", "upstreams": ups, "flush_interval": -1,
			"load_balancing": object{"selection_policy": object{"policy": "weighted_round_robin", "weights": weights}},
			"health_checks": object{
				"active":  object{"uri": "/readyz", "interval": "5s", "timeout": "2s", "passes": 2, "fails": 3},
				"passive": object{"fail_duration": "30s", "max_fails": 3, "unhealthy_status": []int{500, 502, 503, 504}},
			},
		}
	}
	data["@id"] = "token-ne-v1-proxy"
	routes := []object{
		route("deny-internal", []string{"/internal", "/internal/*"}, static(404), false),
		route("v1", []string{"/v1", "/v1/*"}, data, true),
		route("api", []string{"/api", "/api/*", "/healthz", "/readyz"}, apiProxy, true),
		route("spa", nil, apiProxy, true),
	}
	server := object{"listen": []string{":80"}, "routes": routes, "automatic_https": object{"disable": true}}
	servers := object{"token-ne": server}
	apps := object{"http": object{"grace_period": "0s", "servers": servers}}
	if s.TLSMode == "auto" {
		// Let Caddy add its HTTP redirects; keep the internal deny ahead of them.
		server["listen"] = []string{":443"}
		server["automatic_https"] = object{}
		server["tls_connection_policies"] = []object{{}}
		servers["token-ne-http"] = object{"listen": []string{":80"}, "routes": []object{
			object{"match": []object{{"path": []string{"/internal", "/internal/*"}}}, "handle": []object{static(404)}, "terminal": true},
			object{"match": []object{{"host": []string{s.Domain}}}, "handle": []object{{"handler": "static_response", "status_code": 308, "headers": object{"Location": []string{"https://" + s.Domain + "{http.request.uri}"}}}}, "terminal": true},
		}, "automatic_https": object{"disable": true}}
		apps["tls"] = object{"automation": object{"policies": []object{{"subjects": []string{s.Domain}}}}}
	}
	return json.Marshal(object{"admin": object{"listen": "unix/" + s.Socket}, "apps": apps})
}

type Client struct {
	Settings Settings
	HTTP     *http.Client
	mu       sync.Mutex
	last     []byte
}

func New(s Settings) *Client {
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", s.Socket)
	}}
	return &Client{Settings: s, HTTP: &http.Client{Transport: tr, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (c *Client) request(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	req, e := http.NewRequestWithContext(ctx, method, "http://caddy"+path, bytes.NewReader(body))
	if e != nil {
		return nil, e
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	res, e := c.HTTP.Do(req)
	if e != nil {
		return nil, e
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("Caddy %s %s: HTTP %d", method, path, res.StatusCode)
	}
	raw, e := io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
	if len(raw) > 8<<20 {
		return nil, errors.New("Caddy response too large")
	}
	return raw, e
}

func (c *Client) Apply(ctx context.Context, pool []Backend, force bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	target, e := Generate(c.Settings, pool)
	if e != nil {
		return e
	}
	current, e := c.request(ctx, http.MethodGet, "/config/", nil)
	if e != nil {
		return e
	}
	if !json.Valid(current) {
		return errors.New("invalid current Caddy configuration")
	}
	if !force && desiredConfig(current, target) {
		if e = c.verify(ctx, target); e == nil {
			c.last = target
			return nil
		}
	}
	previous := c.last
	if len(previous) == 0 {
		previous = current
	}
	_, e = c.request(ctx, http.MethodPost, "/load", target)
	if e == nil {
		e = c.verify(ctx, target)
	}
	if e != nil {
		recoverCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		current, getErr := c.request(recoverCtx, http.MethodGet, "/config/", nil)
		if getErr != nil || !desiredConfig(current, previous) {
			if _, restoreErr := c.request(recoverCtx, http.MethodPost, "/load", previous); restoreErr != nil {
				return errors.Join(e, fmt.Errorf("restore Caddy configuration: %w", restoreErr))
			}
		}
		return errors.Join(e, c.verify(recoverCtx, previous))
	}
	c.last = target
	return nil
}

func desiredConfig(a, b []byte) bool {
	var got, want any
	if json.Unmarshal(a, &got) != nil || json.Unmarshal(b, &want) != nil {
		return false
	}
	return containsConfig(got, want)
}
func containsConfig(got, want any) bool {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return false
		}
		for key, value := range w {
			if !containsConfig(g[key], value) {
				return false
			}
		}
		return true
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for i, value := range w {
			if !containsConfig(g[i], value) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(got, want)
	}
}

func (c *Client) verify(ctx context.Context, expected []byte) error {
	actual, e := c.request(ctx, http.MethodGet, "/config/", nil)
	if e != nil {
		return e
	}
	if !desiredConfig(actual, expected) {
		return errors.New("Caddy loaded configuration differs from desired configuration")
	}
	raw, e := c.request(ctx, http.MethodGet, "/reverse_proxy/upstreams", nil)
	if e != nil {
		return e
	}
	var rows []struct{ Address string }
	if e = json.Unmarshal(raw, &rows); e != nil || rows == nil {
		return errors.New("invalid Caddy upstream response")
	}
	found := map[string]bool{}
	for _, row := range rows {
		found[row.Address] = true
	}
	var config any
	_ = json.Unmarshal(expected, &config)
	var check func(any) error
	check = func(v any) error {
		switch v := v.(type) {
		case []any:
			for _, x := range v {
				if err := check(x); err != nil {
					return err
				}
			}
		case map[string]any:
			if v["handler"] == "reverse_proxy" {
				if ups, ok := v["upstreams"].([]any); ok {
					for _, u := range ups {
						entry, _ := u.(map[string]any)
						if addr, ok := entry["dial"].(string); ok && !found[addr] {
							return fmt.Errorf("Caddy upstream missing: %s", strings.TrimSpace(addr))
						}
					}
				}
			}
			for _, x := range v {
				if err := check(x); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := check(config); err != nil {
		return err
	}
	for _, routeID := range []string{"token-ne-deny-internal", "token-ne-v1", "token-ne-api", "token-ne-spa"} {
		if !strings.Contains(string(actual), routeID) {
			return fmt.Errorf("Caddy route ID missing: %s", routeID)
		}
	}
	return nil
}
