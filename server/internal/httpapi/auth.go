package httpapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/auth"
	"github.com/IvanLi-CN/token-ne/server/internal/store"
)

func (a *API) allowLogin(ip string) bool {
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	now := time.Now()
	for k, v := range a.attempts {
		if now.Sub(v.start) > time.Minute {
			delete(a.attempts, k)
		}
	}
	v := a.attempts[ip]
	if v.start.IsZero() {
		if len(a.attempts) >= 4096 {
			return false
		}
		v.start = now
	}
	v.count++
	a.attempts[ip] = v
	return v.count <= 10
}
func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &body, "username", "password") {
		return
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !a.allowLogin(ip) {
		w.Header().Set("Retry-After", "60")
		fail(w, r, 401, "unauthorized", "login temporarily unavailable", nil)
		return
	}
	select {
	case a.loginSlots <- struct{}{}:
		defer func() { <-a.loginSlots }()
	default:
		fail(w, r, 401, "unauthorized", "login temporarily unavailable", nil)
		return
	}
	u, e := a.repo.AdminByUsername(r.Context(), body.Username)
	if e != nil && !errors.Is(e, store.ErrNotFound) {
		storageError(w, r, e, "")
		return
	}
	valid := false
	if e == nil {
		valid = auth.VerifyPassword(u.PasswordHash, body.Password)
	} else {
		auth.VerifyPassword(dummyPasswordHash, body.Password)
	}
	if !valid {
		fail(w, r, 401, "unauthorized", "invalid username or password", nil)
		return
	}
	r = r.WithContext(withAdmin(r, u))
	if e = a.repo.RecordAudit(r.Context(), auditFor(r, "auth.login", "admin", u.ID)); e != nil {
		storageError(w, r, e, "")
		return
	}
	expires := time.Now().Add(auth.SessionTTL)
	token, e := auth.EncodeSession([]byte(a.cfg.SessionSecret), auth.Session{AdminID: u.ID, Expires: expires.Unix(), Version: u.SessionVersion})
	if e != nil {
		fail(w, r, 500, "internal_error", "cannot create session", nil)
		return
	}
	http.SetCookie(w, auth.Cookie(token, a.cfg.PublicOrigin, expires))
	writeJSON(w, 200, u)
}

const dummyPasswordHash = "$argon2id$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	u := currentAdmin(r)
	if e := a.repo.InvalidateSession(r.Context(), u.ID, auditFor(r, "auth.logout", "admin", u.ID)); e != nil {
		storageError(w, r, e, "")
		return
	}
	c := auth.Cookie("", a.cfg.PublicOrigin, time.Unix(1, 0))
	c.MaxAge = -1
	http.SetCookie(w, c)
	w.WriteHeader(204)
}
func withAdmin(r *http.Request, u store.Admin) context.Context {
	return context.WithValue(r.Context(), adminKey, u)
}
