package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/IvanLi-CN/token-ne/server/internal/auth"
	"github.com/IvanLi-CN/token-ne/server/internal/authsettings"
	"github.com/IvanLi-CN/token-ne/server/internal/config"
	"github.com/IvanLi-CN/token-ne/server/internal/store"
)

func (a *API) authConfig(w http.ResponseWriter, r *http.Request) {
	settings, e := authsettings.Load(r.Context(), a.repo, a.cfg)
	if e != nil {
		storageError(w, r, e, "")
		return
	}
	configured := settings.OIDCEnabled && settings.IssuerURL != nil && settings.ClientID != nil && settings.ClientSecret != ""
	available := false
	if configured {
		_, e = a.repo.OIDCBindingAny(r.Context())
		available = e == nil
		if e != nil && !errors.Is(e, store.ErrNotFound) {
			storageError(w, r, e, "")
			return
		}
	}
	writeJSON(w, 200, map[string]any{
		"local_login_enabled":  settings.LocalLoginEnabled,
		"oidc_enabled":         settings.OIDCEnabled,
		"oidc_login_available": available,
	})
}

func (a *API) getAuthSettings(w http.ResponseWriter, r *http.Request) {
	settings, e := authsettings.LoadForAdmin(r.Context(), a.repo, a.cfg, currentAdmin(r).ID)
	if e != nil {
		settingsError(w, r, e)
		return
	}
	writeJSON(w, 200, settings)
}

func (a *API) updateAuthSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Revision          int64    `json:"revision"`
		OIDCEnabled       bool     `json:"oidc_enabled"`
		IssuerURL         *string  `json:"issuer_url"`
		ClientID          *string  `json:"client_id"`
		ClientSecret      *string  `json:"client_secret"`
		CurrentPassword   *string  `json:"current_password"`
		Scopes            []string `json:"scopes"`
		LocalLoginEnabled bool     `json:"local_login_enabled"`
		SessionTTLHours   int      `json:"session_ttl_hours"`
	}
	if !decode(w, r, &body, "revision", "oidc_enabled", "scopes", "local_login_enabled", "session_ttl_hours") {
		return
	}
	admin := currentAdmin(r)
	passwordOK := false
	if body.CurrentPassword != nil {
		passwordOK = auth.VerifyPassword(admin.PasswordHash, *body.CurrentPassword)
	}
	audit := auditFor(r, "settings.auth.update", "auth_settings", "")
	payload := map[string]any{
		"oidc_enabled":          body.OIDCEnabled,
		"local_login_enabled":   body.LocalLoginEnabled,
		"session_ttl_hours":     body.SessionTTLHours,
		"issuer_url_changed":    body.IssuerURL != nil,
		"client_id_changed":     body.ClientID != nil,
		"client_secret_changed": body.ClientSecret != nil,
		"current_password":      body.CurrentPassword != nil,
	}
	if id, ok := r.Context().Value(requestKey).(string); ok {
		payload["request_id"] = id
	}
	audit.Payload, _ = json.Marshal(payload)
	settings, e := authsettings.Save(r.Context(), a.repo, a.cfg, admin.ID, passwordOK, authsettings.Input{
		OIDCEnabled:       body.OIDCEnabled,
		IssuerURL:         body.IssuerURL,
		ClientID:          body.ClientID,
		ClientSecret:      body.ClientSecret,
		Scopes:            body.Scopes,
		LocalLoginEnabled: body.LocalLoginEnabled,
		SessionTTLHours:   body.SessionTTLHours,
	}, body.Revision, audit)
	if e != nil {
		settingsError(w, r, e)
		return
	}
	writeJSON(w, 200, settings)
}

func (a *API) testAuthSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IssuerURL string  `json:"issuer_url"`
		ClientID  *string `json:"client_id"`
	}
	if !decode(w, r, &body, "issuer_url") {
		return
	}
	type testError struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	type result struct {
		OK        bool            `json:"ok"`
		Discovery *auth.Discovery `json:"discovery,omitempty"`
		Warnings  []string        `json:"warnings,omitempty"`
		Error     *testError      `json:"error,omitempty"`
	}
	issuer := strings.TrimRight(strings.TrimSpace(body.IssuerURL), "/")
	if e := config.ValidateIssuerURL(issuer); e != nil {
		fail(w, r, 422, "validation_failed", "invalid issuer URL", detail("issuer_url", e.Error()))
		return
	}
	discovery, e := a.oidc.Discover(r.Context(), issuer)
	if e != nil {
		a.oidcDiscoveryMetric("failure")
		writeJSON(w, 200, result{OK: false, Error: &testError{Code: "discovery_failed", Message: e.Error()}})
		return
	}
	if discovery.Issuer != issuer {
		a.oidcDiscoveryMetric("issuer_mismatch")
		writeJSON(w, 200, result{OK: false, Discovery: &discovery, Error: &testError{Code: "issuer_mismatch", Message: "discovery issuer does not match configured issuer"}})
		return
	}
	var warnings []string
	if len(discovery.CodeChallengeMethodsSupported) > 0 && !contains(discovery.CodeChallengeMethodsSupported, "S256") {
		warnings = append(warnings, "provider does not advertise PKCE S256")
	}
	a.oidcDiscoveryMetric("success")
	writeJSON(w, 200, result{OK: true, Discovery: &discovery, Warnings: warnings})
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func settingsError(w http.ResponseWriter, r *http.Request, e error) {
	var validation *authsettings.ValidationError
	if errors.As(e, &validation) {
		details := make([]Detail, 0, len(validation.Fields))
		for _, field := range validation.Fields {
			details = append(details, Detail{Path: field.Path, Reason: field.Reason})
		}
		fail(w, r, 422, "validation_failed", "invalid authentication settings", details)
		return
	}
	if errors.Is(e, store.ErrConflict) {
		fail(w, r, 409, "conflict", "settings revision does not match", detail("revision", "reload settings and retry"))
		return
	}
	storageError(w, r, e, "")
}
