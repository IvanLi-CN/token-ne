package xray

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	xnet "github.com/xtls/xray-core/common/net"
	core "github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf"

	"github.com/xtls/xray-core/app/router"
)

// Protocol is one of the upstream proxy protocols the control plane supports.
type Protocol string

const (
	ProtocolHTTP        Protocol = "http"
	ProtocolSOCKS5      Protocol = "socks5"
	ProtocolShadowsocks Protocol = "shadowsocks"
	ProtocolVMess       Protocol = "vmess"
	ProtocolVLESS       Protocol = "vless"
	ProtocolTrojan      Protocol = "trojan"
)

// ListenAddress is where each per-profile SOCKS5 inbound listens inside the
// Xray container. Core reaches it over the egress network, so it must not be
// loopback.
const ListenAddress = "0.0.0.0"

// TransportOption is the closed transport option set. The JSON keys are the
// control-plane spelling stored in egress_proxies.transport; any other key is a
// caller error. Network is tcp|ws|grpc|httpupgrade.
type TransportOption struct {
	// Network is tcp, ws, grpc or httpupgrade. Empty means tcp.
	Network string `json:"network"`
	// Host is the WebSocket/HTTPUpgrade Host header, or the gRPC authority.
	Host string `json:"host"`
	// Path is the WebSocket/HTTPUpgrade request path.
	Path string `json:"path"`
	// ServiceName is the gRPC service name.
	ServiceName string `json:"service_name"`
	// Headers are extra WebSocket handshake headers.
	Headers map[string]string `json:"headers"`
}

// TLSOption is the closed TLS option set. Mode is none|tls|reality.
type TLSOption struct {
	// Mode is none, tls or reality. Empty means none.
	Mode string `json:"mode"`
	// ServerName is the SNI sent to the proxy server, and the REALITY server
	// name.
	ServerName string `json:"server_name"`
	// ALPN lists the ALPN protocols. "tls" only.
	ALPN []string `json:"alpn"`
	// Fingerprint is the uTLS client hello fingerprint (tls and reality).
	Fingerprint string `json:"fingerprint"`
	// PublicKey is the REALITY server public key (base64url, 32 bytes).
	PublicKey string `json:"public_key"`
	// ShortID is the REALITY short id (hex, at most 16 characters).
	ShortID string `json:"short_id"`
	// SpiderX is the REALITY spider path. Empty means "/".
	SpiderX string `json:"spider_x"`
}

// Profile is the non-secret half of one egress_proxies row. RuntimeState and
// SwapPhase carry the currently persisted values so the reconciler knows
// whether a swap is already in flight and how far it got.
type Profile struct {
	ID           string
	Protocol     Protocol
	Server       string
	Port         int
	Transport    json.RawMessage
	TLS          json.RawMessage
	InboundPort  int
	Enabled      bool
	RuntimeState string
	SwapPhase    string
}

// Credentials is one profile's decrypted proxy credentials. Its String and
// GoString methods redact every field, so printing a Credentials, Desired or
// Profile value cannot leak them; the package never puts a credential in an
// error or a log line either.
type Credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
	UUID     string `json:"uuid"`
	Cipher   string `json:"cipher"`
	Flow     string `json:"flow"`
}

// String redacts every credential field.
func (Credentials) String() string { return "xray.Credentials{redacted}" }

// GoString redacts every credential field for %#v.
func (Credentials) GoString() string { return "xray.Credentials{redacted}" }

// Desired is one profile plus its decrypted credentials: the complete desired
// state of one Xray inbound/outbound/route.
type Desired struct {
	Profile     Profile
	Credentials Credentials
}

// String keeps credentials out of logs even when the whole desired state is
// printed.
func (d Desired) String() string {
	return "xray.Desired{profile=" + d.Profile.ID + ", protocol=" + string(d.Profile.Protocol) + ", credentials=redacted}"
}

// GoString keeps credentials out of %#v renderings.
func (d Desired) GoString() string { return d.String() }

// Objects is the rendered Xray configuration for one profile under its stable
// tags. Render has already validated the generated protobufs by calling Build.
type Objects struct {
	Inbound  *core.InboundHandlerConfig
	Outbound *core.OutboundHandlerConfig
	Rule     *router.RoutingRule
}

// Render turns one desired profile into the Xray objects for its stable tags.
// Anything outside the enumerated option set returns a *ValidationError
// carrying a request field path, so the API layer can answer 422 without
// guessing a downgrade.
func Render(d Desired) (Objects, error) {
	p := d.Profile
	if p.ID == "" {
		return Objects{}, invalid("id", "is required")
	}
	if p.InboundPort < 1024 || p.InboundPort > 65535 {
		return Objects{}, invalid("inbound_port", "must be between 1024 and 65535")
	}
	if p.Server == "" {
		return Objects{}, invalid("server", "is required")
	}
	if p.Port < 1 || p.Port > 65535 {
		return Objects{}, invalid("port", "must be between 1 and 65535")
	}
	_, tls, err := decodeOptions(p)
	if err != nil {
		return Objects{}, err
	}
	if err := validateCredentials(p.Protocol, tls, d.Credentials); err != nil {
		return Objects{}, err
	}
	in, err := buildInbound(p)
	if err != nil {
		return Objects{}, reject(p.ID, "render_inbound", err)
	}
	out, err := buildOutbound(d, OutboundTag(p.ID))
	if err != nil {
		return Objects{}, reject(p.ID, "render_outbound", err)
	}
	return Objects{Inbound: in, Outbound: out, Rule: NewRule(p.ID, OutboundTag(p.ID))}, nil
}

// RenderStagedOutbound renders the outbound under the :staged tag used by the
// swap sequence.
func RenderStagedOutbound(d Desired) (*core.OutboundHandlerConfig, error) {
	if _, err := Render(d); err != nil {
		return nil, err
	}
	out, err := buildOutbound(d, StagedOutboundTag(d.Profile.ID))
	if err != nil {
		return nil, reject(d.Profile.ID, "render_staged", err)
	}
	return out, nil
}

// NewRule is the routing rule that sends the profile's inbound to one outbound.
func NewRule(profileID, outboundTag string) *router.RoutingRule {
	return &router.RoutingRule{
		TargetTag:  &router.RoutingRule_Tag{Tag: outboundTag},
		RuleTag:    RuleTag(profileID),
		InboundTag: []string{InboundTag(profileID)},
	}
}

func reject(profileID, step string, err error) error {
	return &RejectedError{ProfileID: profileID, Step: step, Reason: err.Error()}
}

// DecodeOptions validates the closed transport/TLS option set and returns it in
// normalised form. Middleware and handlers use it to reject a profile without
// rendering it.
func DecodeOptions(p Profile) (TransportOption, TLSOption, error) { return decodeOptions(p) }

func decodeOptions(p Profile) (TransportOption, TLSOption, error) {
	var tr TransportOption
	if err := decodeStrict(p.Transport, "transport", &tr); err != nil {
		return tr, TLSOption{}, err
	}
	var tls TLSOption
	if err := decodeStrict(p.TLS, "tls", &tls); err != nil {
		return tr, tls, err
	}
	tr.Network = strings.ToLower(tr.Network)
	if tr.Network == "" {
		tr.Network = "tcp"
	}
	tls.Mode = strings.ToLower(tls.Mode)
	if tls.Mode == "" {
		tls.Mode = "none"
	}
	switch p.Protocol {
	case ProtocolHTTP, ProtocolSOCKS5, ProtocolShadowsocks, ProtocolVMess, ProtocolVLESS, ProtocolTrojan:
	default:
		return tr, tls, invalid("protocol", `must be one of "http", "socks5", "shadowsocks", "vmess", "vless", "trojan"`)
	}
	switch tr.Network {
	case "tcp", "ws", "grpc", "httpupgrade":
	default:
		return tr, tls, invalid("transport.network", `must be one of "tcp", "ws", "grpc", "httpupgrade"`)
	}
	switch tls.Mode {
	case "none", "tls", "reality":
	default:
		return tr, tls, invalid("tls.mode", `must be one of "none", "tls", "reality"`)
	}
	if tr.Network != "ws" && tr.Network != "httpupgrade" {
		if tr.Path != "" {
			return tr, tls, invalid("transport.path", `is only supported for "ws" and "httpupgrade"`)
		}
		if len(tr.Headers) > 0 {
			return tr, tls, invalid("transport.headers", `is only supported for "ws"`)
		}
	}
	if tr.Network == "tcp" && tr.Host != "" {
		return tr, tls, invalid("transport.host", `is only supported for "ws", "httpupgrade" and "grpc"`)
	}
	if tr.Network != "grpc" && tr.ServiceName != "" {
		return tr, tls, invalid("transport.service_name", `is only supported for "grpc"`)
	}
	switch tls.Mode {
	case "none":
		if tls.ServerName != "" {
			return tr, tls, invalid("tls.server_name", `requires "tls" or "reality"`)
		}
		if len(tls.ALPN) > 0 {
			return tr, tls, invalid("tls.alpn", `requires "tls"`)
		}
		if tls.Fingerprint != "" {
			return tr, tls, invalid("tls.fingerprint", `requires "tls" or "reality"`)
		}
		if tls.PublicKey != "" {
			return tr, tls, invalid("tls.public_key", `requires "reality"`)
		}
		if tls.ShortID != "" {
			return tr, tls, invalid("tls.short_id", `requires "reality"`)
		}
		if tls.SpiderX != "" {
			return tr, tls, invalid("tls.spider_x", `requires "reality"`)
		}
	case "tls":
		if tls.PublicKey != "" {
			return tr, tls, invalid("tls.public_key", `requires "reality"`)
		}
		if tls.ShortID != "" {
			return tr, tls, invalid("tls.short_id", `requires "reality"`)
		}
		if tls.SpiderX != "" {
			return tr, tls, invalid("tls.spider_x", `requires "reality"`)
		}
	case "reality":
		// Xray only supports REALITY over RAW/TCP and gRPC.
		if tr.Network != "tcp" && tr.Network != "grpc" {
			return tr, tls, invalid("transport.network", `"reality" requires network "tcp" or "grpc"`)
		}
		if len(tls.ALPN) > 0 {
			return tr, tls, invalid("tls.alpn", `is not supported by "reality"`)
		}
		if tls.ServerName == "" {
			return tr, tls, invalid("tls.server_name", `is required by "reality"`)
		}
		if tls.PublicKey == "" {
			return tr, tls, invalid("tls.public_key", `is required by "reality"`)
		}
		if key, err := base64.RawURLEncoding.DecodeString(tls.PublicKey); err != nil || len(key) != 32 {
			return tr, tls, invalid("tls.public_key", "must be 32 bytes encoded as base64url without padding")
		}
		if len(tls.ShortID) > 16 {
			return tr, tls, invalid("tls.short_id", "must be at most 16 hex characters")
		}
		if _, err := hex.DecodeString(tls.ShortID); err != nil {
			return tr, tls, invalid("tls.short_id", "must be hexadecimal")
		}
		if tls.SpiderX != "" && !strings.HasPrefix(tls.SpiderX, "/") {
			return tr, tls, invalid("tls.spider_x", `must start with "/"`)
		}
	}
	return tr, tls, nil
}

var shadowCiphers = map[string]bool{
	"aes-128-gcm":             true,
	"aes-256-gcm":             true,
	"chacha20-ietf-poly1305":  true,
	"chacha20-poly1305":       true,
	"xchacha20-ietf-poly1305": true,
}

func validateCredentials(p Protocol, tls TLSOption, c Credentials) error {
	if c.Flow != "" {
		if p != ProtocolVLESS {
			return invalid("credentials.flow", `is only supported for "vless"`)
		}
		switch c.Flow {
		case "xtls-rprx-vision", "xtls-rprx-vision-udp443":
		default:
			return invalid("credentials.flow", `must be "xtls-rprx-vision" or "xtls-rprx-vision-udp443"`)
		}
		if tls.Mode == "none" {
			return invalid("credentials.flow", `"xtls-rprx-vision" requires "tls" or "reality"`)
		}
	}
	switch p {
	case ProtocolHTTP, ProtocolSOCKS5:
		if c.Username == "" && c.Password == "" {
			return nil
		}
		if c.UUID != "" || c.Cipher != "" {
			return invalid("credentials", `"uuid" and "cipher" do not apply to "http" and "socks5"`)
		}
		if (c.Username == "") != (c.Password == "") {
			return invalid("credentials", `"username" and "password" must be set together`)
		}
	case ProtocolShadowsocks:
		if c.Cipher == "" {
			return invalid("credentials.cipher", "is required for shadowsocks")
		}
		if !shadowCiphers[strings.ToLower(c.Cipher)] {
			return invalid("credentials.cipher", "must be a supported AEAD method (aes-128-gcm, aes-256-gcm, chacha20-poly1305, xchacha20-poly1305)")
		}
		if c.Password == "" {
			return invalid("credentials.password", "is required for shadowsocks")
		}
		if c.UUID != "" {
			return invalid("credentials.uuid", "does not apply to shadowsocks")
		}
	case ProtocolVMess, ProtocolVLESS:
		if c.UUID == "" {
			return invalid("credentials.uuid", "is required for "+string(p))
		}
		if _, err := uuid.Parse(c.UUID); err != nil {
			return invalid("credentials.uuid", "must be a UUID")
		}
		if c.Password != "" || c.Cipher != "" {
			return invalid("credentials", `"password" and "cipher" do not apply to "vmess" and "vless"`)
		}
	case ProtocolTrojan:
		if c.Password == "" {
			return invalid("credentials.password", "is required for trojan")
		}
		if c.UUID != "" || c.Cipher != "" {
			return invalid("credentials", `"uuid" and "cipher" do not apply to "trojan"`)
		}
	}
	return nil
}

func buildInbound(p Profile) (*core.InboundHandlerConfig, error) {
	settings := json.RawMessage(`{"auth":"noauth","udp":false}`)
	network := conf.TransportProtocol("tcp")
	cfg := &conf.InboundDetourConfig{
		Protocol:      "socks",
		PortList:      &conf.PortList{Range: []conf.PortRange{{From: uint32(p.InboundPort), To: uint32(p.InboundPort)}}},
		ListenOn:      &conf.Address{Address: xnet.ParseAddress(ListenAddress)},
		Settings:      &settings,
		Tag:           InboundTag(p.ID),
		StreamSetting: &conf.StreamConfig{Network: &network},
	}
	return cfg.Build()
}

func buildOutbound(d Desired, tag string) (*core.OutboundHandlerConfig, error) {
	p := d.Profile
	tr, tls, err := decodeOptions(p)
	if err != nil {
		return nil, err
	}
	settings, protocol, err := outboundSettings(p, d.Credentials)
	if err != nil {
		return nil, err
	}
	stream, err := streamSettings(tr, tls)
	if err != nil {
		return nil, err
	}
	cfg := &conf.OutboundDetourConfig{
		Protocol:      protocol,
		Tag:           tag,
		Settings:      &settings,
		StreamSetting: stream,
	}
	return cfg.Build()
}

func outboundSettings(p Profile, c Credentials) (json.RawMessage, string, error) {
	base := map[string]any{"address": p.Server, "port": p.Port}
	var protocol string
	switch p.Protocol {
	case ProtocolHTTP:
		base["user"], base["pass"] = c.Username, c.Password
		protocol = "http"
	case ProtocolSOCKS5:
		base["user"], base["pass"] = c.Username, c.Password
		protocol = "socks"
	case ProtocolShadowsocks:
		base["method"], base["password"] = strings.ToLower(c.Cipher), c.Password
		protocol = "shadowsocks"
	case ProtocolVMess:
		base["id"], base["security"] = c.UUID, "auto"
		protocol = "vmess"
	case ProtocolVLESS:
		base["id"], base["encryption"] = c.UUID, "none"
		if c.Flow != "" {
			base["flow"] = c.Flow
		}
		protocol = "vless"
	case ProtocolTrojan:
		base["password"] = c.Password
		protocol = "trojan"
	default:
		return nil, "", invalid("protocol", "is not supported")
	}
	raw, err := json.Marshal(base)
	if err != nil {
		return nil, "", err
	}
	return raw, protocol, nil
}

func streamSettings(tr TransportOption, tls TLSOption) (*conf.StreamConfig, error) {
	network := conf.TransportProtocol(tr.Network)
	stream := &conf.StreamConfig{Network: &network, Security: tls.Mode}
	switch tr.Network {
	case "ws":
		stream.WSSettings = &conf.WebSocketConfig{Host: tr.Host, Path: tr.Path, Headers: tr.Headers}
	case "httpupgrade":
		stream.HTTPUPGRADESettings = &conf.HttpUpgradeConfig{Host: tr.Host, Path: tr.Path, Headers: tr.Headers}
	case "grpc":
		stream.GRPCSettings = &conf.GRPCConfig{Authority: tr.Host, ServiceName: tr.ServiceName}
	}
	switch tls.Mode {
	case "tls":
		cfg := &conf.TLSConfig{ServerName: tls.ServerName, Fingerprint: tls.Fingerprint}
		if len(tls.ALPN) > 0 {
			cfg.ALPN = conf.NewStringList(tls.ALPN)
		}
		stream.TLSSettings = cfg
	case "reality":
		fingerprint := tls.Fingerprint
		if fingerprint == "" {
			fingerprint = "chrome"
		}
		stream.REALITYSettings = &conf.REALITYConfig{
			PublicKey:   tls.PublicKey,
			ShortId:     tls.ShortID,
			ServerName:  tls.ServerName,
			Fingerprint: fingerprint,
			SpiderX:     tls.SpiderX,
		}
	}
	return stream, nil
}

// decodeStrict decodes one JSON option object, rejecting unknown keys and
// non-object values with the offending field path under prefix.
func decodeStrict(raw json.RawMessage, prefix string, out any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if trimmed[0] != '{' {
		return invalid(prefix, "must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			path := prefix
			if typeErr.Field != "" {
				path += "." + typeErr.Field
			}
			return invalid(path, "has the wrong type")
		}
		const unknown = `json: unknown field "`
		if msg := err.Error(); strings.HasPrefix(msg, unknown) {
			name := strings.TrimSuffix(strings.TrimPrefix(msg, unknown), `"`)
			return invalid(prefix+"."+name, "is not a supported option")
		}
		return invalid(prefix, "is not valid JSON")
	}
	return nil
}
