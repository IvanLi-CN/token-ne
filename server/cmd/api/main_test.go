package main

import (
	"context"
	"errors"
	"github.com/IvanLi-CN/token-ne/server/internal/config"
	"github.com/IvanLi-CN/token-ne/server/internal/store"
	"net/http/httptest"
	"testing"
)

type healthRepo struct {
	store.Repository
	err error
}

func (r healthRepo) Ping(context.Context) error { return r.err }
func TestMetricsListener(t *testing.T) {
	cfg := config.Config{MetricsToken: "metrics-secret"}
	for _, tc := range []struct {
		path, token string
		err         error
		status      int
	}{{"/healthz", "", nil, 200}, {"/readyz", "", nil, 200}, {"/readyz", "", errors.New("db down"), 503}, {"/metrics", "", nil, 401}, {"/metrics", "Bearer wrong", nil, 401}, {"/metrics", "Bearer metrics-secret", nil, 200}} {
		h := metricsHandler(cfg, healthRepo{err: tc.err})
		r := httptest.NewRequest("GET", tc.path, nil)
		r.Header.Set("Authorization", tc.token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: %d", tc.path, w.Code)
		}
		if tc.path == "/metrics" && w.Code == 200 && w.Body.Len() == 0 {
			t.Fatal("metrics missing")
		}
	}
}
