package config

import (
	"encoding/base64"
	"errors"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"unicode"
)

type AuthEnvSet struct {
	OIDCEnabled       bool
	OIDCIssuerURL     bool
	OIDCClientID      bool
	OIDCClientSecret  bool
	OIDCScopes        bool
	LocalLoginEnabled bool
	SessionTTLHours   bool
}

type Config struct {
	HTTPListen, MetricsListen, PublicOrigin, DatabaseURL                        string
	AdminUsername, AdminPassword, SessionSecret, MetricsToken, CoreControlToken string
	MasterKey                                                                   []byte
	AllowPrivateUpstreams                                                       bool

	OIDCEnabled      bool
	OIDCIssuerURL    string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCScopes       []string
	OIDCCaFile       string

	LocalLoginEnabled bool
	SessionTTLHours   int

	AuthEnv              AuthEnvSet
	StreamUsageInjection bool
	// SecondPhaseModelManagement is the single deployment switch for the
	// phase-two management contract; it is published as
	// features.second_phase_model_management by GET /api/version and defaults
	// to false.
	SecondPhaseModelManagement bool
}

func Load() (Config, error) {
	c := Config{
		HTTPListen:        env("HTTP_LISTEN", "0.0.0.0:8080"),
		MetricsListen:     env("METRICS_LISTEN", "0.0.0.0:9090"),
		PublicOrigin:      os.Getenv("PUBLIC_ORIGIN"),
		DatabaseURL:       os.Getenv("DATABASE_URL"),
		AdminUsername:     os.Getenv("ADMIN_USERNAME"),
		AdminPassword:     os.Getenv("ADMIN_PASSWORD"),
		SessionSecret:     os.Getenv("SESSION_SECRET"),
		MetricsToken:      os.Getenv("METRICS_TOKEN"),
		CoreControlToken:  os.Getenv("CORE_CONTROL_TOKEN"),
		OIDCScopes:        []string{"openid", "profile", "email"},
		LocalLoginEnabled: true,
		SessionTTLHours:   720,
	}
	var e error
	c.MasterKey, e = base64.StdEncoding.DecodeString(os.Getenv("MASTER_KEY"))
	if e != nil || len(c.MasterKey) != 32 {
		return c, errors.New("MASTER_KEY must be base64 encoded 32 bytes")
	}
	if s := os.Getenv("ALLOW_PRIVATE_UPSTREAMS"); s != "" {
		c.AllowPrivateUpstreams, e = strconv.ParseBool(s)
		if e != nil {
			return c, errors.New("ALLOW_PRIVATE_UPSTREAMS must be boolean")
		}
	}
	if v, ok := lookup("OIDC_ENABLED"); ok {
		c.AuthEnv.OIDCEnabled = true
		c.OIDCEnabled, e = strconv.ParseBool(v)
		if e != nil {
			return c, errors.New("OIDC_ENABLED must be boolean")
		}
	}
	if v, ok := lookup("OIDC_ISSUER_URL"); ok {
		c.AuthEnv.OIDCIssuerURL = true
		c.OIDCIssuerURL = v
	}
	if v, ok := lookup("OIDC_CLIENT_ID"); ok {
		c.AuthEnv.OIDCClientID = true
		c.OIDCClientID = v
	}
	if v, ok := lookup("OIDC_CLIENT_SECRET"); ok {
		c.AuthEnv.OIDCClientSecret = true
		c.OIDCClientSecret = v
	}
	if v, ok := lookup("OIDC_SCOPES"); ok {
		c.AuthEnv.OIDCScopes = true
		c.OIDCScopes = strings.Fields(v)
	}
	if v, ok := lookup("AUTH_LOCAL_LOGIN_ENABLED"); ok {
		c.AuthEnv.LocalLoginEnabled = true
		c.LocalLoginEnabled, e = strconv.ParseBool(v)
		if e != nil {
			return c, errors.New("AUTH_LOCAL_LOGIN_ENABLED must be boolean")
		}
	}
	if v, ok := lookup("AUTH_SESSION_TTL_HOURS"); ok {
		c.AuthEnv.SessionTTLHours = true
		c.SessionTTLHours, e = strconv.Atoi(v)
		if e != nil {
			return c, errors.New("AUTH_SESSION_TTL_HOURS must be an integer")
		}
	}
	c.OIDCCaFile = os.Getenv("OIDC_CA_FILE")
	c.StreamUsageInjection = true
	if s := os.Getenv("STREAM_USAGE_INJECTION"); s != "" {
		c.StreamUsageInjection, e = strconv.ParseBool(s)
		if e != nil {
			return c, errors.New("STREAM_USAGE_INJECTION must be boolean")
		}
	}
	if s := os.Getenv("SECOND_PHASE_MODEL_MANAGEMENT"); s != "" {
		c.SecondPhaseModelManagement, e = strconv.ParseBool(s)
		if e != nil {
			return c, errors.New("SECOND_PHASE_MODEL_MANAGEMENT must be boolean")
		}
	}
	return c, c.Validate()
}

// LoadForAuthCLI loads only the settings required to open the database and
// bootstrap the administrator. Authentication environment variables are
// intentionally ignored: a broken OIDC configuration must not block
// break-glass recovery.
func LoadForAuthCLI() (Config, error) {
	c := Config{
		DatabaseURL:       os.Getenv("DATABASE_URL"),
		AdminUsername:     os.Getenv("ADMIN_USERNAME"),
		AdminPassword:     os.Getenv("ADMIN_PASSWORD"),
		LocalLoginEnabled: true,
		SessionTTLHours:   720,
		OIDCScopes:        []string{"openid", "profile", "email"},
	}
	var e error
	c.MasterKey, e = base64.StdEncoding.DecodeString(os.Getenv("MASTER_KEY"))
	if e != nil || len(c.MasterKey) != 32 {
		return c, errors.New("MASTER_KEY must be base64 encoded 32 bytes")
	}
	if strings.TrimSpace(c.DatabaseURL) == "" {
		return c, errors.New("DATABASE_URL is required")
	}
	if strings.TrimSpace(c.AdminUsername) == "" || c.AdminPassword == "" {
		return c, errors.New("ADMIN_USERNAME and ADMIN_PASSWORD are required for bootstrap")
	}
	return c, nil
}

func (c Config) Validate() error {
	for _, a := range []string{c.HTTPListen, c.MetricsListen} {
		_, port, e := net.SplitHostPort(a)
		if e != nil {
			return errors.New("invalid listener address")
		}
		if n, e := strconv.Atoi(port); e != nil || n < 1 || n > 65535 {
			return errors.New("listener port must be between 1 and 65535")
		}
	}
	if c.HTTPListen == c.MetricsListen {
		return errors.New("HTTP and metrics listeners must differ")
	}
	u, e := url.Parse(c.PublicOrigin)
	if e != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("PUBLIC_ORIGIN must be an http(s) origin without path")
	}
	d, e := url.Parse(c.DatabaseURL)
	if e != nil || (d.Scheme != "postgres" && d.Scheme != "postgresql") || d.Hostname() == "" {
		return errors.New("DATABASE_URL must be a PostgreSQL URL")
	}
	if len(c.MasterKey) != 32 || len(c.SessionSecret) < 32 || c.MetricsToken == "" || len(c.CoreControlToken) < 32 {
		return errors.New("MASTER_KEY, CORE_CONTROL_TOKEN (at least 32 bytes), SESSION_SECRET (at least 32 bytes), and METRICS_TOKEN are required")
	}
	if strings.TrimSpace(c.AdminUsername) == "" || len(c.AdminUsername) > 128 || c.AdminPassword == "" {
		return errors.New("ADMIN_USERNAME and ADMIN_PASSWORD are required for bootstrap")
	}
	if c.SessionTTLHours < 1 || c.SessionTTLHours > 2160 {
		return errors.New("AUTH_SESSION_TTL_HOURS must be between 1 and 2160")
	}
	if len(c.OIDCScopes) == 0 {
		return errors.New("OIDC_SCOPES must not be empty")
	}
	hasOpenID := false
	for _, scope := range c.OIDCScopes {
		if !validScope(scope) {
			return errors.New("OIDC_SCOPES contains an invalid scope")
		}
		hasOpenID = hasOpenID || scope == "openid"
	}
	if !hasOpenID {
		return errors.New("OIDC_SCOPES must contain openid")
	}
	if c.OIDCIssuerURL != "" {
		if err := ValidateIssuerURL(c.OIDCIssuerURL); err != nil {
			return err
		}
	}
	if c.OIDCClientID != "" && (len(c.OIDCClientID) > 256 || strings.ContainsFunc(c.OIDCClientID, unicode.IsControl)) {
		return errors.New("OIDC_CLIENT_ID must be 1 to 256 characters without control characters")
	}
	if c.OIDCCaFile != "" {
		if _, err := os.ReadFile(c.OIDCCaFile); err != nil {
			return errors.New("OIDC_CA_FILE must be readable")
		}
	}
	return nil
}

func ValidateIssuerURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("OIDC issuer must be an absolute URL without credentials, query or fragment")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && isLoopback(u.Hostname()) {
		return nil
	}
	return errors.New("OIDC issuer must use https, or http on loopback")
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validScope(scope string) bool {
	if scope == "" || len(scope) > 128 {
		return false
	}
	for _, r := range scope {
		if r > unicode.MaxASCII || unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func lookup(k string) (string, bool) {
	v, ok := os.LookupEnv(k)
	return v, ok && v != ""
}

func env(k, f string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return f
}
