// Deterministic stand-ins for the two things FR-25 needs on the far side of the
// core, all in one process:
//
//  1. an observing proxy (SOCKS5 and HTTP CONNECT) that records, per inbound
//     connection, whether the client handed over a *hostname* (remote DNS,
//     `socks5h` semantics) or a pre-resolved IP — the FR-25 assertion — and
//     whose listener can be stopped and restarted in place so the "proxy down"
//     and "proxy recovered" phases need no process restart;
//  2. a capturing relay in front of the control plane, because the core binds its
//     usage endpoint from the snapshot published at startup. The relay records
//     each usage batch verbatim (the core's egress attribution leaves the process
//     only this way) and forwards it to the real ingest path.
//
// Everything is local. The only DNS step is the static alias table below, so the
// process never reaches anything that is not a loopback target listed in
// MOCK_PROXY_RESOLVE.
package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SOCKS5 wire constants.
const (
	socksVersion = 0x05
	socksNoAuth  = 0x00
	socksCmdConn = 0x01
	atypIPv4     = 0x01
	atypDomain   = 0x03
	atypIPv6     = 0x04

	repSucceeded  = 0x00
	repConnRefuse = 0x05
	repCmdUnsupp  = 0x07
	repAddrUnsupp = 0x08
)

// maxRecorded bounds the captures so a runaway client cannot grow memory. The
// e2e lane makes a handful of connections; the cap exists only for safety.
const maxRecorded = 1000

// Connection is one observed proxy request. `atyp` distinguishes remote DNS
// (`domain`) from a client that resolved the name itself (`ipv4`/`ipv6`), which
// is exactly the FR-25 assertion. `raw_target` is the destination as the client
// wrote it, before the alias table applies.
type Connection struct {
	Sequence    int    `json:"sequence"`
	Protocol    string `json:"protocol"`
	ObservedAt  string `json:"observed_at"`
	ATYP        string `json:"atyp"`
	TargetHost  string `json:"target_host"`
	TargetPort  int    `json:"target_port"`
	RawTarget   string `json:"raw_target"`
	DialAddress string `json:"dial_address,omitempty"`
	Connected   bool   `json:"connected"`
	Error       string `json:"error,omitempty"`
	NoAuth      bool   `json:"no_auth"`
}

// Batch is one captured usage-batch request: the wire body plus the headers the
// egress proof cares about.
type Batch struct {
	Sequence   int               `json:"sequence"`
	ObservedAt string            `json:"observed_at"`
	Headers    map[string]string `json:"headers"`
	Body       json.RawMessage   `json:"body"`
}

type recorder struct {
	mu          sync.Mutex
	sequence    int
	connections []Connection
	batches     []Batch
}

func (r *recorder) add(c Connection) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sequence++
	c.Sequence = r.sequence
	c.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
	r.connections = append(r.connections, c)
	if len(r.connections) > maxRecorded {
		r.connections = r.connections[len(r.connections)-maxRecorded:]
	}
	return c.Sequence
}

// finish records the dial outcome on the already appended entry so an inbound
// connection is observable even when the tunnel never opens.
func (r *recorder) finish(sequence int, dialAddress string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.connections {
		if r.connections[i].Sequence != sequence {
			continue
		}
		r.connections[i].DialAddress = dialAddress
		if err != nil {
			r.connections[i].Connected = false
			r.connections[i].Error = err.Error()
			return
		}
		r.connections[i].Connected = true
		return
	}
}

func (r *recorder) addBatch(headers map[string]string, body []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.batches = append(r.batches, Batch{ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Headers: headers, Body: append(json.RawMessage(nil), body...)})
	if len(r.batches) > maxRecorded {
		r.batches = r.batches[len(r.batches)-maxRecorded:]
	}
}

// snapshot returns the connections. Never nil: an empty capture must serialise as
// [] so a consumer can iterate it without a null check.
func (r *recorder) snapshot() []Connection {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Connection{}, r.connections...)
}

func (r *recorder) batchSnapshot() []Batch {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Batch{}, r.batches...)
}

func (r *recorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.connections = nil
	r.batches = nil
}

// resolver applies the static alias table. A real third-party proxy resolves the
// tunneled hostname; the lane pins that resolution to loopback so no external DNS
// or network is involved. An unlisted host is dialed verbatim, which fails for
// the reserved names the lane uses.
type resolver struct {
	aliases map[string]string
}

func parseResolver(raw string) (*resolver, error) {
	r := &resolver{aliases: map[string]string{}}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		name, address, ok := strings.Cut(entry, "=")
		name, address = strings.TrimSpace(name), strings.TrimSpace(address)
		if !ok || name == "" {
			return nil, fmt.Errorf("invalid MOCK_PROXY_RESOLVE entry %q", entry)
		}
		if _, _, err := net.SplitHostPort(address); err != nil {
			return nil, fmt.Errorf("invalid MOCK_PROXY_RESOLVE address %q", address)
		}
		r.aliases[strings.ToLower(name)] = address
	}
	return r, nil
}

func (r *resolver) dialAddress(host string, port int) string {
	if address, ok := r.aliases[strings.ToLower(host)]; ok {
		return address
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

type proxy struct {
	recorder  *recorder
	resolver  *resolver
	dialer    *net.Dialer
	handshake time.Duration
	// socks and http are the two listener shapes the contract supports. Both are
	// controlled by the capture endpoint so the lane can take the SOCKS5 hop down
	// and bring it back without restarting the process.
	socks *endpoint
	http  *endpoint
	// relayTo is the real control plane; useCaptureRelay forwards every request
	// there after recording the usage batch.
	relayTo string
}

func (p *proxy) dial(host string, port int) (net.Conn, string, error) {
	address := p.resolver.dialAddress(host, port)
	conn, err := p.dialer.Dial("tcp", address)
	return conn, address, err
}

// serveSOCKS5 accepts one client: greeting, one CONNECT, then a raw tunnel.
func (p *proxy) serveSOCKS5(client net.Conn) {
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(p.handshake))
	reader := bufio.NewReader(client)
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil || header[0] != socksVersion {
		return
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(reader, methods); err != nil {
		return
	}
	noAuth := false
	for _, method := range methods {
		if method == socksNoAuth {
			noAuth = true
			break
		}
	}
	// Authentication is refused outright: the frozen contract uses a no-auth
	// internal SOCKS5 listener, so a client offering credentials is a failure,
	// not something to silently accept.
	if !noAuth {
		_, _ = client.Write([]byte{socksVersion, 0xFF})
		return
	}
	if _, err := client.Write([]byte{socksVersion, socksNoAuth}); err != nil {
		return
	}

	request := make([]byte, 4)
	if _, err := io.ReadFull(reader, request); err != nil {
		return
	}
	if request[0] != socksVersion {
		return
	}
	if request[1] != socksCmdConn {
		writeSOCKSReply(client, repCmdUnsupp)
		return
	}
	host, atyp, err := readSOCKSTarget(reader, request[3])
	if err != nil {
		writeSOCKSReply(client, repAddrUnsupp)
		return
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(reader, portBytes); err != nil {
		return
	}
	port := int(binary.BigEndian.Uint16(portBytes))

	sequence := p.recorder.add(Connection{
		Protocol: "socks5", ATYP: atyp, TargetHost: host, TargetPort: port,
		RawTarget: net.JoinHostPort(host, strconv.Itoa(port)), NoAuth: true,
	})
	upstream, address, dialErr := p.dial(host, port)
	p.recorder.finish(sequence, address, dialErr)
	if dialErr != nil {
		writeSOCKSReply(client, repConnRefuse)
		return
	}
	defer upstream.Close()
	if err := writeSOCKSReply(client, repSucceeded); err != nil {
		return
	}
	_ = client.SetDeadline(time.Time{})
	splice(client, upstream)
}

func readSOCKSTarget(reader io.Reader, atyp byte) (string, string, error) {
	switch atyp {
	case atypIPv4:
		raw := make([]byte, 4)
		if _, err := io.ReadFull(reader, raw); err != nil {
			return "", "", err
		}
		return net.IP(raw).String(), "ipv4", nil
	case atypIPv6:
		raw := make([]byte, 16)
		if _, err := io.ReadFull(reader, raw); err != nil {
			return "", "", err
		}
		return net.IP(raw).String(), "ipv6", nil
	case atypDomain:
		length := make([]byte, 1)
		if _, err := io.ReadFull(reader, length); err != nil {
			return "", "", err
		}
		if length[0] == 0 {
			return "", "", errors.New("empty SOCKS5 domain")
		}
		raw := make([]byte, int(length[0]))
		if _, err := io.ReadFull(reader, raw); err != nil {
			return "", "", err
		}
		return strings.ToLower(string(raw)), "domain", nil
	default:
		return "", "", fmt.Errorf("unsupported SOCKS5 address type %d", atyp)
	}
}

func writeSOCKSReply(w io.Writer, code byte) error {
	// BND.ADDR/BND.PORT are reported as 0.0.0.0:0: the client only needs the
	// reply code, and a fabricated bound address would be a lie.
	_, err := w.Write([]byte{socksVersion, code, 0x00, atypIPv4, 0, 0, 0, 0, 0, 0})
	return err
}

// serveHTTPConnect accepts one HTTP CONNECT request and tunnels the stream.
func (p *proxy) serveHTTPConnect(client net.Conn) {
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(p.handshake))
	reader := bufio.NewReader(client)
	request, err := http.ReadRequest(reader)
	if err != nil {
		return
	}
	if request.Method != http.MethodConnect {
		_, _ = fmt.Fprint(client, "HTTP/1.1 405 Method Not Allowed\r\nContent-Length: 0\r\n\r\n")
		return
	}
	host, portText, err := net.SplitHostPort(request.Host)
	if err != nil {
		_, _ = fmt.Fprint(client, "HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\n\r\n")
		return
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		_, _ = fmt.Fprint(client, "HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\n\r\n")
		return
	}
	atyp := "domain"
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() != nil {
			atyp = "ipv4"
		} else {
			atyp = "ipv6"
		}
	}
	sequence := p.recorder.add(Connection{
		Protocol: "http_connect", ATYP: atyp, TargetHost: strings.ToLower(host), TargetPort: port,
		RawTarget: request.Host, NoAuth: request.Header.Get("Proxy-Authorization") == "",
	})
	upstream, address, dialErr := p.dial(host, port)
	p.recorder.finish(sequence, address, dialErr)
	if dialErr != nil {
		_, _ = fmt.Fprint(client, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}
	defer upstream.Close()
	if _, err := fmt.Fprint(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	_ = client.SetDeadline(time.Time{})
	// A CONNECT client may pipeline bytes behind the request head.
	if buffered := reader.Buffered(); buffered > 0 {
		head := make([]byte, buffered)
		if _, err := io.ReadFull(reader, head); err == nil {
			if _, err = upstream.Write(head); err != nil {
				return
			}
		}
	}
	splice(client, upstream)
}

// splice copies both directions and returns once either side is done, which is
// enough for a one-request test tunnel.
func splice(a, b net.Conn) {
	done := make(chan struct{}, 2)
	pipe := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if closer, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = closer.CloseWrite()
		}
		done <- struct{}{}
	}
	go pipe(a, b)
	go pipe(b, a)
	<-done
}

// endpoint is a listener the control surface can stop and restart in place.
// FR-25 items 2 and 3 need the SOCKS5 port to disappear and come back while the
// capture stays readable, so the listener lifecycle cannot be the process
// lifecycle.
type endpoint struct {
	mu       sync.Mutex
	address  string
	handle   func(net.Conn)
	listener net.Listener
	lastErr  string
}

func newEndpoint(address string, handle func(net.Conn)) *endpoint {
	return &endpoint{address: address, handle: handle}
}

// Start binds the address and serves in its own goroutine, so a caller (the
// control endpoint) gets the bind result immediately. A bind failure is recorded
// and returned so the lane can tell "down on purpose" from "cannot bind".
func (e *endpoint) Start() error {
	e.mu.Lock()
	if e.listener != nil {
		e.mu.Unlock()
		return nil
	}
	listener, err := net.Listen("tcp", e.address)
	if err != nil {
		e.lastErr = err.Error()
		e.mu.Unlock()
		return err
	}
	e.listener = listener
	e.lastErr = ""
	e.mu.Unlock()
	slog.Info("mock proxy listening", "address", e.address)
	go e.serve(listener)
	return nil
}

func (e *endpoint) serve(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			// A closed listener is a requested stop; anything else is a failure
			// that must not be swallowed.
			if !errors.Is(err, net.ErrClosed) {
				e.mu.Lock()
				e.lastErr = err.Error()
				e.listener = nil
				e.mu.Unlock()
				slog.Warn("mock proxy listener failed", "address", e.address, "error", err)
			}
			return
		}
		go e.handle(conn)
	}
}

// Stop closes the listener; in-flight tunnels keep running so a stop never
// masquerades as a mid-request failure.
func (e *endpoint) Stop() {
	e.mu.Lock()
	listener := e.listener
	e.listener = nil
	e.mu.Unlock()
	if listener != nil {
		_ = listener.Close()
		slog.Info("mock proxy listener stopped", "address", e.address)
	}
}

func (e *endpoint) Up() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.listener != nil
}

func (e *endpoint) Err() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastErr
}

// captureUsage records a usage batch and then forwards it verbatim to the real
// control plane. The forward is what keeps the ingest path in the loop while the
// emitted bytes stay observable; a forwarding failure answers the retryable 503
// the core expects, so its spool is retained exactly as with a real control plane.
func (p *proxy) captureUsage(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": "unreadable usage batch"})
		return
	}
	if !json.Valid(body) {
		writeJSON(w, 400, map[string]any{"error": "invalid usage batch"})
		return
	}
	headers := map[string]string{}
	for _, name := range []string{"Authorization", "Content-Type", "X-Token-Ne-Protocol-Version",
		"X-Token-Ne-Instance-Id", "X-Token-Ne-Request-Id", "Idempotency-Key"} {
		if value := r.Header.Get(name); value != "" {
			headers[strings.ToLower(name)] = value
		}
	}
	p.recorder.addBatch(headers, body)
	if p.relayTo == "" {
		writeJSON(w, 202, map[string]any{"accepted": 0, "duplicates": 0})
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		strings.TrimRight(p.relayTo, "/")+"/internal/v1/usage/batch", bytes.NewReader(body))
	if err != nil {
		writeJSON(w, 502, map[string]any{"error": "usage forward could not be built"})
		return
	}
	for name, values := range r.Header {
		req.Header[name] = values
	}
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		w.Header().Set("Retry-After", "1")
		writeJSON(w, 503, map[string]any{"error": "usage forward unavailable"})
		return
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(raw)
}

// relay forwards a request the mock does not own to the real control plane. It is
// deliberately verbatim: the lane must not change the bytes a client sends.
func (p *proxy) relay(w http.ResponseWriter, r *http.Request) {
	target := strings.TrimRight(p.relayTo, "/") + r.URL.RequestURI()
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": "unreadable relay body"})
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(body))
	if err != nil {
		writeJSON(w, 502, map[string]any{"error": "relay request could not be built"})
		return
	}
	for name, values := range r.Header {
		req.Header[name] = values
	}
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		writeJSON(w, 503, map[string]any{"error": "control plane relay unavailable"})
		return
	}
	defer response.Body.Close()
	for name, values := range response.Header {
		w.Header()[name] = values
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

// relayListener serves the control-plane surface. The core binds its usage
// endpoint from the snapshot the control plane publishes, so the published
// address has to be this listener for the batch to be observable.
func (p *proxy) relayListener(address string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/v1/usage/batch", p.captureUsage)
	mux.HandleFunc("/", p.relay)
	slog.Info("mock proxy relay listening", "address", address, "target", p.relayTo)
	return http.ListenAndServe(address, mux)
}

// control is the observation surface: captures, resets and listener control.
func (p *proxy) control(address string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /__proxy/connections", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"connections": p.recorder.snapshot()})
	})
	mux.HandleFunc("GET /__proxy/usage", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"batches": p.recorder.batchSnapshot()})
	})
	mux.HandleFunc("POST /__proxy/reset", func(w http.ResponseWriter, _ *http.Request) {
		p.recorder.reset()
		writeJSON(w, 200, map[string]any{"status": "ok"})
	})
	// POST /__proxy/listener {"endpoint":"socks5|http","state":"up|down"} gives
	// the lane an out-of-band way to make the SOCKS5 hop unreachable without
	// losing the capture or restarting the process.
	mux.HandleFunc("POST /__proxy/listener", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Endpoint string `json:"endpoint"`
			State    string `json:"state"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
			writeJSON(w, 400, map[string]any{"error": "invalid listener payload"})
			return
		}
		target := p.socks
		switch body.Endpoint {
		case "", "socks5":
		case "http":
			target = p.http
		default:
			writeJSON(w, 400, map[string]any{"error": "unknown endpoint"})
			return
		}
		if target == nil {
			writeJSON(w, 409, map[string]any{"error": "endpoint is not configured"})
			return
		}
		switch body.State {
		case "down":
			target.Stop()
			writeJSON(w, 200, map[string]any{"endpoint": body.Endpoint, "state": "down"})
		case "up":
			if err := target.Start(); err != nil {
				writeJSON(w, 503, map[string]any{"error": err.Error()})
				return
			}
			writeJSON(w, 200, map[string]any{"endpoint": body.Endpoint, "state": "up"})
		default:
			writeJSON(w, 400, map[string]any{"error": "state must be up or down"})
		}
	})
	mux.HandleFunc("GET /__proxy/listeners", func(w http.ResponseWriter, _ *http.Request) {
		out := map[string]any{}
		if p.socks != nil {
			out["socks5"] = map[string]any{"up": p.socks.Up(), "last_error": p.socks.Err()}
		}
		if p.http != nil {
			out["http_connect"] = map[string]any{"up": p.http.Up(), "last_error": p.http.Err()}
		}
		writeJSON(w, 200, out)
	})
	slog.Info("mock proxy control listening", "address", address)
	return http.ListenAndServe(address, mux)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func main() {
	resolver, err := parseResolver(os.Getenv("MOCK_PROXY_RESOLVE"))
	if err != nil {
		slog.Error("invalid resolver", "error", err)
		os.Exit(2)
	}
	handshake := 10 * time.Second
	if raw := strings.TrimSpace(os.Getenv("MOCK_PROXY_HANDSHAKE_TIMEOUT_SECONDS")); raw != "" {
		seconds, convErr := strconv.Atoi(raw)
		if convErr != nil || seconds < 0 {
			slog.Error("invalid MOCK_PROXY_HANDSHAKE_TIMEOUT_SECONDS", "value", raw)
			os.Exit(2)
		}
		handshake = time.Duration(seconds) * time.Second
	}
	p := &proxy{
		recorder:  &recorder{},
		resolver:  resolver,
		dialer:    &net.Dialer{Timeout: 10 * time.Second},
		handshake: handshake,
		relayTo:   strings.TrimSpace(os.Getenv("MOCK_PROXY_RELAY_TO")),
	}
	socksAddress := envOr("MOCK_PROXY_LISTEN", "127.0.0.1:1080")
	controlAddress := envOr("MOCK_PROXY_CONTROL_LISTEN", "127.0.0.1:8081")
	p.socks = newEndpoint(socksAddress, p.serveSOCKS5)
	// The HTTP CONNECT listener is opt-in so the docker-free lane binds exactly
	// one SOCKS5 port and nothing else.
	if httpAddress := strings.TrimSpace(os.Getenv("MOCK_PROXY_HTTP_LISTEN")); httpAddress != "" {
		p.http = newEndpoint(httpAddress, p.serveHTTPConnect)
	}
	errs := make(chan error, 3)
	// A stopped listener is a normal state, not a process exit: only a real bind
	// or serve error is reported, and the control surface keeps running so the
	// listener can be brought back.
	go func() {
		if err := p.socks.Start(); err != nil {
			errs <- err
		}
	}()
	if p.http != nil {
		go func() {
			if err := p.http.Start(); err != nil {
				errs <- err
			}
		}()
	}
	if relayAddress := strings.TrimSpace(os.Getenv("MOCK_PROXY_RELAY_LISTEN")); relayAddress != "" {
		go func() { errs <- p.relayListener(relayAddress) }()
	}
	go func() { errs <- p.control(controlAddress) }()
	if err := <-errs; err != nil {
		slog.Error("mock proxy stopped", "error", err)
		os.Exit(1)
	}
}
