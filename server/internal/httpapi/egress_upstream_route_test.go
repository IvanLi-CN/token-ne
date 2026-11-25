package httpapi

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/auth"
	"github.com/IvanLi-CN/token-ne/server/internal/config"
	"github.com/IvanLi-CN/token-ne/server/internal/store"
	"github.com/google/uuid"
)

// The contract says a bound upstream's requests, catalog sync and test all leave
// through its profile. Catalog sync and the connection test used to build their
// own `Proxy: nil` transport, which bypassed the binding silently.
//
// These tests use a real SOCKS5 server as the "Xray inbound" and a real upstream
// that the tunnel connects to, so the assertion is about where the bytes went,
// not about which struct field was set.
func TestBoundUpstreamsRouteThroughTheirProfile(t *testing.T) {
	recorder := startSocks5Recorder(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"mock-model","object":"model","owned_by":"test"}]}`))
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)

	profileID := uuid.NewString()
	repo := newEgressRepo()
	repo.proxies[profileID] = store.EgressProxy{ID: profileID, Name: "bound", Protocol: "socks5",
		Server: "proxy.example.test", Port: 1080, Enabled: true, Effective: true, RuntimeState: "ready", InboundPort: recorder.port}
	key, nonce, err := auth.Encrypt(make([]byte, 32), "upstream-key")
	if err != nil {
		t.Fatal(err)
	}
	up := store.Upstream{ID: uuid.NewString(), Name: "bound-upstream", Kind: "api_key", BaseURL: "http://" + target.Host,
		AuthStyle: "bearer", Enabled: true, Connect: 5000, FirstByte: 5000, Idle: 5000,
		Ciphertext: key, Nonce: nonce, EgressProxyID: &profileID}
	repo.upstreams[up.ID] = up

	cfg := config.Config{PublicOrigin: "https://admin.example", SessionSecret: strings.Repeat("s", 32),
		MasterKey: make([]byte, 32), AllowPrivateUpstreams: true, XrayProxyPortMin: 15000, XrayProxyPortMax: 15999}
	t.Setenv(egressProxyHostEnv, "127.0.0.1")
	api := newAPI(cfg, repo)

	models, err := api.fetchUpstreamModels(context.Background(), up)
	if err != nil {
		t.Fatalf("catalog sync through the bound profile failed: %v", err)
	}
	if len(models) != 1 || models[0].ID != "mock-model" {
		t.Fatalf("models = %+v", models)
	}
	seen := recorder.hosts()
	if len(seen) == 0 {
		t.Fatal("catalog sync never reached the proxy: it bypassed the binding")
	}
	if seen[0] != target.Hostname() {
		t.Fatalf("proxy saw host %q, want the upstream hostname %q", seen[0], target.Hostname())
	}

	// An unavailable profile must fail instead of going direct.
	disabled := repo.proxies[profileID]
	disabled.Effective = false
	repo.proxies[profileID] = disabled
	recorder.reset()
	if _, err = api.fetchUpstreamModels(context.Background(), up); err == nil {
		t.Fatal("sync succeeded with an unavailable profile: it fell back to direct")
	}
	if seen = recorder.hosts(); len(seen) != 0 {
		t.Fatalf("unavailable profile still reached the proxy: %v", seen)
	}

	// An unbound upstream keeps the direct path and never touches the proxy.
	direct := up
	direct.ID = uuid.NewString()
	direct.EgressProxyID = nil
	if _, err = api.fetchUpstreamModels(context.Background(), direct); err != nil {
		t.Fatalf("direct sync failed: %v", err)
	}
	if seen = recorder.hosts(); len(seen) != 0 {
		t.Fatalf("unbound upstream went through the proxy: %v", seen)
	}
}

// The connection test is the other caller and had the same bypass.
func TestUpstreamTestUsesTheBoundProfile(t *testing.T) {
	recorder := startSocks5Recorder(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)

	profileID := uuid.NewString()
	repo := newEgressRepo()
	repo.proxies[profileID] = store.EgressProxy{ID: profileID, Name: "bound", Protocol: "socks5",
		Server: "proxy.example.test", Port: 1080, Enabled: true, Effective: true, RuntimeState: "ready", InboundPort: recorder.port}
	key, nonce, _ := auth.Encrypt(make([]byte, 32), "upstream-key")
	up := store.Upstream{ID: uuid.NewString(), Name: "bound", Kind: "api_key", BaseURL: "http://" + target.Host,
		AuthStyle: "bearer", Enabled: true, Connect: 5000, FirstByte: 5000, Idle: 5000,
		Ciphertext: key, Nonce: nonce, EgressProxyID: &profileID}
	repo.upstreams[up.ID] = up

	cfg := config.Config{PublicOrigin: "https://admin.example", SessionSecret: strings.Repeat("s", 32),
		MasterKey: make([]byte, 32), AllowPrivateUpstreams: true, XrayProxyPortMin: 15000, XrayProxyPortMax: 15999}
	t.Setenv(egressProxyHostEnv, "127.0.0.1")
	token, _ := auth.EncodeSession([]byte(cfg.SessionSecret), auth.Session{AdminID: repo.admin.ID, Expires: time.Now().Add(time.Hour).Unix(), Version: 1})
	h := NewRouter(cfg, repo)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/upstreams/"+up.ID+"/test", nil)
	r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	r.Header.Set("Origin", cfg.PublicOrigin)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("test endpoint: %d %s", w.Code, w.Body.String())
	}
	if len(recorder.hosts()) == 0 {
		var body map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		t.Fatalf("connection test bypassed the bound profile: %v", body)
	}
}

// socks5Recorder is a no-auth SOCKS5 server that answers each CONNECT by dialling
// the recorded target and piping both directions. It records the requested
// hostname, so a test can tell remote DNS from a locally resolved address.
type socks5Recorder struct {
	port int

	mu    sync.Mutex
	seen  []string
	conns []net.Conn
}

func (s *socks5Recorder) hosts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.seen...)
}

func (s *socks5Recorder) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = nil
}

func (s *socks5Recorder) record(host string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, host)
}

func startSocks5Recorder(t *testing.T) *socks5Recorder {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &socks5Recorder{port: ln.Addr().(*net.TCPAddr).Port}
	t.Cleanup(func() {
		_ = ln.Close()
		s.mu.Lock()
		for _, c := range s.conns {
			_ = c.Close()
		}
		s.mu.Unlock()
	})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
	return s
}

func (s *socks5Recorder) serve(conn net.Conn) {
	defer conn.Close()
	// Greeting: version, nmethods, methods.
	head := make([]byte, 2)
	if _, err := io.ReadFull(conn, head); err != nil {
		return
	}
	if _, err := io.ReadFull(conn, make([]byte, head[1])); err != nil {
		return
	}
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		return
	}
	// Request: version, cmd, rsv, atyp.
	req := make([]byte, 4)
	if _, err := io.ReadFull(conn, req); err != nil {
		return
	}
	var host string
	switch req[3] {
	case 0x01:
		buf := make([]byte, 4)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return
		}
		host = net.IP(buf).String()
	case 0x03:
		size := make([]byte, 1)
		if _, err := io.ReadFull(conn, size); err != nil {
			return
		}
		buf := make([]byte, size[0])
		if _, err := io.ReadFull(conn, buf); err != nil {
			return
		}
		host = string(buf)
	default:
		return
	}
	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBuf); err != nil {
		return
	}
	s.record(host)

	target, err := net.DialTimeout("tcp", net.JoinHostPort(host, itoa(binary.BigEndian.Uint16(portBuf))), 5*time.Second)
	if err != nil {
		_, _ = conn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer target.Close()
	if _, err = conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	s.mu.Lock()
	s.conns = append(s.conns, conn, target)
	s.mu.Unlock()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(target, conn); done <- struct{}{} }()
	go func() { _, _ = io.Copy(conn, target); done <- struct{}{} }()
	<-done
}

func itoa(v uint16) string {
	if v == 0 {
		return "0"
	}
	var buf [5]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
