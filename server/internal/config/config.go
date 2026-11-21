package config

import (
	"encoding/base64"
	"errors"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	HTTPListen, MetricsListen, PublicOrigin, DatabaseURL      string
	AdminUsername, AdminPassword, SessionSecret, MetricsToken string
	MasterKey                                                 []byte
	AllowPrivateUpstreams                                     bool
}

func Load() (Config, error) {
	c := Config{HTTPListen: env("HTTP_LISTEN", "0.0.0.0:8080"), MetricsListen: env("METRICS_LISTEN", "0.0.0.0:9090"), PublicOrigin: os.Getenv("PUBLIC_ORIGIN"), DatabaseURL: os.Getenv("DATABASE_URL"), AdminUsername: os.Getenv("ADMIN_USERNAME"), AdminPassword: os.Getenv("ADMIN_PASSWORD"), SessionSecret: os.Getenv("SESSION_SECRET"), MetricsToken: os.Getenv("METRICS_TOKEN")}
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
	return c, c.Validate()
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
	if len(c.MasterKey) != 32 || len(c.SessionSecret) < 32 || c.MetricsToken == "" {
		return errors.New("MASTER_KEY, SESSION_SECRET (at least 32 bytes), and METRICS_TOKEN are required")
	}
	if strings.TrimSpace(c.AdminUsername) == "" || len(c.AdminUsername) > 128 || c.AdminPassword == "" {
		return errors.New("ADMIN_USERNAME and ADMIN_PASSWORD are required for bootstrap")
	}
	return nil
}
func env(k, f string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return f
}
