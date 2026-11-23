package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

const CookieName = "tn_session"

type Session struct {
	AdminID string `json:"sub"`
	Expires int64  `json:"exp"`
	Version int    `json:"sv"`
}

func EncodeSession(secret []byte, s Session) (string, error) {
	b, e := json.Marshal(s)
	if e != nil {
		return "", e
	}
	p := base64.RawURLEncoding.EncodeToString(b)
	return p + "." + Sign(secret, p), nil
}
func DecodeSession(secret []byte, t string, now time.Time) (Session, error) {
	var s Session
	p := strings.Split(t, ".")
	if len(p) != 2 || !VerifySignature(secret, p[0], p[1]) {
		return s, errors.New("invalid session")
	}
	b, e := base64.RawURLEncoding.DecodeString(p[0])
	if e != nil {
		return s, e
	}
	if e = json.Unmarshal(b, &s); e != nil {
		return s, e
	}
	if _, e = uuid.Parse(s.AdminID); e != nil || s.Version < 1 || s.Expires <= now.Unix() {
		return s, errors.New("expired or invalid session")
	}
	return s, nil
}
func Cookie(t, origin string, expires time.Time) *http.Cookie {
	return &http.Cookie{Name: CookieName, Value: t, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(origin, "https://"), SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: int(time.Until(expires).Seconds())}
}
func ValidateOrigin(r *http.Request, origin string) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	a, e := url.Parse(r.Header.Get("Origin"))
	if e != nil || a.Host == "" || a.User != nil || a.RawQuery != "" || a.Fragment != "" || a.Path != "" {
		return false
	}
	b, e := url.Parse(origin)
	if e != nil {
		return false
	}
	port := func(u *url.URL) string {
		if u.Port() != "" {
			return u.Port()
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}
