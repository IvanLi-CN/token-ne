package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

const oidcCacheTTL = 10 * time.Minute

type Discovery struct {
	Issuer                        string   `json:"issuer"`
	AuthorizationEndpoint         string   `json:"authorization_endpoint"`
	TokenEndpoint                 string   `json:"token_endpoint"`
	JWKSURI                       string   `json:"jwks_uri"`
	ScopesSupported               []string `json:"scopes_supported,omitempty"`
	CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported,omitempty"`
}

type Identity struct {
	Issuer            string
	Subject           string
	PreferredUsername string
	Email             string
}

type OIDCOptions struct {
	CAFile string
	Now    func() time.Time
}

type OIDC struct {
	client  *http.Client
	initErr error
	now     func() time.Time

	mu        sync.Mutex
	discovery map[string]cacheEntry[Discovery]
	providers map[string]cacheEntry[*oidc.Provider]
}

type cacheEntry[T any] struct {
	value   T
	fetched time.Time
}

func NewOIDC(opts OIDCOptions) *OIDC {
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	var initErr error
	if opts.CAFile != "" {
		pool, e := x509.SystemCertPool()
		if e != nil {
			pool = x509.NewCertPool()
		}
		pem, e := os.ReadFile(opts.CAFile)
		if e == nil && !pool.AppendCertsFromPEM(pem) {
			e = errors.New("OIDC_CA_FILE contains no certificates")
		}
		if e != nil {
			initErr = fmt.Errorf("load OIDC CA file: %w", e)
		} else if transport, ok := http.DefaultTransport.(*http.Transport); ok {
			clone := transport.Clone()
			clone.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
			client.Transport = clone
		}
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &OIDC{client: client, initErr: initErr, now: now, discovery: map[string]cacheEntry[Discovery]{}, providers: map[string]cacheEntry[*oidc.Provider]{}}
}

func (o *OIDC) InitError() error { return o.initErr }

func (o *OIDC) Discover(ctx context.Context, issuer string) (Discovery, error) {
	if o.initErr != nil {
		return Discovery{}, o.initErr
	}
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	if issuer == "" {
		return Discovery{}, errors.New("issuer is empty")
	}
	now := o.now()
	o.mu.Lock()
	if entry, ok := o.discovery[issuer]; ok && now.Sub(entry.fetched) < oidcCacheTTL {
		o.mu.Unlock()
		return entry.value, nil
	}
	o.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, issuer+"/.well-known/openid-configuration", nil)
	if e != nil {
		return Discovery{}, e
	}
	resp, e := o.client.Do(req)
	if e != nil {
		return Discovery{}, e
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Discovery{}, fmt.Errorf("discovery returned HTTP %d", resp.StatusCode)
	}
	body, e := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if e != nil {
		return Discovery{}, e
	}
	var d Discovery
	if e = json.Unmarshal(body, &d); e != nil {
		return Discovery{}, e
	}
	if d.Issuer == "" || d.AuthorizationEndpoint == "" || d.TokenEndpoint == "" || d.JWKSURI == "" {
		return Discovery{}, errors.New("discovery document is incomplete")
	}
	o.mu.Lock()
	o.discovery[issuer] = cacheEntry[Discovery]{value: d, fetched: now}
	o.mu.Unlock()
	return d, nil
}

func (o *OIDC) Provider(ctx context.Context, issuer string) (*oidc.Provider, error) {
	if o.initErr != nil {
		return nil, o.initErr
	}
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	now := o.now()
	o.mu.Lock()
	if entry, ok := o.providers[issuer]; ok && now.Sub(entry.fetched) < oidcCacheTTL {
		o.mu.Unlock()
		return entry.value, nil
	}
	o.mu.Unlock()
	provider, e := oidc.NewProvider(oidc.ClientContext(ctx, o.client), issuer)
	if e != nil {
		return nil, e
	}
	o.mu.Lock()
	o.providers[issuer] = cacheEntry[*oidc.Provider]{value: provider, fetched: now}
	o.mu.Unlock()
	return provider, nil
}

func (o *OIDC) AuthorizationURL(ctx context.Context, issuer, clientID, redirectURL string, scopes []string, state, nonce, verifier string) (string, error) {
	d, e := o.Discover(ctx, issuer)
	if e != nil {
		return "", e
	}
	if d.Issuer != strings.TrimRight(issuer, "/") {
		return "", errors.New("discovery issuer does not match configured issuer")
	}
	u, e := url.Parse(d.AuthorizationEndpoint)
	if e != nil || u.Hostname() == "" {
		return "", errors.New("invalid authorization endpoint")
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURL)
	q.Set("scope", strings.Join(scopes, " "))
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("code_challenge", PKCEChallenge(verifier))
	q.Set("code_challenge_method", "S256")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (o *OIDC) Exchange(ctx context.Context, issuer, clientID, clientSecret, redirectURL, code, verifier, nonce string) (Identity, error) {
	d, e := o.Discover(ctx, issuer)
	if e != nil {
		return Identity{}, e
	}
	if d.Issuer != strings.TrimRight(issuer, "/") {
		return Identity{}, errors.New("discovery issuer does not match configured issuer")
	}
	provider, e := o.Provider(ctx, issuer)
	if e != nil {
		return Identity{}, e
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURL},
		"client_id":     {clientID},
		"code_verifier": {verifier},
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, d.TokenEndpoint, strings.NewReader(form.Encode()))
	if e != nil {
		return Identity{}, e
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(clientID, clientSecret)
	resp, e := o.client.Do(req)
	if e != nil {
		return Identity{}, e
	}
	defer resp.Body.Close()
	body, e := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if e != nil {
		return Identity{}, e
	}
	var token struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
	}
	if e = json.Unmarshal(body, &token); e != nil {
		return Identity{}, fmt.Errorf("token endpoint returned invalid JSON")
	}
	if resp.StatusCode != http.StatusOK || token.Error != "" || token.IDToken == "" {
		if token.Error != "" {
			return Identity{}, fmt.Errorf("token endpoint rejected request: %s", token.Error)
		}
		return Identity{}, fmt.Errorf("token endpoint returned HTTP %d", resp.StatusCode)
	}
	verifierConfig := &oidc.Config{
		ClientID:             clientID,
		SupportedSigningAlgs: []string{"RS256", "ES256"},
		Now:                  func() time.Time { return o.now().Add(-60 * time.Second) },
	}
	idToken, e := provider.Verifier(verifierConfig).Verify(ctx, token.IDToken)
	if e != nil {
		return Identity{}, e
	}
	if idToken.Nonce != nonce {
		return Identity{}, errors.New("nonce mismatch")
	}
	var claims struct {
		AZP               string `json:"azp"`
		PreferredUsername string `json:"preferred_username"`
		Email             string `json:"email"`
		Iat               int64  `json:"iat"`
	}
	if e = idToken.Claims(&claims); e != nil {
		return Identity{}, e
	}
	if claims.AZP != "" && claims.AZP != clientID {
		return Identity{}, errors.New("azp mismatch")
	}
	if claims.Iat != 0 && time.Unix(claims.Iat, 0).After(o.now().Add(60*time.Second)) {
		return Identity{}, errors.New("id token issued in the future")
	}
	if idToken.Subject == "" {
		return Identity{}, errors.New("id token subject is missing")
	}
	return Identity{Issuer: idToken.Issuer, Subject: idToken.Subject, PreferredUsername: claims.PreferredUsername, Email: claims.Email}, nil
}

func RandomToken(bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func PKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
