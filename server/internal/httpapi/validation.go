package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/IvanLi-CN/token-ne/server/internal/store"
	"github.com/google/uuid"
)

type Detail struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

func detail(p, r string) []Detail { return []Detail{{p, r}} }

func normalizeUpstreamBaseURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	if path != "/v1" && !strings.HasSuffix(path, "/v1") {
		return raw
	}
	path = strings.TrimSuffix(path, "/v1")
	u.RawPath = path
	u.Path, err = url.PathUnescape(path)
	if err != nil {
		return raw
	}
	return u.String()
}

func validName(s string, max int) bool {
	return strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= max && !strings.ContainsFunc(s, unicode.IsControl)
}
func ValidateModel(m store.Model) []Detail {
	if !validName(m.ID, 256) {
		return detail("model_id", "must contain 1 to 256 characters")
	}
	if m.Mapping != nil && utf8.RuneCountInString(*m.Mapping) > 256 {
		return detail("default_upstream_model", "maximum length is 256")
	}
	if m.Shutdown != nil {
		if _, e := time.Parse(time.DateOnly, *m.Shutdown); e != nil {
			return detail("shutdown_date", "must be YYYY-MM-DD")
		}
	}
	for p, v := range map[string]*float64{"input_usd_per_mtok": m.Billing.Input, "cached_input_usd_per_mtok": m.Billing.Cached, "output_usd_per_mtok": m.Billing.Output} {
		if v != nil && (*v < 0 || *v >= 100000000 || math.IsNaN(*v) || math.IsInf(*v, 0)) {
			return detail("billing."+p, "must be a nonnegative numeric(14,6) value")
		}
	}
	return nil
}
func ValidateUpstream(u store.Upstream, allowPrivate bool) []Detail {
	if !validName(u.Name, 128) {
		return detail("name", "must contain 1 to 128 characters")
	}
	if u.Kind != "api_key" {
		return detail("kind", "must be api_key")
	}
	if u.AuthStyle != "bearer" && u.AuthStyle != "api_key" {
		return detail("auth_style", "must be bearer or api_key")
	}
	v, e := url.Parse(u.BaseURL)
	if e != nil || v.Hostname() == "" || v.User != nil || v.RawQuery != "" || v.Fragment != "" || (!allowPrivate && v.Scheme != "https") || (allowPrivate && v.Scheme != "http" && v.Scheme != "https") {
		return detail("base_url", "must be an HTTPS URL without credentials, query or fragment")
	}
	if !allowPrivate {
		if ip := net.ParseIP(v.Hostname()); ip != nil && !publicIP(ip) {
			return detail("base_url", "private addresses are not allowed")
		}
		if strings.EqualFold(v.Hostname(), "localhost") {
			return detail("base_url", "private addresses are not allowed")
		}
	}
	if u.Connect < 100 || u.Connect > 600000 {
		return detail("connect_timeout_ms", "must be between 100 and 600000")
	}
	if u.FirstByte < 100 || u.FirstByte > 3600000 {
		return detail("first_byte_timeout_ms", "must be between 100 and 3600000")
	}
	if u.Idle < 100 || u.Idle > 3600000 {
		return detail("idle_timeout_ms", "must be between 100 and 3600000")
	}
	seen := map[string]bool{}
	for i, c := range u.Models {
		p := "models[" + strconv.Itoa(i) + "]"
		if !validName(c.ModelID, 256) || seen[c.ModelID] {
			return detail(p+".model_id", "must be a unique model ID")
		}
		seen[c.ModelID] = true
		if c.Mapping != nil && utf8.RuneCountInString(*c.Mapping) > 256 {
			return detail(p+".upstream_model_name", "maximum length is 256")
		}
		if c.Priority < 0 || c.Priority > 100000 {
			return detail(p+".priority", "must be between 0 and 100000")
		}
		if c.Weight < 0 || c.Weight > 10000 {
			return detail(p+".weight", "must be between 0 and 10000")
		}
	}
	return nil
}
func ValidateKey(k store.Key) []Detail {
	if !validName(k.Name, 128) {
		return detail("name", "must contain 1 to 128 characters")
	}
	if k.MaxInflight < 1 || k.MaxInflight > 10000 {
		return detail("max_inflight", "must be between 1 and 10000")
	}
	seen := map[string]bool{}
	for i, m := range k.Allowed {
		if !validName(m, 256) || seen[m] {
			return detail("allowed_models["+strconv.Itoa(i)+"]", "must be a unique model ID or wildcard")
		}
		seen[m] = true
	}
	return nil
}
func EncodeCursor(c store.Cursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}
func DecodeCursor(s string) (*store.Cursor, error) {
	if s == "" {
		return nil, nil
	}
	if len(s) > 512 {
		return nil, errors.New("invalid cursor")
	}
	b, e := base64.RawURLEncoding.DecodeString(s)
	if e != nil {
		return nil, e
	}
	var c store.Cursor
	if e = json.Unmarshal(b, &c); e != nil {
		return nil, e
	}
	if _, e = uuid.Parse(c.ID); e != nil || c.Time.IsZero() {
		return nil, errors.New("invalid cursor")
	}
	return &c, nil
}
func pageLimit(s string) (int, error) {
	if s == "" {
		return 50, nil
	}
	n, e := strconv.Atoi(s)
	if e != nil || n < 1 || n > 200 {
		return 0, errors.New("limit must be between 1 and 200")
	}
	return n, nil
}
func publicIP(ip net.IP) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, s := range []string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32"} {
		_, n, _ := net.ParseCIDR(s)
		if n.Contains(ip) {
			return false
		}
	}
	return true
}
