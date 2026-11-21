package auth

import (
	"bytes"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSecrets(t *testing.T) {
	p, e := HashPassword("a test password")
	if e != nil || !VerifyPassword(p, "a test password") || VerifyPassword(p, "wrong") || VerifyPassword("$argon2id$bad", "x") {
		t.Fatal("password verification")
	}
	s, prefix, h, e := NewKey()
	if e != nil || len(h) != 32 || !bytes.Equal(h, KeyHash(s)) || s[:len(prefix)] != prefix {
		t.Fatal("key generation")
	}
	master := bytes.Repeat([]byte{7}, 32)
	c, n, e := Encrypt(master, s)
	if e != nil {
		t.Fatal(e)
	}
	got, e := Decrypt(master, c, n)
	if e != nil || got != s {
		t.Fatal("encryption round trip")
	}
	c[0] ^= 1
	if _, e = Decrypt(master, c, n); e == nil {
		t.Fatal("tampering accepted")
	}
	if _, e = Decrypt(master, c, nil); e == nil {
		t.Fatal("bad nonce accepted")
	}
	if _, _, e = Encrypt(master[:16], s); e == nil {
		t.Fatal("AES-128 accepted")
	}
	if Hint("abcdef") != "cdef" || Mask(Hint("abcdef")) != "****cdef" {
		t.Fatal("masking")
	}
}
func TestSession(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 32)
	now := time.Now()
	s := Session{uuid.NewString(), now.Add(time.Hour).Unix(), 3}
	v, e := EncodeSession(key, s)
	if e != nil {
		t.Fatal(e)
	}
	got, e := DecodeSession(key, v, now)
	if e != nil || got != s {
		t.Fatal("session roundtrip")
	}
	for _, bad := range []string{v + "x", "", v[:len(v)-4]} {
		if _, e = DecodeSession(key, bad, now); e == nil {
			t.Fatal("tampered session")
		}
	}
	if _, e = DecodeSession(key, v, now.Add(2*time.Hour)); e == nil {
		t.Fatal("expired session")
	}
	c := Cookie(v, "https://example.com", now.Add(time.Hour))
	if !c.Secure || !c.HttpOnly || c.Path != "/" || c.Name != "tn_session" {
		t.Fatal("cookie flags")
	}
	if Cookie(v, "http://localhost", now).Secure {
		t.Fatal("http cookie secure")
	}
}
func TestCSRF(t *testing.T) {
	for _, tc := range []struct {
		origin, site string
		ok           bool
	}{{"https://example.com", "same-origin", true}, {"https://example.com:443", "", true}, {"https://example.com", "cross-site", false}, {"https://evil.example", "same-site", false}, {"http://example.com", "same-origin", false}, {"", "", false}, {"null", "", false}, {"https://example.com/path", "", false}, {"https://example.com.evil", "", false}} {
		r := httptest.NewRequest("POST", "/", nil)
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Sec-Fetch-Site", tc.site)
		if ValidateOrigin(r, "https://example.com") != tc.ok {
			t.Errorf("origin=%q site=%q", tc.origin, tc.site)
		}
	}
}
