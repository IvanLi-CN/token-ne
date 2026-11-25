package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/auth"
	"github.com/IvanLi-CN/token-ne/server/internal/config"
	"github.com/IvanLi-CN/token-ne/server/internal/store"
	"github.com/google/uuid"
)

// egressRepo is an in-memory stand-in for the egress surface. It reproduces the
// store's contract: unique names, one inbound port per profile that survives
// enable/disable, a binding check on delete, and a dangling-reference check on
// upstream writes.
type egressRepo struct {
	*fakeRepo
	mu        sync.Mutex
	proxies   map[string]store.EgressProxy
	order     []string
	ports     store.PortRange
	exhaust   bool
	lastAudit store.Audit
	saveErr   error
}

func newEgressRepo() *egressRepo {
	f := &fakeRepo{admin: store.Admin{ID: uuid.NewString(), Username: "admin", SessionVersion: 1},
		models: map[string]store.Model{}, upstreams: map[string]store.Upstream{}, keys: map[string]store.Key{}}
	return &egressRepo{fakeRepo: f, proxies: map[string]store.EgressProxy{}, ports: store.PortRange{Min: 15000, Max: 15999}}
}

func (r *egressRepo) EgressProxies(context.Context) ([]store.EgressProxy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []store.EgressProxy{}
	for _, id := range r.order {
		out = append(out, r.proxies[id])
	}
	return out, nil
}

func (r *egressRepo) EgressProxy(_ context.Context, id string) (store.EgressProxy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.proxies[id]
	if !ok {
		return store.EgressProxy{}, store.ErrNotFound
	}
	return p, nil
}

func (r *egressRepo) SaveEgressProxy(_ context.Context, p store.EgressProxy, create bool, ports store.PortRange, a store.Audit) (store.EgressProxy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastAudit = a
	if r.saveErr != nil {
		return store.EgressProxy{}, r.saveErr
	}
	r.ports = ports
	for _, other := range r.proxies {
		if other.ID != p.ID && other.Name == p.Name {
			return store.EgressProxy{}, &store.FieldError{Path: "name", Kind: store.ErrConflict, Reason: "an egress proxy with this name already exists"}
		}
	}
	if create {
		if r.exhaust {
			return store.EgressProxy{}, store.ErrEgressPortExhausted
		}
		p.InboundPort = r.freePort()
		p.RuntimeState = "pending"
		p.Created = time.Now().UTC()
		r.order = append(r.order, p.ID)
	} else {
		old, ok := r.proxies[p.ID]
		if !ok {
			return store.EgressProxy{}, store.ErrNotFound
		}
		p.InboundPort, p.RuntimeState, p.Created = old.InboundPort, old.RuntimeState, old.Created
	}
	p.Updated = time.Now().UTC()
	p.HasCredentials = len(p.Ciphertext) > 0
	p.Effective = p.Enabled && (p.RuntimeState == "ready" || p.RuntimeState == "swapping")
	r.proxies[p.ID] = p
	return p, nil
}

func (r *egressRepo) freePort() int {
	used := map[int]bool{}
	for _, p := range r.proxies {
		used[p.InboundPort] = true
	}
	for port := r.ports.Min; port <= r.ports.Max; port++ {
		if !used[port] {
			return port
		}
	}
	return 0
}

func (r *egressRepo) DeleteEgressProxy(_ context.Context, id string, a store.Audit) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastAudit = a
	if _, ok := r.proxies[id]; !ok {
		return store.ErrNotFound
	}
	for _, u := range r.upstreams {
		if u.EgressProxyID != nil && *u.EgressProxyID == id {
			return &store.FieldError{Path: "egress_proxy_id", Kind: store.ErrConflict, Reason: "upstream " + u.ID + " still binds this profile"}
		}
	}
	delete(r.proxies, id)
	filtered := r.order[:0]
	for _, existing := range r.order {
		if existing != id {
			filtered = append(filtered, existing)
		}
	}
	r.order = filtered
	return nil
}

func (r *egressRepo) SetEgressProxyState(_ context.Context, id string, state store.EgressProxyState, _ store.Audit) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.proxies[id]
	if !ok {
		return store.ErrNotFound
	}
	p.RuntimeState, p.SwapPhase, p.LastError = state.RuntimeState, state.SwapPhase, state.LastError
	r.proxies[id] = p
	return nil
}

func (r *egressRepo) EgressProxyCredential(context.Context, string, []byte) (json.RawMessage, error) {
	return nil, store.ErrNotFound
}

func (r *egressRepo) SaveUpstream(ctx context.Context, u store.Upstream, create bool, a store.Audit) (store.Upstream, error) {
	if u.EgressProxyID != nil {
		if _, err := uuid.Parse(*u.EgressProxyID); err != nil {
			return store.Upstream{}, &store.FieldError{Path: "egress_proxy_id", Kind: store.ErrReference, Reason: "must be a UUID"}
		}
		r.mu.Lock()
		_, ok := r.proxies[*u.EgressProxyID]
		r.mu.Unlock()
		if !ok {
			return store.Upstream{}, &store.FieldError{Path: "egress_proxy_id", Kind: store.ErrReference, Reason: "referenced egress proxy does not exist"}
		}
	}
	return r.fakeRepo.SaveUpstream(ctx, u, create, a)
}

// Delete handles the egress kind so the shared delete path stays in one place.
func (r *egressRepo) Delete(ctx context.Context, kind, id string, a store.Audit) error {
	if kind == "egress_proxy" {
		return r.DeleteEgressProxy(ctx, id, a)
	}
	return r.fakeRepo.Delete(ctx, kind, id, a)
}

type egressHarness struct {
	h      http.Handler
	repo   *egressRepo
	cfg    config.Config
	cookie *http.Cookie
}

func newEgressHarness(t *testing.T, opts EgressOptions) egressHarness {
	t.Helper()
	// Private upstreams are allowed so the binding tests do not need DNS.
	cfg := config.Config{PublicOrigin: "https://admin.example", SessionSecret: strings.Repeat("s", 32), MasterKey: make([]byte, 32),
		AllowPrivateUpstreams: true, XrayProxyPortMin: 15000, XrayProxyPortMax: 15999}
	repo := newEgressRepo()
	token, _ := auth.EncodeSession([]byte(cfg.SessionSecret), auth.Session{AdminID: repo.admin.ID, Expires: time.Now().Add(time.Hour).Unix(), Version: 1})
	if opts.NetworkReady == nil {
		opts.NetworkReady = func(context.Context) error { return nil }
	}
	return egressHarness{h: NewRouterWithEgress(cfg, repo, opts), repo: repo, cfg: cfg,
		cookie: &http.Cookie{Name: auth.CookieName, Value: token}}
}

func egressBody(extra string) string {
	return `{"name":"office-egress","protocol":"socks5","server":"proxy.example.com","port":1080,"enabled":false` + extra + `}`
}

func TestEgressProxyCRUD(t *testing.T) {
	e := newEgressHarness(t, EgressOptions{})

	if w := request(e.h, e.cfg, nil, "GET", "/api/egress-proxies", ""); w.Code != 401 {
		t.Fatalf("unauthenticated list: %d", w.Code)
	}
	w := request(e.h, e.cfg, e.cookie, "GET", "/api/egress-proxies", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"data":[]`) {
		t.Fatalf("empty list: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/egress-proxies", "GET", w)

	w = request(e.h, e.cfg, e.cookie, "POST", "/api/egress-proxies", egressBody(`,"transport":{"network":"ws","path":"/ray"},"tls":{"security":"tls","server_name":"proxy.example.com"},"credentials":{"username":"u","password":"p"}`))
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/egress-proxies", "POST", w)
	body := w.Body.String()
	for _, forbidden := range []string{"password", `"p"`, "\"u\"", "ciphertext"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("create echoed credentials (%s): %s", forbidden, body)
		}
	}
	if !strings.Contains(body, `"has_credentials":true`) || !strings.Contains(body, `"inbound_port":15000`) || !strings.Contains(body, `"runtime_state":"pending"`) {
		t.Fatalf("create shape: %s", body)
	}
	if e.repo.lastAudit.Action != "egress_proxy.create" || e.repo.lastAudit.TargetType != "egress_proxy" {
		t.Fatalf("create audit: %+v", e.repo.lastAudit)
	}
	if strings.Contains(string(e.repo.lastAudit.Payload), "password") {
		t.Fatalf("audit payload leaked credentials: %s", e.repo.lastAudit.Payload)
	}
	p, err := e.repo.EgressProxy(context.Background(), e.repo.order[0])
	if err != nil || len(p.Ciphertext) == 0 || len(p.Nonce) != 12 {
		t.Fatalf("stored credentials: %+v %v", p, err)
	}
	id := p.ID

	w = request(e.h, e.cfg, e.cookie, "GET", "/api/egress-proxies/"+id, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"network":"ws"`) {
		t.Fatalf("get: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/egress-proxies/{proxy_id}", "GET", w)

	// credentials omitted keeps the stored ciphertext; transport omitted keeps
	// the stored options.
	w = request(e.h, e.cfg, e.cookie, "PUT", "/api/egress-proxies/"+id, `{"name":"office-egress","protocol":"socks5","server":"proxy.example.com","port":1080,"enabled":false}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"has_credentials":true`) || !strings.Contains(w.Body.String(), `"network":"ws"`) {
		t.Fatalf("update keeping credentials: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/egress-proxies/{proxy_id}", "PUT", w)

	w = request(e.h, e.cfg, e.cookie, "PUT", "/api/egress-proxies/"+id, `{"name":"office-egress","protocol":"socks5","server":"proxy.example.com","port":1080,"enabled":false,"credentials":null}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"has_credentials":false`) {
		t.Fatalf("clearing credentials: %d %s", w.Code, w.Body.String())
	}
	if p, _ = e.repo.EgressProxy(context.Background(), id); len(p.Ciphertext) != 0 {
		t.Fatalf("ciphertext survived a clear: %+v", p)
	}

	w = request(e.h, e.cfg, e.cookie, "GET", "/api/egress-proxies/"+uuid.NewString(), "")
	if w.Code != 404 {
		t.Fatalf("unknown profile: %d %s", w.Code, w.Body.String())
	}

	w = request(e.h, e.cfg, e.cookie, "DELETE", "/api/egress-proxies/"+id, "")
	if w.Code != 204 || w.Body.Len() != 0 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if e.repo.lastAudit.Action != "egress_proxy.delete" {
		t.Fatalf("delete audit: %+v", e.repo.lastAudit)
	}
	if w = request(e.h, e.cfg, e.cookie, "DELETE", "/api/egress-proxies/"+id, ""); w.Code != 404 {
		t.Fatalf("delete twice: %d %s", w.Code, w.Body.String())
	}
}

func TestEgressProxyClosedFields(t *testing.T) {
	e := newEgressHarness(t, EgressOptions{})
	cases := []struct {
		name, body, path string
	}{
		{"unknown protocol", `{"name":"p","protocol":"socks4","server":"h","port":1080,"enabled":false}`, "protocol"},
		{"unknown transport network", egressBody(`,"transport":{"network":"quic"}`), "transport.network"},
		{"unknown transport field", egressBody(`,"transport":{"network":"ws","sni":"x"}`), "transport.sni"},
		{"unknown tls mode", egressBody(`,"tls":{"security":"xtls"}`), "tls.security"},
		{"unknown tls field", egressBody(`,"tls":{"security":"tls","pin_sha256":"x"}`), "tls.pin_sha256"},
		{"reality without key", egressBody(`,"tls":{"security":"reality","server_name":"www.example.com"}`), "tls.public_key"},
		{"port range", `{"name":"p","protocol":"socks5","server":"h","port":70000,"enabled":false}`, "port"},
		{"missing server", `{"name":"p","protocol":"socks5","server":"","port":1080,"enabled":false}`, "server"},
		{"transport not object", egressBody(`,"transport":[]`), "transport"},
		{"credentials not object", egressBody(`,"credentials":"secret"`), "credentials"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := request(e.h, e.cfg, e.cookie, "POST", "/api/egress-proxies", tc.body)
			if w.Code != 422 || !strings.Contains(w.Body.String(), `"path":"`+tc.path+`"`) {
				t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
			}
			contract(t, "/api/egress-proxies", "POST", w)
		})
	}
	if w := request(e.h, e.cfg, e.cookie, "POST", "/api/egress-proxies", `{"name":"p","protocol":"socks5","server":"h","port":1080}`); w.Code != 422 || !strings.Contains(w.Body.String(), `"path":"enabled"`) {
		t.Fatalf("missing enabled: %d %s", w.Code, w.Body.String())
	}
	if len(e.repo.proxies) != 0 {
		t.Fatalf("invalid input created profiles: %+v", e.repo.proxies)
	}
}

func TestEgressProxyConflicts(t *testing.T) {
	e := newEgressHarness(t, EgressOptions{})
	if w := request(e.h, e.cfg, e.cookie, "POST", "/api/egress-proxies", egressBody("")); w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	w := request(e.h, e.cfg, e.cookie, "POST", "/api/egress-proxies", egressBody(`,"name":"office-egress"`))
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"path":"name"`) {
		t.Fatalf("duplicate name: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/egress-proxies", "POST", w)

	id := e.repo.order[0]
	upstreamID := uuid.NewString()
	e.repo.upstreams[upstreamID] = store.Upstream{ID: upstreamID, Name: "bound", EgressProxyID: &id}
	w = request(e.h, e.cfg, e.cookie, "DELETE", "/api/egress-proxies/"+id, "")
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"path":"egress_proxy_id"`) {
		t.Fatalf("delete referenced: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/egress-proxies/{proxy_id}", "DELETE", w)

	delete(e.repo.upstreams, upstreamID)
	e.repo.exhaust = true
	w = request(e.h, e.cfg, e.cookie, "POST", "/api/egress-proxies", egressBody(`,"name":"second"`))
	if w.Code != 409 || !strings.Contains(w.Body.String(), "port range is exhausted") {
		t.Fatalf("port exhaustion: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/egress-proxies", "POST", w)
}

func TestEgressProxyEnableRequiresNetwork(t *testing.T) {
	networkErr := errors.New("CORE_NETWORKS does not include token_ne_egress")
	e := newEgressHarness(t, EgressOptions{NetworkReady: func(context.Context) error { return networkErr }})

	w := request(e.h, e.cfg, e.cookie, "POST", "/api/egress-proxies", `{"name":"p","protocol":"socks5","server":"h","port":1080,"enabled":true}`)
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"path":"enabled"`) {
		t.Fatalf("enable without network: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/egress-proxies", "POST", w)
	if len(e.repo.proxies) != 0 {
		t.Fatal("a refused enable must not create the profile")
	}

	// Disabled creation needs no network, but a later enable does.
	if w = request(e.h, e.cfg, e.cookie, "POST", "/api/egress-proxies", egressBody("")); w.Code != 201 {
		t.Fatalf("disabled create: %d %s", w.Code, w.Body.String())
	}
	id := e.repo.order[0]
	if w = request(e.h, e.cfg, e.cookie, "PUT", "/api/egress-proxies/"+id, `{"name":"office-egress","protocol":"socks5","server":"proxy.example.com","port":1080,"enabled":true}`); w.Code != 409 {
		t.Fatalf("enable existing: %d %s", w.Code, w.Body.String())
	}
}

func TestEgressProxyPortStableAcrossToggle(t *testing.T) {
	e := newEgressHarness(t, EgressOptions{})
	if w := request(e.h, e.cfg, e.cookie, "POST", "/api/egress-proxies", egressBody("")); w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	id := e.repo.order[0]
	created := e.repo.proxies[id].InboundPort

	for _, enabled := range []string{"true", "false", "true"} {
		body := `{"name":"office-egress","protocol":"socks5","server":"proxy.example.com","port":1080,"enabled":` + enabled + `}`
		w := request(e.h, e.cfg, e.cookie, "PUT", "/api/egress-proxies/"+id, body)
		if w.Code != 200 {
			t.Fatalf("toggle %s: %d %s", enabled, w.Code, w.Body.String())
		}
	}
	if got := e.repo.proxies[id].InboundPort; got != created {
		t.Fatalf("inbound port changed on toggle: %d then %d", created, got)
	}
	// A second profile takes the next port; the first profile still holds its.
	if w := request(e.h, e.cfg, e.cookie, "POST", "/api/egress-proxies", egressBody(`,"name":"second"`)); w.Code != 201 {
		t.Fatalf("second create: %d %s", w.Code, w.Body.String())
	}
	second := e.repo.proxies[e.repo.order[1]].InboundPort
	if second == created || second != created+1 {
		t.Fatalf("second profile port: %d after %d", second, created)
	}
}

func TestUpstreamEgressBinding(t *testing.T) {
	e := newEgressHarness(t, EgressOptions{})
	base := `,"kind":"api_key","base_url":"https://api.example/v1","auth_style":"bearer","api_key":"secret-value-1234","enabled":true,"models":[]`

	// Dangling reference is a field error with the request path.
	dangling := uuid.NewString()
	w := request(e.h, e.cfg, e.cookie, "POST", "/api/upstreams", `{"name":"u"`+base+`,"egress_proxy_id":"`+dangling+`"}`)
	if w.Code != 422 || !strings.Contains(w.Body.String(), `"path":"egress_proxy_id"`) {
		t.Fatalf("dangling binding: %d %s", w.Code, w.Body.String())
	}

	// Direct binding is the default and is echoed as null.
	w = request(e.h, e.cfg, e.cookie, "POST", "/api/upstreams", `{"name":"u"`+base+`}`)
	if w.Code != 201 || !strings.Contains(w.Body.String(), `"egress_proxy_id":null`) {
		t.Fatalf("direct create: %d %s", w.Code, w.Body.String())
	}
	directID := w.Body.String()
	upstreamID := directID[strings.Index(directID, `"id":"`)+6:]
	upstreamID = upstreamID[:strings.Index(upstreamID, `"`)]

	if w = request(e.h, e.cfg, e.cookie, "POST", "/api/egress-proxies", egressBody(`,"name":"bound-target"`)); w.Code != 201 {
		t.Fatalf("profile create: %d %s", w.Code, w.Body.String())
	}
	proxyID := e.repo.order[0]
	w = request(e.h, e.cfg, e.cookie, "PUT", "/api/upstreams/"+upstreamID, `{"name":"u"`+base+`,"egress_proxy_id":"`+proxyID+`"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"egress_proxy_id":"`+proxyID+`"`) {
		t.Fatalf("bind profile: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/upstreams/{upstream_id}", "PUT", w)

	w = request(e.h, e.cfg, e.cookie, "GET", "/api/upstreams/"+upstreamID, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"egress_proxy_id":"`+proxyID+`"`) {
		t.Fatalf("echo binding: %d %s", w.Code, w.Body.String())
	}
	contract(t, "/api/upstreams/{upstream_id}", "GET", w)

	// null unbinds.
	w = request(e.h, e.cfg, e.cookie, "PUT", "/api/upstreams/"+upstreamID, `{"name":"u"`+base+`,"egress_proxy_id":null}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"egress_proxy_id":null`) {
		t.Fatalf("unbind: %d %s", w.Code, w.Body.String())
	}

	// A non-UUID value is a validation failure, not a storage error.
	w = request(e.h, e.cfg, e.cookie, "PUT", "/api/upstreams/"+upstreamID, `{"name":"u"`+base+`,"egress_proxy_id":"not-a-uuid"}`)
	if w.Code != 422 || !strings.Contains(w.Body.String(), `"path":"egress_proxy_id"`) {
		t.Fatalf("invalid uuid: %d %s", w.Code, w.Body.String())
	}
}

func TestEgressProxyTestClassification(t *testing.T) {
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/forbidden" {
			w.WriteHeader(403)
			return
		}
		w.WriteHeader(204)
	}))
	defer probe.Close()

	cases := []struct {
		name, target string
		transport    func(store.EgressProxy) (http.RoundTripper, error)
		wantOK       bool
		wantClass    string
	}{
		{"success", probe.URL, directTransport, true, ""},
		{"non-2xx", probe.URL + "/forbidden", directTransport, false, "http_status"},
		{"dns", probe.URL, func(store.EgressProxy) (http.RoundTripper, error) {
			return roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "probe.invalid"}}
			}), nil
		}, false, "dns"},
		{"proxy handshake", probe.URL, func(store.EgressProxy) (http.RoundTripper, error) {
			return roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, &net.OpError{Op: "proxyconnect", Net: "tcp", Err: errors.New("socks connect tcp: unexpected status")}
			}), nil
		}, false, "proxy_handshake"},
		{"timeout", probe.URL, func(store.EgressProxy) (http.RoundTripper, error) {
			return roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, &net.OpError{Op: "dial", Net: "tcp", Err: timeoutError{}}
			}), nil
		}, false, "timeout"},
		{"connect", probe.URL, func(store.EgressProxy) (http.RoundTripper, error) {
			return roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
			}), nil
		}, false, "connect"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEgressHarness(t, EgressOptions{ProbeURL: tc.target, Transport: tc.transport})
			if w := request(e.h, e.cfg, e.cookie, "POST", "/api/egress-proxies", egressBody("")); w.Code != 201 {
				t.Fatalf("create: %d %s", w.Code, w.Body.String())
			}
			id := e.repo.order[0]
			w := request(e.h, e.cfg, e.cookie, "POST", "/api/egress-proxies/"+id+"/test", "")
			if w.Code != 200 {
				t.Fatalf("test: %d %s", w.Code, w.Body.String())
			}
			contract(t, "/api/egress-proxies/{proxy_id}/test", "POST", w)
			body := w.Body.String()
			if tc.wantOK {
				if !strings.Contains(body, `"ok":true`) || !strings.Contains(body, `"error_class":null`) || !strings.Contains(body, `"status":204`) {
					t.Fatalf("success shape: %s", body)
				}
			} else {
				if !strings.Contains(body, `"ok":false`) || !strings.Contains(body, `"error_class":"`+tc.wantClass+`"`) {
					t.Fatalf("%s classification: %s", tc.wantClass, body)
				}
			}
			if strings.Contains(body, "proxy.example.com") || strings.Contains(body, "password") {
				t.Fatalf("test response leaked proxy coordinates: %s", body)
			}
			tested := false
			for _, audit := range e.repo.audits {
				tested = tested || audit.Action == "egress_proxy.test"
			}
			if !tested {
				t.Fatalf("missing test audit: %+v", e.repo.audits)
			}
		})
	}
	if w := request(newEgressHarness(t, EgressOptions{}).h, config.Config{PublicOrigin: "https://admin.example", SessionSecret: strings.Repeat("s", 32)}, nil, "GET", "/api/egress-proxies", ""); w.Code != 401 {
		t.Fatalf("test endpoint must be protected: %d", w.Code)
	}
}

// directTransport proves the probe result mapping without Xray: it reaches the
// httptest target directly while keeping the profile's inbound port out of the
// path.
func directTransport(store.EgressProxy) (http.RoundTripper, error) {
	return http.DefaultTransport, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestCheckCoreEgressNetwork(t *testing.T) {
	ctx := context.Background()
	if err := checkCoreEgressNetwork(ctx, "token_ne_edge,token_ne_core_control", "unix:///nonexistent.sock"); err == nil || !strings.Contains(err.Error(), "CORE_NETWORKS") {
		t.Fatalf("network absent from CORE_NETWORKS: %v", err)
	}
	// CORE_NETWORKS lists it, but no Docker socket is reachable: the running
	// container check cannot be proven, so enabling must be refused.
	if err := checkCoreEgressNetwork(ctx, "token_ne_edge,token_ne_egress", "unix:///nonexistent.sock"); err == nil {
		t.Fatal("unreachable Docker must not pass the container network check")
	}
	if err := checkCoreEgressNetwork(ctx, "token_ne_edge,token_ne_egress", "ftp://socket"); err == nil {
		t.Fatal("unsupported Docker host must not pass")
	}
}
