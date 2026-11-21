package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	for k, v := range map[string]string{"MASTER_KEY": base64.StdEncoding.EncodeToString(make([]byte, 32)), "PUBLIC_ORIGIN": "https://example.com", "DATABASE_URL": "postgres://user:password@db/token_ne", "ADMIN_USERNAME": "admin", "ADMIN_PASSWORD": "secret", "SESSION_SECRET": strings.Repeat("s", 32), "METRICS_TOKEN": "metrics", "CORE_CONTROL_TOKEN": strings.Repeat("c", 32), "HTTP_LISTEN": "0.0.0.0:8080", "METRICS_LISTEN": "0.0.0.0:9090", "ALLOW_PRIVATE_UPSTREAMS": "false"} {
		t.Setenv(k, v)
	}
	if _, e := Load(); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct{ k, v string }{{"MASTER_KEY", "bad"}, {"PUBLIC_ORIGIN", "https://example.com/path"}, {"DATABASE_URL", "file:///tmp/db"}, {"SESSION_SECRET", "short"}, {"METRICS_TOKEN", ""}, {"CORE_CONTROL_TOKEN", "short"}, {"ALLOW_PRIVATE_UPSTREAMS", "maybe"}, {"HTTP_LISTEN", "bad"}} {
		t.Run(tc.k, func(t *testing.T) {
			t.Setenv(tc.k, tc.v)
			if _, e := Load(); e == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}
