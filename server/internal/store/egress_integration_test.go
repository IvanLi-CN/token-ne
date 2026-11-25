package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/auth"
	"github.com/google/uuid"
)

// egressFixture is one isolated slice of the egress tables: the profiles it
// created and the upstreams that bind them.
type egressFixture struct {
	store  *Store
	ctx    context.Context
	ids    []string
	ports  PortRange
	audit  Audit
	master []byte
}

func newEgressFixture(t *testing.T) *egressFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	s, e := Open(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.Close)
	f := &egressFixture{store: s, ctx: ctx, ports: PortRange{Min: 16000, Max: 16002},
		audit: Audit{Actor: "tester", Action: "egress_proxy.test", Payload: json.RawMessage(`{}`)}, master: make([]byte, 32)}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, id := range f.ids {
			_, _ = s.Pool.Exec(cleanup, "update upstreams set egress_proxy_id=null where egress_proxy_id=$1", id)
			_, _ = s.Pool.Exec(cleanup, "delete from upstreams where egress_proxy_id=$1", id)
			_, _ = s.Pool.Exec(cleanup, "delete from egress_proxies where id=$1", id)
		}
	})
	f.resetPorts(t)
	return f
}

// resetPorts clears any profile holding a port in this fixture's range so the
// exhaustion and allocation tests see a deterministic pool.
func (f *egressFixture) resetPorts(t *testing.T) {
	t.Helper()
	rows, e := f.store.Pool.Query(f.ctx, "select id::text from egress_proxies where inbound_port between $1 and $2", f.ports.Min, f.ports.Max)
	if e != nil {
		t.Fatal(e)
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			t.Fatal(e)
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if _, e = f.store.Pool.Exec(f.ctx, "update upstreams set egress_proxy_id=null where egress_proxy_id=$1", id); e != nil {
			t.Fatal(e)
		}
		if _, e = f.store.Pool.Exec(f.ctx, "delete from egress_proxies where id=$1", id); e != nil {
			t.Fatal(e)
		}
	}
}

func (f *egressFixture) profile(name string, credentials json.RawMessage) EgressProxy {
	p := EgressProxy{ID: uuid.NewString(), Name: name, Protocol: "socks5", Server: "proxy.example.com", Port: 1080,
		Transport: json.RawMessage(`{"network":"ws"}`), TLS: json.RawMessage(`{"security":"tls"}`)}
	if len(credentials) > 0 {
		ciphertext, nonce, e := auth.Encrypt(f.master, string(credentials))
		if e != nil {
			panic(e)
		}
		p.Ciphertext, p.Nonce = ciphertext, nonce
	}
	f.ids = append(f.ids, p.ID)
	return p
}

func TestEgressProxyCRUDIntegration(t *testing.T) {
	f := newEgressFixture(t)
	p := f.profile("egress-crud-"+uuid.NewString()[:8], json.RawMessage(`{"username":"u","password":"p"}`))

	created, e := f.store.SaveEgressProxy(f.ctx, p, true, f.ports, f.audit)
	if e != nil {
		t.Fatal(e)
	}
	if created.InboundPort != f.ports.Min || created.RuntimeState != "pending" || !created.HasCredentials {
		t.Fatalf("created profile: %+v", created)
	}
	// jsonb normalises formatting, so compare the decoded objects.
	for name, pair := range map[string][2]string{"transport": {string(created.Transport), `{"network":"ws"}`}, "tls": {string(created.TLS), `{"security":"tls"}`}} {
		var got, want any
		if e = json.Unmarshal([]byte(pair[0]), &got); e != nil {
			t.Fatalf("%s option object: %v", name, e)
		}
		if e = json.Unmarshal([]byte(pair[1]), &want); e != nil {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s option object: %s, want %s", name, pair[0], pair[1])
		}
	}

	// Credentials never leave through the JSON shape.
	raw, e := json.Marshal(created)
	if e != nil {
		t.Fatal(e)
	}
	for _, forbidden := range []string{"password", "ciphertext", `"p"`, "credentials_ciphertext"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("JSON leaked credentials (%s): %s", forbidden, raw)
		}
	}

	credential, e := f.store.EgressProxyCredential(f.ctx, created.ID, f.master)
	if e != nil || string(credential) != `{"username":"u","password":"p"}` {
		t.Fatalf("credential decrypt: %s %v", credential, e)
	}

	read, e := f.store.EgressProxy(f.ctx, created.ID)
	if e != nil || read.Name != created.Name || !read.HasCredentials {
		t.Fatalf("read: %+v %v", read, e)
	}
	list, e := f.store.EgressProxies(f.ctx)
	if e != nil || len(list) == 0 {
		t.Fatalf("list: %d %v", len(list), e)
	}

	// Duplicate name is a field conflict, not a bare unique violation.
	duplicate := f.profile(created.Name, nil)
	duplicate.ID = uuid.NewString()
	f.ids = append(f.ids, duplicate.ID)
	var field *FieldError
	if _, e = f.store.SaveEgressProxy(f.ctx, duplicate, true, f.ports, f.audit); !errors.As(e, &field) || !errors.Is(e, ErrConflict) || field.Path != "name" {
		t.Fatalf("duplicate name = %v, want name conflict", e)
	}

	// Disable keeps the inbound port; the update path never reallocates.
	updated := created
	updated.Enabled = false
	updated.Server = "proxy2.example.com"
	after, e := f.store.SaveEgressProxy(f.ctx, updated, false, f.ports, f.audit)
	if e != nil || after.InboundPort != created.InboundPort || after.Server != "proxy2.example.com" {
		t.Fatalf("update: %+v %v", after, e)
	}
	if !after.HasCredentials {
		t.Fatal("update without credentials must keep the stored ciphertext")
	}

	// Clearing credentials removes both halves of the pair.
	cleared := after
	cleared.Ciphertext, cleared.Nonce = nil, nil
	after, e = f.store.SaveEgressProxy(f.ctx, cleared, false, f.ports, f.audit)
	if e != nil || after.HasCredentials {
		t.Fatalf("clear credentials: %+v %v", after, e)
	}
	if _, e = f.store.EgressProxyCredential(f.ctx, after.ID, f.master); !errors.Is(e, ErrNotFound) {
		t.Fatalf("credential after clear = %v, want ErrNotFound", e)
	}

	// Runtime state is written only through the reconciler projection.
	phase := "route_staged"
	message := "xray reconcile failed"
	if e = f.store.SetEgressProxyState(f.ctx, after.ID, EgressProxyState{RuntimeState: "swapping", SwapPhase: &phase, LastError: &message}, f.audit); e != nil {
		t.Fatal(e)
	}
	// Enabled + swapping is effective because the route points at a live outbound.
	after.Enabled = true
	if after, e = f.store.SaveEgressProxy(f.ctx, after, false, f.ports, f.audit); e != nil {
		t.Fatal(e)
	}
	if !after.Effective || after.SwapPhase == nil || *after.SwapPhase != "route_staged" || after.LastError == nil {
		t.Fatalf("effective swapping: %+v", after)
	}
	if e = f.store.SetEgressProxyState(f.ctx, after.ID, EgressProxyState{RuntimeState: "degraded"}, f.audit); e != nil {
		t.Fatal(e)
	}
	if read, e = f.store.EgressProxy(f.ctx, after.ID); e != nil || read.Effective || read.SwapPhase != nil {
		t.Fatalf("degraded is not effective: %+v %v", read, e)
	}

	if e = f.store.DeleteEgressProxy(f.ctx, after.ID, f.audit); e != nil {
		t.Fatal(e)
	}
	if _, e = f.store.EgressProxy(f.ctx, after.ID); !errors.Is(e, ErrNotFound) {
		t.Fatalf("profile after delete = %v, want ErrNotFound", e)
	}
	if e = f.store.DeleteEgressProxy(f.ctx, after.ID, f.audit); !errors.Is(e, ErrNotFound) {
		t.Fatalf("second delete = %v, want ErrNotFound", e)
	}
}

func TestEgressProxyPortAllocationIntegration(t *testing.T) {
	f := newEgressFixture(t)

	first, e := f.store.SaveEgressProxy(f.ctx, f.profile("egress-port-a-"+uuid.NewString()[:8], nil), true, f.ports, f.audit)
	if e != nil {
		t.Fatal(e)
	}
	second, e := f.store.SaveEgressProxy(f.ctx, f.profile("egress-port-b-"+uuid.NewString()[:8], nil), true, f.ports, f.audit)
	if e != nil {
		t.Fatal(e)
	}
	if first.InboundPort != f.ports.Min || second.InboundPort != f.ports.Min+1 {
		t.Fatalf("allocation: %d then %d", first.InboundPort, second.InboundPort)
	}

	// The port survives disable/enable because the update path never allocates.
	for _, enabled := range []bool{true, false, true} {
		p := first
		p.Enabled = enabled
		if p, e = f.store.SaveEgressProxy(f.ctx, p, false, f.ports, f.audit); e != nil {
			t.Fatal(e)
		}
		if p.InboundPort != first.InboundPort {
			t.Fatalf("port changed while toggling enabled: %d then %d", first.InboundPort, p.InboundPort)
		}
	}

	// A third create takes the last port; a fourth exhausts the range.
	third, e := f.store.SaveEgressProxy(f.ctx, f.profile("egress-port-c-"+uuid.NewString()[:8], nil), true, f.ports, f.audit)
	if e != nil {
		t.Fatal(e)
	}
	if third.InboundPort != f.ports.Max {
		t.Fatalf("third port = %d, want %d", third.InboundPort, f.ports.Max)
	}
	over := f.profile("egress-port-d-"+uuid.NewString()[:8], nil)
	if _, e = f.store.SaveEgressProxy(f.ctx, over, true, f.ports, f.audit); !errors.Is(e, ErrEgressPortExhausted) {
		t.Fatalf("exhaustion = %v, want ErrEgressPortExhausted", e)
	}

	// Deleting one profile returns exactly its port to the pool.
	if e = f.store.DeleteEgressProxy(f.ctx, second.ID, f.audit); e != nil {
		t.Fatal(e)
	}
	reused, e := f.store.SaveEgressProxy(f.ctx, over, true, f.ports, f.audit)
	if e != nil || reused.InboundPort != second.InboundPort {
		t.Fatalf("port reuse: %+v %v", reused, e)
	}
}

func TestEgressProxyPortAllocationSerializedIntegration(t *testing.T) {
	f := newEgressFixture(t)
	// Two concurrent creates must not take the same port: the advisory lock
	// serialises allocation inside the insert transaction.
	const workers = 3
	results := make(chan EgressProxy, workers)
	errs := make(chan error, workers)
	for range workers {
		go func() {
			p, e := f.store.SaveEgressProxy(f.ctx, f.profile("egress-race-"+uuid.NewString(), nil), true, f.ports, f.audit)
			results <- p
			errs <- e
		}()
	}
	seen := map[int]bool{}
	for range workers {
		if e := <-errs; e != nil {
			t.Fatal(e)
		}
		port := (<-results).InboundPort
		if seen[port] {
			t.Fatalf("port %d allocated twice", port)
		}
		seen[port] = true
	}
}

func TestEgressProxyDeleteReferencedIntegration(t *testing.T) {
	f := newEgressFixture(t)
	created, e := f.store.SaveEgressProxy(f.ctx, f.profile("egress-bound-"+uuid.NewString()[:8], nil), true, f.ports, f.audit)
	if e != nil {
		t.Fatal(e)
	}
	upstreamID := uuid.NewString()
	if _, e = f.store.Pool.Exec(f.ctx, `insert into upstreams(id,name,base_url,api_key_ciphertext,api_key_nonce,api_key_hint,egress_proxy_id)
values($1,$2,'https://example.test',$3,$4,'hint',$5)`, upstreamID, "egress-bound-up-"+upstreamID[:8], []byte("cipher"), make([]byte, 12), created.ID); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = f.store.Pool.Exec(cleanup, "delete from upstreams where id=$1", upstreamID)
	})

	var field *FieldError
	if e = f.store.DeleteEgressProxy(f.ctx, created.ID, f.audit); !errors.As(e, &field) || !errors.Is(e, ErrConflict) || field.Path != "egress_proxy_id" {
		t.Fatalf("delete referenced = %v, want egress_proxy_id conflict", e)
	}

	// The binding survives even when the profile is disabled, and the store
	// never clears it on disable.
	disabled := created
	disabled.Enabled = false
	if _, e = f.store.SaveEgressProxy(f.ctx, disabled, false, f.ports, f.audit); e != nil {
		t.Fatal(e)
	}
	upstream, e := f.store.Upstream(f.ctx, upstreamID)
	if e != nil || upstream.EgressProxyID == nil || *upstream.EgressProxyID != created.ID {
		t.Fatalf("binding after disable: %+v %v", upstream, e)
	}

	if _, e = f.store.Pool.Exec(f.ctx, "update upstreams set egress_proxy_id=null where id=$1", upstreamID); e != nil {
		t.Fatal(e)
	}
	if e = f.store.DeleteEgressProxy(f.ctx, created.ID, f.audit); e != nil {
		t.Fatalf("unblocked delete: %v", e)
	}
}

func TestUpstreamEgressBindingIntegration(t *testing.T) {
	f := newEgressFixture(t)
	created, e := f.store.SaveEgressProxy(f.ctx, f.profile("egress-up-"+uuid.NewString()[:8], nil), true, f.ports, f.audit)
	if e != nil {
		t.Fatal(e)
	}
	name := "egress-upstream-" + uuid.NewString()[:8]
	upstream := Upstream{ID: uuid.NewString(), Name: name, Kind: "api_key", BaseURL: "https://example.test", AuthStyle: "bearer",
		Hint: "hint", Enabled: true, Connect: 10000, FirstByte: 90000, Idle: 300000,
		Ciphertext: []byte("cipher"), Nonce: make([]byte, 12), EgressProxyID: &created.ID}
	saved, e := f.store.SaveUpstream(f.ctx, upstream, true, f.audit)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = f.store.Pool.Exec(cleanup, "delete from upstreams where id=$1", saved.ID)
	})
	if saved.EgressProxyID == nil || *saved.EgressProxyID != created.ID {
		t.Fatalf("saved binding: %+v", saved)
	}
	if read, e := f.store.Upstream(f.ctx, saved.ID); e != nil || read.EgressProxyID == nil || *read.EgressProxyID != created.ID {
		t.Fatalf("read binding: %+v %v", read, e)
	}

	// Unbinding to direct egress clears the column.
	saved.EgressProxyID = nil
	if saved, e = f.store.SaveUpstream(f.ctx, saved, false, f.audit); e != nil || saved.EgressProxyID != nil {
		t.Fatalf("unbind: %+v %v", saved, e)
	}

	// A dangling reference is a field error, so the API answers 422 with a path
	// instead of a bare constraint failure.
	dangling := uuid.NewString()
	saved.EgressProxyID = &dangling
	var field *FieldError
	if _, e = f.store.SaveUpstream(f.ctx, saved, false, f.audit); !errors.As(e, &field) || !errors.Is(e, ErrReference) || field.Path != "egress_proxy_id" {
		t.Fatalf("dangling reference = %v, want egress_proxy_id reference error", e)
	}
	if read, e := f.store.Upstream(f.ctx, saved.ID); e != nil || read.EgressProxyID != nil {
		t.Fatalf("failed write changed the binding: %+v %v", read, e)
	}
}
