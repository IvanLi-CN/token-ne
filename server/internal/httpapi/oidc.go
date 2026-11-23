package httpapi

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/IvanLi-CN/token-ne/server/internal/auth"
	"github.com/IvanLi-CN/token-ne/server/internal/authsettings"
	"github.com/IvanLi-CN/token-ne/server/internal/store"
	"github.com/google/uuid"
)

const oidcFlowCookie = "tn_oidc_flow"
const oidcFlowTTL = 10 * time.Minute

func (a *API) oidcStart(w http.ResponseWriter, r *http.Request) {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !a.allowLogin(ip) {
		a.oidcLoginMetric("rate_limited")
		a.redirectLoginError(w, r)
		return
	}
	settings, e := authsettings.Load(r.Context(), a.repo, a.cfg)
	if e != nil || !oidcReady(settings) {
		a.oidcLoginMetric("unavailable")
		a.redirectLoginError(w, r)
		return
	}
	if _, e = a.repo.OIDCBindingAny(r.Context()); e != nil {
		a.oidcLoginMetric("unbound")
		a.redirectLoginError(w, r)
		return
	}
	flow, state, verifier, e := a.newFlow(r, "login", nil, safeReturnPath(r.URL.Query().Get("returnTo")))
	if e != nil {
		a.redirectLoginError(w, r)
		return
	}
	authorizationURL, e := a.oidc.AuthorizationURL(r.Context(), *settings.IssuerURL, *settings.ClientID, a.redirectURL(), settings.Scopes, state, flow.Nonce, verifier)
	if e != nil {
		a.oidcFailure(r, "login", nil, *settings.IssuerURL, nil, e)
		a.oidcLoginMetric("failure")
		a.redirectLoginError(w, r)
		return
	}
	a.oidcLoginMetric("started")
	http.SetCookie(w, a.flowCookie(flow.ID, flow.ExpiresAt))
	http.Redirect(w, r, authorizationURL, http.StatusFound)
}

func (a *API) oidcBindStart(w http.ResponseWriter, r *http.Request) {
	settings, e := authsettings.Load(r.Context(), a.repo, a.cfg)
	if e != nil {
		storageError(w, r, e, "")
		return
	}
	if !oidcReady(settings) {
		fail(w, r, 409, "conflict", "OIDC is not configured", nil)
		return
	}
	admin := currentAdmin(r)
	flow, state, verifier, e := a.newFlow(r, "bind", &admin.ID, "/system/settings")
	if e != nil {
		storageError(w, r, e, "")
		return
	}
	authorizationURL, e := a.oidc.AuthorizationURL(r.Context(), *settings.IssuerURL, *settings.ClientID, a.redirectURL(), settings.Scopes, state, flow.Nonce, verifier)
	if e != nil {
		a.oidcFailure(r, "bind", &admin.ID, *settings.IssuerURL, nil, e)
		a.oidcLoginMetric("failure")
		fail(w, r, 503, "upstream_unavailable", "OIDC provider is unavailable", nil)
		return
	}
	a.oidcLoginMetric("bind_started")
	http.SetCookie(w, a.flowCookie(flow.ID, flow.ExpiresAt))
	writeJSON(w, 200, map[string]any{"authorization_url": authorizationURL, "expires_at": flow.ExpiresAt})
}

func (a *API) oidcCallback(w http.ResponseWriter, r *http.Request) {
	flowID, e := a.flowIDFromCookie(r)
	if e != nil {
		a.redirectLoginError(w, r)
		return
	}
	state := r.URL.Query().Get("state")
	if state == "" {
		a.redirectLoginError(w, r)
		return
	}
	hash := sha256.Sum256([]byte(state))
	flow, e := a.repo.AuthFlow(r.Context(), hash[:])
	if e != nil || flow.ID != flowID {
		a.redirectLoginError(w, r)
		return
	}
	clear := a.flowCookie("", time.Unix(1, 0))
	clear.MaxAge = -1
	http.SetCookie(w, clear)
	if e = a.repo.ConsumeAuthFlow(r.Context(), flow.ID, time.Now()); e != nil {
		a.redirectModeError(w, r, flow.Mode)
		return
	}
	settings, e := authsettings.Load(r.Context(), a.repo, a.cfg)
	if e != nil || !oidcReady(settings) {
		a.redirectModeError(w, r, flow.Mode)
		return
	}
	verifier, e := auth.Decrypt(a.cfg.MasterKey, flow.PKCEVerifierCiphertext, flow.PKCEVerifierNonce)
	if e != nil {
		a.redirectModeError(w, r, flow.Mode)
		return
	}
	identity, e := a.oidc.Exchange(r.Context(), *settings.IssuerURL, *settings.ClientID, settings.ClientSecret, a.redirectURL(), r.URL.Query().Get("code"), verifier, flow.Nonce)
	if e != nil {
		a.oidcFailure(r, flow.Mode, flow.AdminID, *settings.IssuerURL, nil, e)
		a.oidcLoginMetric("failure")
		a.redirectModeError(w, r, flow.Mode)
		return
	}
	if flow.Mode == "bind" {
		a.finishBind(w, r, flow, identity, settings)
		return
	}
	a.finishLogin(w, r, flow, identity, settings)
}

func (a *API) finishLogin(w http.ResponseWriter, r *http.Request, flow store.AuthFlow, identity auth.Identity, settings authsettings.Settings) {
	binding, e := a.repo.OIDCBindingAny(r.Context())
	if e != nil || binding.Issuer != identity.Issuer || binding.Subject != identity.Subject {
		a.oidcFailure(r, "login", nil, *settings.IssuerURL, &identity, errors.New("OIDC identity is not bound"))
		a.oidcLoginMetric("failure")
		a.redirectLoginError(w, r)
		return
	}
	admin, e := a.repo.AdminByID(r.Context(), binding.AdminID)
	if e != nil {
		a.oidcFailure(r, "login", &binding.AdminID, *settings.IssuerURL, &identity, e)
		a.oidcLoginMetric("failure")
		a.redirectLoginError(w, r)
		return
	}
	if e = a.repo.RecordAudit(r.Context(), a.auditForSubject(r, admin.Username, "auth.oidc.login", admin.ID, identity)); e != nil {
		slog.Warn("OIDC login audit failed", "error", e)
	}
	if e = a.issueSession(w, admin, settings.SessionTTLHours); e != nil {
		a.oidcLoginMetric("failure")
		a.redirectLoginError(w, r)
		return
	}
	a.oidcLoginMetric("success")
	http.Redirect(w, r, safeReturnPath(valueOr(flow.ReturnTo, "/")), http.StatusFound)
}

func (a *API) finishBind(w http.ResponseWriter, r *http.Request, flow store.AuthFlow, identity auth.Identity, settings authsettings.Settings) {
	if flow.AdminID == nil {
		a.oidcFailure(r, "bind", nil, *settings.IssuerURL, &identity, errors.New("OIDC bind flow has no administrator"))
		a.oidcLoginMetric("failure")
		a.redirectBindError(w, r)
		return
	}
	admin, e := a.repo.AdminByID(r.Context(), *flow.AdminID)
	if e != nil {
		a.oidcFailure(r, "bind", flow.AdminID, *settings.IssuerURL, &identity, e)
		a.oidcLoginMetric("failure")
		a.redirectBindError(w, r)
		return
	}
	version, e := a.repo.ReplaceOIDCBinding(r.Context(), admin.ID, identity.Issuer, identity.Subject, a.auditForSubject(r, admin.Username, "auth.oidc.bind", admin.ID, identity))
	if e != nil {
		a.oidcFailure(r, "bind", &admin.ID, *settings.IssuerURL, &identity, e)
		a.oidcLoginMetric("failure")
		a.redirectBindError(w, r)
		return
	}
	admin.SessionVersion = version
	if e = a.issueSession(w, admin, settings.SessionTTLHours); e != nil {
		a.oidcLoginMetric("failure")
		a.redirectBindError(w, r)
		return
	}
	a.oidcLoginMetric("bind_success")
	http.Redirect(w, r, "/system/settings?oidc=bound", http.StatusFound)
}

func (a *API) newFlow(r *http.Request, mode string, adminID *string, returnTo string) (store.AuthFlow, string, string, error) {
	state, e := auth.RandomToken(32)
	if e != nil {
		return store.AuthFlow{}, "", "", e
	}
	nonce, e := auth.RandomToken(32)
	if e != nil {
		return store.AuthFlow{}, "", "", e
	}
	verifier, e := auth.RandomToken(32)
	if e != nil {
		return store.AuthFlow{}, "", "", e
	}
	ciphertext, nonceBytes, e := auth.Encrypt(a.cfg.MasterKey, verifier)
	if e != nil {
		return store.AuthFlow{}, "", "", e
	}
	hash := sha256.Sum256([]byte(state))
	flow := store.AuthFlow{
		ID:                     uuid.NewString(),
		StateHash:              hash[:],
		Nonce:                  nonce,
		PKCEVerifierCiphertext: ciphertext,
		PKCEVerifierNonce:      nonceBytes,
		Mode:                   mode,
		AdminID:                adminID,
		ReturnTo:               &returnTo,
		ExpiresAt:              time.Now().Add(oidcFlowTTL),
	}
	if e = a.repo.CreateAuthFlow(r.Context(), flow); e != nil {
		return store.AuthFlow{}, "", "", e
	}
	return flow, state, verifier, nil
}

func (a *API) flowCookie(id string, expires time.Time) *http.Cookie {
	value := id + "." + auth.Sign([]byte(a.cfg.SessionSecret), id)
	return &http.Cookie{Name: oidcFlowCookie, Value: value, Path: "/api/auth/oidc", HttpOnly: true, Secure: strings.HasPrefix(a.cfg.PublicOrigin, "https://"), SameSite: http.SameSiteLaxMode, Expires: expires, MaxAge: int(time.Until(expires).Seconds())}
}

func (a *API) flowIDFromCookie(r *http.Request) (string, error) {
	c, e := r.Cookie(oidcFlowCookie)
	if e != nil {
		return "", e
	}
	id, signature, ok := strings.Cut(c.Value, ".")
	if !ok || id == "" || !auth.VerifySignature([]byte(a.cfg.SessionSecret), id, signature) {
		return "", errors.New("invalid OIDC flow cookie")
	}
	return id, nil
}

func (a *API) redirectURL() string {
	return strings.TrimRight(a.cfg.PublicOrigin, "/") + "/api/auth/oidc/callback"
}

func (a *API) issueSession(w http.ResponseWriter, admin store.Admin, ttlHours int) error {
	if ttlHours < 1 {
		ttlHours = 720
	}
	expires := time.Now().Add(time.Duration(ttlHours) * time.Hour)
	token, e := auth.EncodeSession([]byte(a.cfg.SessionSecret), auth.Session{AdminID: admin.ID, Expires: expires.Unix(), Version: admin.SessionVersion})
	if e != nil {
		return e
	}
	http.SetCookie(w, auth.Cookie(token, a.cfg.PublicOrigin, expires))
	return nil
}

func (a *API) redirectLoginError(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/login?error=oidc_failed", http.StatusFound)
}

func (a *API) redirectBindError(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/system/settings?oidc=bind_failed", http.StatusFound)
}

func (a *API) redirectModeError(w http.ResponseWriter, r *http.Request, mode string) {
	if mode == "bind" {
		a.redirectBindError(w, r)
		return
	}
	a.redirectLoginError(w, r)
}

func oidcReady(v authsettings.Settings) bool {
	return v.OIDCEnabled && v.IssuerURL != nil && v.ClientID != nil && v.ClientSecret != ""
}

func (a *API) oidcLoginMetric(result string) {
	if a.metrics != nil {
		a.metrics.OIDCLogin(result)
	}
}

func (a *API) oidcDiscoveryMetric(result string) {
	if a.metrics != nil {
		a.metrics.OIDCDiscovery(result)
	}
}

func (a *API) auditForSubject(r *http.Request, actor, action, adminID string, identity auth.Identity) store.Audit {
	payload, _ := json.Marshal(map[string]string{"issuer": identity.Issuer, "subject": identity.Subject, "result": "success"})
	return a.auditSubject(r, actor, action, adminID, payload)
}

func (a *API) auditSubject(r *http.Request, actor, action, adminID string, payload json.RawMessage) store.Audit {
	var fields map[string]any
	if len(payload) == 0 || json.Unmarshal(payload, &fields) != nil || fields == nil {
		fields = map[string]any{}
	}
	if id, ok := r.Context().Value(requestKey).(string); ok {
		fields["request_id"] = id
	}
	payload, _ = json.Marshal(fields)
	audit := store.Audit{Actor: actor, Action: action, TargetType: "admin", Payload: payload}
	if adminID != "" {
		audit.TargetID = &adminID
	}
	if host, _, e := net.SplitHostPort(r.RemoteAddr); e == nil && net.ParseIP(host) != nil {
		audit.ClientIP = &host
	}
	return audit
}

func (a *API) oidcFailure(r *http.Request, mode string, adminID *string, issuer string, identity *auth.Identity, err error) {
	slog.Warn("OIDC flow failed", "mode", mode, "error", err)
	actor := "oidc"
	target := ""
	if adminID != nil {
		target = *adminID
		if admin, e := a.repo.AdminByID(r.Context(), target); e == nil {
			actor = admin.Username
		}
	}
	fields := map[string]string{"result": "failure", "mode": mode, "reason": err.Error()}
	if issuer != "" {
		fields["issuer"] = issuer
	}
	if identity != nil {
		fields["issuer"] = identity.Issuer
		fields["subject"] = identity.Subject
	}
	payload, _ := json.Marshal(fields)
	if e := a.repo.RecordAudit(r.Context(), a.auditSubject(r, actor, "auth.oidc.failure", target, payload)); e != nil {
		slog.Warn("OIDC failure audit failed", "error", e)
	}
}

func safeReturnPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.Contains(raw, "\\") || strings.ContainsFunc(raw, unicode.IsControl) {
		return "/"
	}
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "" || u.Host != "" || u.Path == "" || strings.HasPrefix(u.Path, "//") {
		return "/"
	}
	return u.RequestURI()
}

func valueOr(v *string, fallback string) string {
	if v == nil || *v == "" {
		return fallback
	}
	return *v
}
