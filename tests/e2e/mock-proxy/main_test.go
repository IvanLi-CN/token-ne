package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// testProxy builds a proxy whose alias table sends every test hostname to one
// local echo listener, so a SOCKS5 round trip needs no external network.
func testProxy(t *testing.T) (*proxy, string) {
	t.Helper()
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = echo.Close() })
	go func() {
		for {
			conn, err := echo.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	// Every mapped test host points at the echo listener; the alias table is
	// only consulted for the reserved names the tests use.
	resolved := map[string]string{"upstream.test": echo.Addr().String()}
	p := &proxy{recorder: &recorder{}, resolver: &resolver{aliases: resolved}, dialer: &net.Dialer{Timeout: 5 * time.Second}, handshake: 5 * time.Second}
	return p, echo.Addr().String()
}

func listen(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go handle(conn)
		}
	}()
	return listener.Addr().String()
}

func dialSocks(t *testing.T, address string, atyp byte, host string, port int, offerNoAuth bool) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	method := byte(0x02)
	if offerNoAuth {
		method = socksNoAuth
	}
	if _, err = conn.Write([]byte{socksVersion, 0x01, method}); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 2)
	if _, err = io.ReadFull(conn, reply); err != nil {
		t.Fatal(err)
	}
	if !offerNoAuth {
		if reply[1] != 0xFF {
			t.Fatalf("credential-only greeting accepted: %v", reply)
		}
		return nil
	}
	if reply[0] != socksVersion || reply[1] != socksNoAuth {
		t.Fatalf("unexpected greeting reply: %v", reply)
	}
	request := []byte{socksVersion, socksCmdConn, 0x00, atyp}
	switch atyp {
	case atypDomain:
		request = append(request, byte(len(host)))
		request = append(request, host...)
	case atypIPv4:
		request = append(request, net.ParseIP(host).To4()...)
	}
	request = binary.BigEndian.AppendUint16(request, uint16(port))
	if _, err = conn.Write(request); err != nil {
		t.Fatal(err)
	}
	head := make([]byte, 4)
	if _, err = io.ReadFull(conn, head); err != nil {
		t.Fatal(err)
	}
	rest := make([]byte, 6)
	if _, err = io.ReadFull(conn, rest); err != nil {
		t.Fatal(err)
	}
	if head[1] != repSucceeded {
		t.Fatalf("SOCKS5 reply code %d", head[1])
	}
	return conn
}

// fr25 item 1: the proxy must see the upstream hostname, not a pre-resolved IP.
func TestSOCKS5RecordsDomainTargetAndTunnels(t *testing.T) {
	p, _ := testProxy(t)
	address := listen(t, p.serveSOCKS5)
	conn := dialSocks(t, address, atypDomain, "upstream.test", 8080, true)
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 4)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatal(err)
	}
	if string(reply) != "ping" {
		t.Fatalf("tunnel payload: %q", reply)
	}
	records := p.recorder.snapshot()
	if len(records) != 1 {
		t.Fatalf("expected one connection, got %d", len(records))
	}
	got := records[0]
	if got.Protocol != "socks5" || got.ATYP != "domain" || got.TargetHost != "upstream.test" || got.TargetPort != 8080 {
		t.Fatalf("unexpected capture: %+v", got)
	}
	if got.RawTarget != "upstream.test:8080" {
		t.Fatalf("raw target: %q", got.RawTarget)
	}
	if !got.NoAuth {
		t.Fatal("no-auth greeting must be recorded")
	}
	if !got.Connected {
		t.Fatalf("tunnel was not recorded as connected: %+v", got)
	}
}

// An IP destination proves the capture distinguishes local resolution from
// remote DNS; the lane asserts on atyp to catch a regression that resolves early.
func TestSOCKS5RecordsIPv4Target(t *testing.T) {
	p, _ := testProxy(t)
	address := listen(t, p.serveSOCKS5)
	echoHost, echoPort, err := net.SplitHostPort(p.resolver.aliases["upstream.test"])
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(echoPort)
	if err != nil {
		t.Fatal(err)
	}
	dialSocks(t, address, atypIPv4, echoHost, port, true)
	records := p.recorder.snapshot()
	if len(records) != 1 || records[0].ATYP != "ipv4" || records[0].TargetHost != echoHost {
		t.Fatalf("unexpected capture: %+v", records)
	}
}

// A client offering only a username/password method must be refused; the
// internal contract is a no-auth listener and silently accepting credentials
// would hide a credential leak.
func TestSOCKS5RefusesCredentialOnlyClient(t *testing.T) {
	p, _ := testProxy(t)
	address := listen(t, p.serveSOCKS5)
	dialSocks(t, address, atypDomain, "upstream.test", 80, false)
	if records := p.recorder.snapshot(); len(records) != 0 {
		t.Fatalf("refused greeting recorded a connection: %+v", records)
	}
}

// An alias miss reaches an unresolvable reserved name and must still be
// captured with connected=false: "no connection" is evidence, not an error.
func TestSOCKS5RecordsFailedDial(t *testing.T) {
	p := &proxy{recorder: &recorder{}, resolver: &resolver{aliases: map[string]string{}}, dialer: &net.Dialer{Timeout: 2 * time.Second}, handshake: 5 * time.Second}
	address := listen(t, p.serveSOCKS5)
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.Write([]byte{socksVersion, 0x01, socksNoAuth}); err != nil {
		t.Fatal(err)
	}
	greeting := make([]byte, 2)
	if _, err = io.ReadFull(conn, greeting); err != nil {
		t.Fatal(err)
	}
	request := []byte{socksVersion, socksCmdConn, 0x00, atypDomain, byte(len("unmapped.invalid"))}
	request = append(request, "unmapped.invalid"...)
	request = binary.BigEndian.AppendUint16(request, 443)
	if _, err = conn.Write(request); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	_, _ = io.ReadFull(conn, reply)
	records := p.recorder.snapshot()
	if len(records) != 1 || records[0].Connected {
		t.Fatalf("failed dial not captured: %+v", records)
	}
	if records[0].TargetHost != "unmapped.invalid" || records[0].TargetPort != 443 {
		t.Fatalf("unexpected target: %+v", records[0])
	}
}

// The HTTP CONNECT listener is the second supported shape of the third-party
// proxy; it must record the same host name and refuse a credential-bearing
// CONNECT.
func TestHTTPConnectRecordsTarget(t *testing.T) {
	p, _ := testProxy(t)
	address := listen(t, p.serveHTTPConnect)
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = io.WriteString(conn, "CONNECT upstream.test:8443 HTTP/1.1\r\nHost: upstream.test:8443\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "200") {
		t.Fatalf("CONNECT reply: %q", line)
	}
	// Consume the rest of the response head so the tunnel payload starts clean.
	for {
		remaining, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if remaining == "\r\n" || remaining == "\n" {
			break
		}
	}
	if _, err = conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	echo := make([]byte, 5)
	if _, err = io.ReadFull(reader, echo); err != nil {
		t.Fatal(err)
	}
	if string(echo) != "hello" {
		t.Fatalf("CONNECT tunnel payload: %q", echo)
	}
	records := p.recorder.snapshot()
	if len(records) != 1 || records[0].Protocol != "http_connect" || records[0].TargetHost != "upstream.test" || records[0].TargetPort != 8443 {
		t.Fatalf("unexpected capture: %+v", records)
	}
	if !records[0].NoAuth {
		t.Fatal("credential-free CONNECT must record no_auth=true")
	}
}

func TestControlExposesAndResetsCapture(t *testing.T) {
	p, _ := testProxy(t)
	address := listen(t, p.serveSOCKS5)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /__proxy/connections", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"connections": p.recorder.snapshot()})
	})
	mux.HandleFunc("POST /__proxy/reset", func(w http.ResponseWriter, _ *http.Request) {
		p.recorder.reset()
		writeJSON(w, 200, map[string]any{"status": "ok"})
	})
	control := listen(t, func(conn net.Conn) {
		server := &http.Server{Handler: mux}
		_ = server.Serve(&oneConnListener{conn: conn})
	})
	dialSocks(t, address, atypDomain, "upstream.test", 9000, true)

	response, err := http.Get("http://" + control + "/__proxy/connections")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Connections []Connection `json:"connections"`
	}
	if err = json.NewDecoder(response.Body).Decode(&capture); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if len(capture.Connections) != 1 || capture.Connections[0].TargetHost != "upstream.test" {
		t.Fatalf("capture endpoint: %+v", capture.Connections)
	}
	reset, err := http.Post("http://"+control+"/__proxy/reset", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = reset.Body.Close()
	if len(p.recorder.snapshot()) != 0 {
		t.Fatal("reset did not clear the capture")
	}
}

// oneConnListener serves exactly one connection so the test control handler
// needs no port lifecycle of its own.
type oneConnListener struct {
	conn net.Conn
	done bool
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	if l.done {
		return nil, net.ErrClosed
	}
	l.done = true
	return l.conn, nil
}
func (l *oneConnListener) Close() error   { return nil }
func (l *oneConnListener) Addr() net.Addr { return l.conn.LocalAddr() }

func TestParseResolverRejectsMalformed(t *testing.T) {
	for _, raw := range []string{"upstream.test", "upstream.test=127.0.0.1", "=127.0.0.1:80"} {
		if _, err := parseResolver(raw); err == nil {
			t.Fatalf("accepted malformed resolver %q", raw)
		}
	}
	r, err := parseResolver(" Upstream.Test = 127.0.0.1:8080 , other.test=127.0.0.1:9")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.dialAddress("upstream.test", 443); got != "127.0.0.1:8080" {
		t.Fatalf("alias lookup: %q", got)
	}
	if got := r.dialAddress("missing.test", 443); got != "missing.test:443" {
		t.Fatalf("pass-through: %q", got)
	}
}

// FR-25 items 2 and 3 need the SOCKS5 hop to go away and come back while the
// process keeps running; the endpoint models exactly that.
func TestEndpointStopsAndRestartsInPlace(t *testing.T) {
	p := &proxy{recorder: &recorder{}, resolver: &resolver{aliases: map[string]string{}}, dialer: &net.Dialer{Timeout: 2 * time.Second}, handshake: 2 * time.Second}
	// Bind an ephemeral address first so the restart uses a known-free port.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	_ = probe.Close()
	p.socks = newEndpoint(address, p.serveSOCKS5)
	if err := p.socks.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := net.DialTimeout("tcp", address, time.Second); err != nil {
		t.Fatalf("listener refused a connection while up: %v", err)
	}
	p.socks.Stop()
	deadline := time.Now().Add(2 * time.Second)
	for p.socks.Up() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if p.socks.Up() {
		t.Fatal("listener still reported up after Stop")
	}
	if _, err := net.DialTimeout("tcp", address, 200*time.Millisecond); err == nil {
		t.Fatal("a connection succeeded while the listener was down")
	}
	// The capture outlives the listener: the control surface stays readable.
	p.recorder.add(Connection{Protocol: "socks5", ATYP: "domain", TargetHost: "upstream.test", TargetPort: 80})
	if len(p.recorder.snapshot()) != 1 {
		t.Fatal("capture lost records across a listener stop")
	}
	if err := p.socks.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := net.DialTimeout("tcp", address, time.Second); err != nil {
		t.Fatalf("restarted listener refused a connection: %v", err)
	}
	p.socks.Stop()
}

// The usage capture is how the lane sees the core's egress attribution; a batch
// must round-trip verbatim so a redacted/lost field is visible.
func TestUsageBatchCapture(t *testing.T) {
	p := &proxy{recorder: &recorder{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/v1/usage/batch", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil || !json.Valid(body) {
			w.WriteHeader(400)
			return
		}
		headers := map[string]string{"authorization": r.Header.Get("Authorization")}
		p.recorder.addBatch(headers, body)
		w.WriteHeader(202)
	})
	mux.HandleFunc("GET /__proxy/usage", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"batches": p.recorder.batchSnapshot()})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	payload := `{"events":[{"event_id":"e1","egress_mode":"proxy","egress_proxy_id":"p1"}]}`
	response, err := http.Post(server.URL+"/internal/v1/usage/batch", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 202 {
		t.Fatalf("ingest answer: %d", response.StatusCode)
	}
	_ = response.Body.Close()

	capture, err := http.Get(server.URL + "/__proxy/usage")
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Body.Close()
	var got struct {
		Batches []Batch `json:"batches"`
	}
	if err = json.NewDecoder(capture.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Batches) != 1 {
		t.Fatalf("batches: %d", len(got.Batches))
	}
	var event struct {
		Events []struct {
			EgressMode    string `json:"egress_mode"`
			EgressProxyID string `json:"egress_proxy_id"`
		} `json:"events"`
	}
	if err = json.Unmarshal(got.Batches[0].Body, &event); err != nil {
		t.Fatal(err)
	}
	if len(event.Events) != 1 || event.Events[0].EgressMode != "proxy" || event.Events[0].EgressProxyID != "p1" {
		t.Fatalf("captured body lost the attribution: %+v", event)
	}
	// A reset must clear both captures so a later assertion cannot read a stale
	// batch from an earlier phase.
	p.recorder.reset()
	if len(p.recorder.batchSnapshot()) != 0 {
		t.Fatal("reset did not clear the usage capture")
	}
}
