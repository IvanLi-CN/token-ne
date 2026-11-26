package httpapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/IvanLi-CN/token-ne/server/internal/auth"
	"github.com/IvanLi-CN/token-ne/server/internal/store"
	"github.com/google/uuid"
)

// egressNetworkName is the Compose network a core instance must join before an
// egress profile may be enabled. The name is the compose `name:` of the egress
// network, not the service key.
const egressNetworkName = "token_ne_egress"

// egressCoreLabel selects the dynamic core containers; they are created by the
// control plane, not declared as a Compose service.
const egressCoreLabel = "com.token-ne.role=core"

// egressProbeTimeout bounds one profile test. /test must answer even when the
// third-party proxy is unreachable.
const egressProbeTimeout = 10 * time.Second

// egressProxyHostEnv is the host the API dials for a profile's SOCKS5 inbound.
// In the composed deployment the API reaches Xray over the egress network by
// service name; loopback would only work when both run in the same namespace.
const egressProxyHostEnv = "XRAY_EGRESS_HOST"

// egressProxyHostDefault is the Compose service name of the Xray singleton.
const egressProxyHostDefault = "xray"

// egressProbeTargetEnv overrides the controlled probe target. The default is a
// no-content endpoint: a successful probe proves the whole path without
// downloading a body.
const egressProbeTargetEnv = "EGRESS_PROBE_URL"

const egressDefaultProbeTarget = "https://www.gstatic.com/generate_204"

// EgressOptions carries the injectable edges of the egress proxy surface. The
// zero value is the production wiring; tests replace the network check and the
// probe transport so neither Docker nor Xray is needed.
type EgressOptions struct {
	// NetworkReady proves the core instances are attached to the egress
	// network. Nil uses the Docker Engine check.
	NetworkReady func(context.Context) error
	// ProbeURL is the controlled probe target. Empty uses EGRESS_PROBE_URL or
	// the built-in no-content target.
	ProbeURL string
	// Transport builds the round tripper the probe uses. Nil routes through the
	// profile's internal SOCKS5 inbound port on the Xray singleton.
	Transport func(store.EgressProxy) (http.RoundTripper, error)
}

type optionalObject struct {
	present bool
	raw     json.RawMessage
}

// UnmarshalJSON records presence so `null` can be told apart from an omitted
// field: credentials are keep/clear/replace, not a plain pointer.
func (o *optionalObject) UnmarshalJSON(b []byte) error {
	o.present = true
	o.raw = append(json.RawMessage(nil), b...)
	return nil
}

type egressInput struct {
	Name        string         `json:"name"`
	Protocol    string         `json:"protocol"`
	Server      string         `json:"server"`
	Port        int            `json:"port"`
	Transport   optionalObject `json:"transport"`
	TLS         optionalObject `json:"tls"`
	Credentials optionalObject `json:"credentials"`
	Enabled     bool           `json:"enabled"`
}

// egressTransport is the closed, non-secret transport option set. Network names
// the Xray transport; the remaining fields are the protocol-specific knobs the
// control plane accepts. Anything else is rejected, because silently ignoring a
// misspelled transport field is unsafe.
type egressTransport struct {
	Network     string            `json:"network,omitempty"`
	Host        string            `json:"host,omitempty"`
	Path        string            `json:"path,omitempty"`
	ServiceName string            `json:"service_name,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
}

// egressTLS is the closed, non-secret TLS option set. The field names are the
// ones the Xray renderer decodes (`server/internal/xray.TLSOption`), not a
// parallel spelling: the reconciler decodes with DisallowUnknownFields, so an
// option this layer accepted under a different name would make the profile
// permanently degraded.
type egressTLS struct {
	Mode        string   `json:"mode,omitempty"`
	ServerName  string   `json:"server_name,omitempty"`
	ALPN        []string `json:"alpn,omitempty"`
	Fingerprint string   `json:"fingerprint,omitempty"`
	PublicKey   string   `json:"public_key,omitempty"`
	ShortID     string   `json:"short_id,omitempty"`
	SpiderX     string   `json:"spider_x,omitempty"`
}

var (
	egressProtocols  = map[string]bool{"http": true, "socks5": true, "shadowsocks": true, "vmess": true, "vless": true, "trojan": true}
	egressTransports = map[string]bool{"tcp": true, "ws": true, "grpc": true, "httpupgrade": true}
	egressTLSModes   = map[string]bool{"none": true, "tls": true, "reality": true}
)

// optionKeys derives the closed field set from the struct tags, so the allowed
// set and the decoding target cannot drift apart.
func optionKeys(v any) map[string]bool {
	t := reflect.TypeOf(v)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	out := make(map[string]bool, t.NumField())
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			out[name] = true
		}
	}
	return out
}

// decodeOptions rejects every key outside the known set with an exact path,
// then decodes the value into the typed struct with unknown fields disallowed.
func decodeOptions(prefix string, raw json.RawMessage, dst any) []Detail {
	if len(raw) == 0 || string(raw) == "null" {
		return detail(prefix, "must be an object")
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil || keys == nil {
		return detail(prefix, "must be an object")
	}
	allowed := optionKeys(dst)
	details := []Detail{}
	for key := range keys {
		if !allowed[key] {
			details = append(details, Detail{Path: prefix + "." + key, Reason: "unknown option"})
		}
	}
	if len(details) > 0 {
		return details
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return detail(prefix+"."+typeErr.Field, "invalid option type")
		}
		return detail(prefix, "invalid option value")
	}
	return nil
}

// validateEgressInput enforces the frozen contract: closed protocol, transport
// and TLS option sets, no downgrade guessing, and no control characters in
// free-form values.
func validateEgressInput(in egressInput) (egressTransport, egressTLS, []Detail) {
	var transport egressTransport
	var tlsOptions egressTLS
	details := []Detail{}
	if !validName(in.Name, 128) {
		details = append(details, Detail{Path: "name", Reason: "must contain 1 to 128 characters without control characters"})
	}
	if !egressProtocols[in.Protocol] {
		details = append(details, Detail{Path: "protocol", Reason: "must be one of http, socks5, shadowsocks, vmess, vless, trojan"})
	}
	if trimmedEgressServer(in.Server) == "" {
		details = append(details, Detail{Path: "server", Reason: "must contain 1 to 255 characters without whitespace"})
	}
	if in.Port < 1 || in.Port > 65535 {
		details = append(details, Detail{Path: "port", Reason: "must be between 1 and 65535"})
	}
	if in.Transport.present {
		if d := decodeOptions("transport", in.Transport.raw, &transport); len(d) > 0 {
			details = append(details, d...)
		} else if transport.Network != "" && !egressTransports[transport.Network] {
			details = append(details, Detail{Path: "transport.network", Reason: "must be one of tcp, ws, grpc, httpupgrade"})
		}
	}
	if in.TLS.present {
		if d := decodeOptions("tls", in.TLS.raw, &tlsOptions); len(d) > 0 {
			details = append(details, d...)
		} else {
			switch {
			case tlsOptions.Mode != "" && !egressTLSModes[tlsOptions.Mode]:
				details = append(details, Detail{Path: "tls.mode", Reason: "must be one of none, tls, reality"})
			case tlsOptions.Mode == "reality":
				if tlsOptions.ServerName == "" {
					details = append(details, Detail{Path: "tls.server_name", Reason: "is required for reality"})
				}
				if tlsOptions.PublicKey == "" {
					details = append(details, Detail{Path: "tls.public_key", Reason: "is required for reality"})
				}
				if tlsOptions.ShortID == "" {
					details = append(details, Detail{Path: "tls.short_id", Reason: "is required for reality"})
				}
			}
		}
	}
	return transport, tlsOptions, details
}

// trimmedEgressServer rejects a server value that cannot be a host or address.
func trimmedEgressServer(server string) string {
	if utf8.RuneCountInString(server) > 255 || strings.ContainsFunc(server, unicode.IsControl) || strings.ContainsAny(server, " \t/?#@") {
		return ""
	}
	return server
}

// validateEgressCredentials accepts only a JSON object or null and bounds the
// plaintext so a profile cannot smuggle unrelated payloads into the database.
func validateEgressCredentials(raw json.RawMessage) []Detail {
	if string(raw) == "null" {
		return nil
	}
	if len(raw) > 16384 {
		return detail("credentials", "must not exceed 16384 bytes")
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil || keys == nil {
		return detail("credentials", "must be an object or null")
	}
	return nil
}

// egressView is the API projection: it never carries credentials or ciphertext.
type egressView struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Protocol       string          `json:"protocol"`
	Server         string          `json:"server"`
	Port           int             `json:"port"`
	Transport      json.RawMessage `json:"transport"`
	TLS            json.RawMessage `json:"tls"`
	Enabled        bool            `json:"enabled"`
	Effective      bool            `json:"effective_enabled"`
	RuntimeState   string          `json:"runtime_state"`
	SwapPhase      *string         `json:"swap_phase"`
	InboundPort    int             `json:"inbound_port"`
	LastError      *string         `json:"last_error"`
	HasCredentials bool            `json:"has_credentials"`
	Created        time.Time       `json:"created_at"`
	Updated        time.Time       `json:"updated_at"`
}

func egressProxyView(p store.EgressProxy) egressView {
	return egressView{ID: p.ID, Name: p.Name, Protocol: p.Protocol, Server: p.Server, Port: p.Port,
		Transport: objectOrEmpty(p.Transport), TLS: objectOrEmpty(p.TLS), Enabled: p.Enabled, Effective: p.Effective,
		RuntimeState: p.RuntimeState, SwapPhase: p.SwapPhase, InboundPort: p.InboundPort, LastError: p.LastError,
		HasCredentials: p.HasCredentials, Created: p.Created, Updated: p.Updated}
}

func objectOrEmpty(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage("{}")
	}
	return raw
}

func (a *API) egressProxies(w http.ResponseWriter, r *http.Request) {
	rows, e := a.repo.EgressProxies(r.Context())
	if e != nil {
		a.egressError(w, r, e)
		return
	}
	out := make([]egressView, len(rows))
	for i := range rows {
		out[i] = egressProxyView(rows[i])
	}
	writeJSON(w, 200, map[string]any{"data": out})
}

func (a *API) egressProxy(w http.ResponseWriter, r *http.Request) {
	if !validUUID(w, r, "proxy_id") {
		return
	}
	p, e := a.repo.EgressProxy(r.Context(), r.PathValue("proxy_id"))
	if e != nil {
		a.egressError(w, r, e)
		return
	}
	writeJSON(w, 200, egressProxyView(p))
}

func (a *API) saveEgressProxy(w http.ResponseWriter, r *http.Request) {
	create := r.Method == "POST"
	if !create && !validUUID(w, r, "proxy_id") {
		return
	}
	var in egressInput
	if !decode(w, r, &in, "name", "protocol", "server", "port", "enabled") {
		return
	}
	transport, tlsOptions, details := validateEgressInput(in)
	if in.Credentials.present {
		details = append(details, validateEgressCredentials(in.Credentials.raw)...)
	}
	if len(details) > 0 {
		fail(w, r, 422, "validation_failed", "invalid egress proxy", details)
		return
	}
	// Normalised objects are what gets stored: a stray key never persists.
	transportJSON, _ := json.Marshal(transport)
	tlsJSON, _ := json.Marshal(tlsOptions)
	p := store.EgressProxy{Name: in.Name, Protocol: in.Protocol, Server: trimmedEgressServer(in.Server), Port: in.Port,
		Transport: jsonObjectDefault(transportJSON, in.Transport.present), TLS: jsonObjectDefault(tlsJSON, in.TLS.present),
		Enabled: in.Enabled}
	action, status := "egress_proxy.update", 200
	previous := store.EgressProxy{}
	if create {
		p.ID = uuid.NewString()
		action, status = "egress_proxy.create", 201
	} else {
		id := r.PathValue("proxy_id")
		old, e := a.repo.EgressProxy(r.Context(), id)
		if e != nil {
			a.egressError(w, r, e)
			return
		}
		previous = old
		p.ID = id
		// Omitted transport/TLS keep the stored options; an explicit object
		// replaces them.
		if !in.Transport.present {
			p.Transport = objectOrEmpty(old.Transport)
		}
		if !in.TLS.present {
			p.TLS = objectOrEmpty(old.TLS)
		}
	}
	if !in.Credentials.present {
		p.Ciphertext, p.Nonce = previous.Ciphertext, previous.Nonce
	} else if string(in.Credentials.raw) != "null" {
		ciphertext, nonce, e := auth.Encrypt(a.cfg.MasterKey, string(in.Credentials.raw))
		if e != nil {
			fail(w, r, 500, "internal_error", "credential encryption failed", nil)
			return
		}
		p.Ciphertext, p.Nonce = ciphertext, nonce
	}
	// Enabling requires the core instances on the egress network. Re-validating
	// an already enabled profile would block edits during a Docker hiccup, so
	// only the disabled-to-enabled transition is gated.
	if p.Enabled && (create || !previous.Enabled) {
		if e := a.egressNetworkReady(r.Context()); e != nil {
			fail(w, r, 409, "conflict", "egress network is not available to core instances", detail("enabled", e.Error()))
			return
		}
	}
	audi := auditFor(r, action, "egress_proxy", p.ID)
	audi.Payload = egressAuditPayload(p)
	saved, e := a.repo.SaveEgressProxy(r.Context(), p, create, a.egressPorts(), audi)
	if e != nil {
		a.egressError(w, r, e)
		return
	}
	writeJSON(w, status, egressProxyView(saved))
}

func jsonObjectDefault(raw []byte, present bool) json.RawMessage {
	if !present {
		return json.RawMessage("{}")
	}
	return json.RawMessage(raw)
}

// egressAuditPayload records the profile shape without credentials: neither the
// plaintext nor the ciphertext may reach audit_events.
func egressAuditPayload(p store.EgressProxy) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"egress_proxy": egressView{ID: p.ID, Name: p.Name, Protocol: p.Protocol,
		Server: p.Server, Port: p.Port, Transport: p.Transport, TLS: p.TLS, Enabled: p.Enabled,
		HasCredentials: len(p.Ciphertext) > 0}})
	return raw
}

func (a *API) deleteEgressProxy(w http.ResponseWriter, r *http.Request) {
	if !validUUID(w, r, "proxy_id") {
		return
	}
	id := r.PathValue("proxy_id")
	if e := a.repo.DeleteEgressProxy(r.Context(), id, auditFor(r, "egress_proxy.delete", "egress_proxy", id)); e != nil {
		a.egressError(w, r, e)
		return
	}
	w.WriteHeader(204)
}

// testEgressProxy probes the controlled target through the profile's outbound.
// The answer always carries the bounded error class, never a credential, and
// never a raw transport error, which could quote proxy coordinates.
func (a *API) testEgressProxy(w http.ResponseWriter, r *http.Request) {
	if !validUUID(w, r, "proxy_id") {
		return
	}
	p, e := a.repo.EgressProxy(r.Context(), r.PathValue("proxy_id"))
	if e != nil {
		a.egressError(w, r, e)
		return
	}
	transport, e := a.egressTransport(p)
	if e != nil {
		fail(w, r, 500, "internal_error", "egress transport is unavailable", nil)
		return
	}
	if closer, ok := transport.(interface{ CloseIdleConnections() }); ok {
		defer closer.CloseIdleConnections()
	}
	ctx, cancel := context.WithTimeout(r.Context(), egressProbeTimeout)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, "GET", a.egressProbeURL(), nil)
	if e != nil {
		fail(w, r, 500, "internal_error", "probe target is invalid", nil)
		return
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	start := time.Now()
	res, e := client.Do(req)
	duration := time.Since(start).Milliseconds()
	var status *int
	var errorClass *string
	ok := false
	if e == nil {
		defer res.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
		status = &res.StatusCode
		ok = res.StatusCode >= 200 && res.StatusCode < 300
		if !ok {
			class := "http_status"
			errorClass = &class
		}
	} else {
		class := classifyEgressError(e)
		errorClass = &class
	}
	if e = a.repo.RecordAudit(r.Context(), auditFor(r, "egress_proxy.test", "egress_proxy", p.ID)); e != nil {
		a.egressError(w, r, e)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": ok, "status": status, "duration_ms": duration, "error_class": errorClass})
}

// classifyEgressError folds the transport failure into the bounded vocabulary
// the contract allows. Specific causes win over the generic connect class.
func classifyEgressError(err error) string {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "proxyconnect" {
		return "proxy_handshake"
	}
	var recordErr tls.RecordHeaderError
	var unknownAuthority x509.UnknownAuthorityError
	var hostnameErr x509.HostnameError
	var invalidCert x509.CertificateInvalidError
	if errors.As(err, &recordErr) || errors.As(err, &unknownAuthority) || errors.As(err, &hostnameErr) || errors.As(err, &invalidCert) {
		return "tls"
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "socks") || strings.Contains(message, "proxy handshake") {
		return "proxy_handshake"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	return "connect"
}

// egressPorts is the configured inbound port pool.
func (a *API) egressPorts() store.PortRange {
	return store.PortRange{Min: a.cfg.XrayProxyPortMin, Max: a.cfg.XrayProxyPortMax}
}

func (a *API) egressProbeURL() string {
	if a.egress.ProbeURL != "" {
		return a.egress.ProbeURL
	}
	if v := strings.TrimSpace(os.Getenv(egressProbeTargetEnv)); v != "" {
		return v
	}
	return egressDefaultProbeTarget
}

// egressProxyHost is where the API dials a profile's SOCKS5 inbound. Inside the
// Compose project that is the Xray service on the egress network; loopback would
// only work if the API and Xray shared a network namespace, which they do not.
func (a *API) egressProxyHost() string {
	if v := strings.TrimSpace(os.Getenv(egressProxyHostEnv)); v != "" {
		return v
	}
	return egressProxyHostDefault
}

// upstreamTransport builds the HTTP transport for one upstream. A bound
// upstream goes through its profile's internal SOCKS5 inbound with socks5h
// semantics; an unbound one keeps the pinned-address direct dial. There is no
// third branch: when the profile is unavailable this returns an error instead
// of silently falling back to direct egress, which is what the contract
// forbids. Both the catalog sync and the connection test use this, so neither
// can bypass the binding.
func (a *API) upstreamTransport(ctx context.Context, u store.Upstream) (*http.Transport, error) {
	direct := func() *http.Transport {
		dial := safeDial(a.cfg.AllowPrivateUpstreams)
		return &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			ctx, cancel := context.WithTimeout(ctx, time.Duration(u.Connect)*time.Millisecond)
			defer cancel()
			return dial(ctx, network, address)
		}, TLSHandshakeTimeout: time.Duration(u.Connect) * time.Millisecond,
			ResponseHeaderTimeout: time.Duration(u.FirstByte) * time.Millisecond}
	}
	if u.EgressProxyID == nil {
		return direct(), nil
	}
	p, e := a.repo.EgressProxy(ctx, *u.EgressProxyID)
	if e != nil {
		return nil, errEgressUnavailable
	}
	// Effective enablement is the routing qualification: an admin-enabled
	// profile whose runtime still has a live outbound. Anything else means the
	// bound upstream has no candidate, so the probe fails rather than going
	// direct.
	if !p.Effective || p.InboundPort < 1 {
		return nil, errEgressUnavailable
	}
	proxyURL := &url.URL{Scheme: "socks5h", Host: net.JoinHostPort(a.egressProxyHost(), fmt.Sprint(p.InboundPort))}
	// The proxy host is our own deployment endpoint, so it dials directly while
	// the upstream hostname travels inside the SOCKS5 tunnel and is resolved by
	// the third-party proxy, never here.
	return &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSHandshakeTimeout: time.Duration(u.Connect) * time.Millisecond,
		ResponseHeaderTimeout: time.Duration(u.FirstByte) * time.Millisecond}, nil
}

// errEgressUnavailable reports that a bound upstream cannot reach its profile.
// Distinct from a transport error so callers can say "no candidate" rather
// than pretending the third-party proxy timed out.
var errEgressUnavailable = errors.New("bound egress proxy is unavailable")

// egressTransport routes the probe through the profile's internal SOCKS5
// inbound port: Xray resolves the target hostname, not the control plane.
func (a *API) egressTransport(p store.EgressProxy) (http.RoundTripper, error) {
	if a.egress.Transport != nil {
		return a.egress.Transport(p)
	}
	if p.InboundPort < 1 {
		return nil, errors.New("profile has no inbound port")
	}
	proxyURL := &url.URL{Scheme: "socks5h", Host: net.JoinHostPort(a.egressProxyHost(), fmt.Sprint(p.InboundPort))}
	return &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSHandshakeTimeout: egressProbeTimeout,
		ResponseHeaderTimeout: egressProbeTimeout, DisableKeepAlives: true}, nil
}

func (a *API) egressNetworkReady(ctx context.Context) error {
	if a.egress.NetworkReady != nil {
		return a.egress.NetworkReady(ctx)
	}
	return checkCoreEgressNetwork(ctx, os.Getenv("CORE_NETWORKS"), os.Getenv("DOCKER_HOST"))
}

// checkCoreEgressNetwork proves the instances can reach the egress network. The
// Compose project must list it in CORE_NETWORKS — that is what new containers
// join — and every running core container must already carry it. A network
// object that merely exists does not pass.
func checkCoreEgressNetwork(ctx context.Context, coreNetworks, dockerHost string) error {
	if !containsNetwork(coreNetworks, egressNetworkName) {
		return errors.New("CORE_NETWORKS does not include " + egressNetworkName)
	}
	running, err := runningCoreNetworks(ctx, dockerHost)
	if err != nil {
		return errors.New("cannot verify the core container networks")
	}
	if len(running) == 0 {
		// No instance exists yet; the next one is created from CORE_NETWORKS,
		// which already contains the egress network.
		return nil
	}
	attached := 0
	for _, networks := range running {
		if networks[egressNetworkName] {
			attached++
		}
	}
	if attached == 0 {
		return errors.New("no running core container is attached to " + egressNetworkName)
	}
	if attached != len(running) {
		return errors.New("a running core container is not attached to " + egressNetworkName)
	}
	return nil
}

func containsNetwork(value, name string) bool {
	for _, item := range strings.Split(value, ",") {
		if strings.TrimSpace(item) == name {
			return true
		}
	}
	return false
}

// runningCoreNetworks lists the network names of each running core container
// through the Docker Engine API, the same socket the rest of the control plane
// uses.
func runningCoreNetworks(ctx context.Context, dockerHost string) ([]map[string]bool, error) {
	if dockerHost == "" {
		dockerHost = "unix:///var/run/docker.sock"
	}
	u, err := url.Parse(dockerHost)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	var dial func(context.Context, string, string) (net.Conn, error)
	switch u.Scheme {
	case "unix":
		dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", u.Path)
		}
	case "tcp":
		dial = dialer.DialContext
	default:
		return nil, errors.New("unsupported Docker host")
	}
	client := &http.Client{Transport: &http.Transport{DialContext: dial}, Timeout: 3 * time.Second}
	filter, _ := json.Marshal(map[string][]string{"label": {egressCoreLabel}})
	req, err := http.NewRequestWithContext(ctx, "GET", "http://docker/containers/json?all=true&filters="+url.QueryEscape(string(filter)), nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, errors.New("Docker API rejected the container list")
	}
	var containers []struct {
		State           string                                        `json:"State"`
		NetworkSettings struct{ Networks map[string]json.RawMessage } `json:"NetworkSettings"`
	}
	if err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&containers); err != nil {
		return nil, err
	}
	out := []map[string]bool{}
	for _, c := range containers {
		if c.State != "running" {
			continue
		}
		names := make(map[string]bool, len(c.NetworkSettings.Networks))
		for name := range c.NetworkSettings.Networks {
			names[name] = true
		}
		out = append(out, names)
	}
	return out, nil
}

// egressError keeps the package status vocabulary: 404 unknown, 409 conflict
// (duplicate name, live binding, exhausted port range), 422 invalid reference,
// 503 storage unavailable.
func (a *API) egressError(w http.ResponseWriter, r *http.Request, e error) {
	var field *store.FieldError
	switch {
	case errors.As(e, &field):
		if errors.Is(field, store.ErrConflict) {
			fail(w, r, 409, "conflict", "egress proxy conflicts with existing state", detail(field.Path, field.Reason))
			return
		}
		fail(w, r, 422, "validation_failed", "invalid egress proxy field", detail(field.Path, field.Reason))
	case errors.Is(e, store.ErrEgressPortExhausted):
		fail(w, r, 409, "conflict", "egress proxy port range is exhausted", detail("inbound_port", "no free port in the configured range"))
	case errors.Is(e, store.ErrNotFound):
		fail(w, r, 404, "not_found", "resource not found", nil)
	case errors.Is(e, store.ErrConflict):
		fail(w, r, 409, "conflict", "egress proxy conflicts with existing state", nil)
	case errors.Is(e, store.ErrReference):
		fail(w, r, 422, "validation_failed", "invalid egress proxy reference", detail("egress_proxy_id", "referenced egress proxy does not exist"))
	default:
		fail(w, r, 503, "storage_unavailable", "storage operation unavailable", nil)
	}
}
