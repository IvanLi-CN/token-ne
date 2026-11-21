package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, e := rand.Read(salt); e != nil {
		return "", e
	}
	key := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return fmt.Sprintf("$argon2id$v=19$m=65536,t=3,p=2$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}
func VerifyPassword(encoded, password string) bool {
	p := strings.Split(encoded, "$")
	if len(p) != 6 || p[1] != "argon2id" || p[2] != "v=19" || p[3] != "m=65536,t=3,p=2" {
		return false
	}
	salt, e := base64.RawStdEncoding.DecodeString(p[4])
	if e != nil || len(salt) != 16 {
		return false
	}
	want, e := base64.RawStdEncoding.DecodeString(p[5])
	if e != nil || len(want) != 32 {
		return false
	}
	return subtle.ConstantTimeCompare(argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32), want) == 1
}
func KeyHash(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }
func NewKey() (string, string, []byte, error) {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return "", "", nil, e
	}
	s := "tn_" + base64.RawURLEncoding.EncodeToString(b)
	return s, s[:11], KeyHash(s), nil
}
func Hint(s string) string {
	if len(s) <= 4 {
		return strings.Repeat("*", len(s))
	}
	return s[len(s)-4:]
}
func Mask(s string) string { return "****" + s }
func Encrypt(master []byte, p string) ([]byte, []byte, error) {
	if len(master) != 32 {
		return nil, nil, errors.New("master key must be 32 bytes")
	}
	b, e := aes.NewCipher(master)
	if e != nil {
		return nil, nil, e
	}
	g, e := cipher.NewGCM(b)
	if e != nil {
		return nil, nil, e
	}
	n := make([]byte, g.NonceSize())
	if _, e = rand.Read(n); e != nil {
		return nil, nil, e
	}
	return g.Seal(nil, n, []byte(p), nil), n, nil
}
func Decrypt(master, c, n []byte) (string, error) {
	if len(master) != 32 {
		return "", errors.New("master key must be 32 bytes")
	}
	b, e := aes.NewCipher(master)
	if e != nil {
		return "", e
	}
	g, e := cipher.NewGCM(b)
	if e != nil {
		return "", e
	}
	if len(n) != g.NonceSize() {
		return "", errors.New("invalid nonce")
	}
	p, e := g.Open(nil, n, c, nil)
	return string(p), e
}
func Sign(secret []byte, s string) string {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))
}
func VerifySignature(secret []byte, s, sig string) bool {
	a, e := hex.DecodeString(sig)
	if e != nil {
		return false
	}
	b, _ := hex.DecodeString(Sign(secret, s))
	return hmac.Equal(a, b)
}
