package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/IvanLi-CN/token-ne/server/internal/auth"
	"github.com/IvanLi-CN/token-ne/server/internal/store"
	"github.com/google/uuid"
)

func (a *API) listUpstreams(w http.ResponseWriter, r *http.Request) {
	v, e := a.repo.Upstreams(r.Context())
	if e != nil {
		storageError(w, r, e, "")
		return
	}
	writeJSON(w, 200, map[string]any{"data": v})
}
func (a *API) getUpstream(w http.ResponseWriter, r *http.Request) {
	if !validUUID(w, r, "upstream_id") {
		return
	}
	v, e := a.repo.Upstream(r.Context(), r.PathValue("upstream_id"))
	if e != nil {
		storageError(w, r, e, "")
		return
	}
	writeJSON(w, 200, v)
}
func (a *API) saveUpstream(w http.ResponseWriter, r *http.Request) {
	create := r.Method == "POST"
	var body struct {
		store.Upstream
		APIKey *string `json:"api_key"`
	}
	body.Connect = 10000
	body.FirstByte = 90000
	body.Idle = 300000
	required := []string{"name", "kind", "base_url", "auth_style", "enabled", "models"}
	if create {
		required = append(required, "api_key")
	} else if !validUUID(w, r, "upstream_id") {
		return
	}
	if !decode(w, r, &body, required...) {
		return
	}
	u := body.Upstream
	if u.Models == nil {
		fail(w, r, 422, "validation_failed", "models must be an array", detail("models", "must be an array"))
		return
	}
	if d := ValidateUpstream(u, a.cfg.AllowPrivateUpstreams); len(d) > 0 {
		fail(w, r, 422, "validation_failed", "invalid upstream", d)
		return
	}
	if !a.cfg.AllowPrivateUpstreams {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		err := validatePublicHost(ctx, u.BaseURL, net.DefaultResolver.LookupIPAddr)
		cancel()
		if err != nil {
			fail(w, r, 422, "validation_failed", "invalid upstream address", detail("base_url", err.Error()))
			return
		}
	}
	if create {
		u.ID = uuid.NewString()
		if body.APIKey == nil || *body.APIKey == "" {
			fail(w, r, 422, "validation_failed", "API key required", detail("api_key", "is required"))
			return
		}
	} else {
		u.ID = r.PathValue("upstream_id")
		old, e := a.repo.Upstream(r.Context(), u.ID)
		if e != nil {
			storageError(w, r, e, "")
			return
		}
		u.Ciphertext = old.Ciphertext
		u.Nonce = old.Nonce
		u.Hint = old.Hint
	}
	if body.APIKey != nil && *body.APIKey != "" {
		if len(*body.APIKey) > 16384 || strings.ContainsFunc(*body.APIKey, unicode.IsControl) {
			fail(w, r, 422, "validation_failed", "invalid API key", detail("api_key", "invalid length or control characters"))
			return
		}
		var e error
		u.Ciphertext, u.Nonce, e = auth.Encrypt(a.cfg.MasterKey, *body.APIKey)
		if e != nil {
			fail(w, r, 500, "internal_error", "key encryption failed", nil)
			return
		}
		u.Hint = auth.Hint(*body.APIKey)
	}
	u.Masked = auth.Mask(u.Hint)
	u.LastFour = u.Hint
	action := "upstream.update"
	status := 200
	if create {
		action = "upstream.create"
		status = 201
	}
	audi := auditFor(r, action, "upstream", u.ID)
	audi.Payload, _ = json.Marshal(u)
	v, e := a.repo.SaveUpstream(r.Context(), u, create, audi)
	if e != nil {
		if errors.Is(e, store.ErrConflict) {
			fail(w, r, 422, "validation_failed", "upstream name is already in use", detail("name", "must be unique"))
			return
		}
		storageError(w, r, e, "models")
		return
	}
	writeJSON(w, status, v)
}
func (a *API) deleteUpstream(w http.ResponseWriter, r *http.Request) {
	if !validUUID(w, r, "upstream_id") {
		return
	}
	id := r.PathValue("upstream_id")
	if e := a.repo.Delete(r.Context(), "upstream", id, auditFor(r, "upstream.delete", "upstream", id)); e != nil {
		storageError(w, r, e, "")
		return
	}
	w.WriteHeader(204)
}

// Resolve and verify at dial time, pinning the dial to the checked IP to prevent DNS rebinding.
func safeDial(allow bool) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		ips, e := net.DefaultResolver.LookupIPAddr(ctx, host)
		if e != nil {
			return nil, errors.New("upstream DNS resolution failed")
		}
		for _, ip := range ips {
			if !allow && !publicIP(ip.IP) {
				return nil, errors.New("upstream address is blocked")
			}
		}
		for _, ip := range ips {
			conn, e := (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if e == nil {
				return conn, nil
			}
		}
		return nil, errors.New("upstream connection failed")
	}
}
func (a *API) testUpstream(w http.ResponseWriter, r *http.Request) {
	if !validUUID(w, r, "upstream_id") {
		return
	}
	u, e := a.repo.Upstream(r.Context(), r.PathValue("upstream_id"))
	if e != nil {
		storageError(w, r, e, "")
		return
	}
	if d := ValidateUpstream(u, a.cfg.AllowPrivateUpstreams); len(d) > 0 {
		fail(w, r, 422, "validation_failed", "invalid upstream", d)
		return
	}
	key, e := auth.Decrypt(a.cfg.MasterKey, u.Ciphertext, u.Nonce)
	if e != nil {
		fail(w, r, 500, "internal_error", "key decryption failed", nil)
		return
	}
	base, _ := url.Parse(u.BaseURL)
	base.Path = strings.TrimRight(base.Path, "/") + "/v1/models"
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(u.Connect+u.FirstByte)*time.Millisecond)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, "GET", base.String(), nil)
	if e != nil {
		fail(w, r, 500, "internal_error", "test request failed", nil)
		return
	}
	if u.AuthStyle == "bearer" {
		req.Header.Set("Authorization", "Bearer "+key)
	} else {
		req.Header.Set("x-api-key", key)
	}
	dial := safeDial(a.cfg.AllowPrivateUpstreams)
	tr := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, n, addr string) (net.Conn, error) {
		ctx, c := context.WithTimeout(ctx, time.Duration(u.Connect)*time.Millisecond)
		defer c()
		return dial(ctx, n, addr)
	}, TLSHandshakeTimeout: time.Duration(u.Connect) * time.Millisecond, ResponseHeaderTimeout: time.Duration(u.FirstByte) * time.Millisecond}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	start := time.Now()
	res, e := client.Do(req)
	var status *int
	var summary *string
	ok := false
	if e == nil {
		defer res.Body.Close()
		status = &res.StatusCode
		ok = res.StatusCode >= 200 && res.StatusCode < 300
		if !ok {
			s := "upstream returned a non-success status"
			summary = &s
		}
	} else {
		s := "upstream connection or response timeout failure"
		summary = &s
	}
	if e = a.repo.RecordAudit(r.Context(), auditFor(r, "upstream.test", "upstream", u.ID)); e != nil {
		storageError(w, r, e, "")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": ok, "status": status, "duration_ms": time.Since(start).Milliseconds(), "error": summary})
}

// DNS is validated when storing a public upstream; the transport revalidates and pins each connection.
func validatePublicHost(ctx context.Context, raw string, lookup func(context.Context, string) ([]net.IPAddr, error)) error {
	u, e := url.Parse(raw)
	if e != nil {
		return errors.New("invalid upstream URL")
	}
	ips, e := lookup(ctx, u.Hostname())
	if e != nil || len(ips) == 0 {
		return errors.New("upstream hostname cannot be resolved")
	}
	for _, ip := range ips {
		if !publicIP(ip.IP) {
			return errors.New("upstream hostname resolves to a blocked address")
		}
	}
	return nil
}
