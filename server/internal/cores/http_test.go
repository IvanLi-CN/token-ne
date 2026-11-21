package cores

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInternalAuthenticationAndMismatch(t *testing.T) {
	h := HTTP{Token: "control-secret", PublicMeta: BrokerMeta()}
	mux := http.NewServeMux()
	h.Register(mux)
	for _, tc := range []struct {
		path, auth, protocol, body string
		want                       int
	}{
		{"/internal/v1/meta", "", "1", "", 401},
		{"/internal/v1/meta", "control-secret", "1", "", 401},
		{"/internal/v1/meta", "Bearer control-secret", "2", "", 409},
		{"/internal/v1/meta", "Bearer control-secret", "1", "", 200},
		{"/internal/v1/config?instance_id=" + uuid.NewString(), "Bearer control-secret", "1", "", 400},
	} {
		r := httptest.NewRequest("GET", tc.path, strings.NewReader(tc.body))
		r.Header.Set("Authorization", tc.auth)
		r.Header.Set(ProtocolHeader, tc.protocol)
		id, _ := uuid.NewV7()
		r.Header.Set(RequestHeader, id.String())
		if strings.Contains(tc.path, "config") {
			r.Header.Set(InstanceHeader, uuid.NewString())
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
	id := uuid.NewString()
	r := httptest.NewRequest("PUT", "/internal/v1/config", strings.NewReader(`{"protocol_version":1,"config_schema_version":1,"instance_id":"`+id+`","revision":2}`))
	r.Header.Set("Authorization", "Bearer control-secret")
	r.Header.Set(ProtocolHeader, "1")
	rid, _ := uuid.NewV7()
	r.Header.Set(RequestHeader, rid.String())
	r.Header.Set(InstanceHeader, id)
	r.Header.Set(RevisionHeader, "1")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("revision mismatch accepted: %d", w.Code)
	}
}
func TestCoreClientStrictAcknowledgement(t *testing.T) {
	id := uuid.NewString()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" || !UUID(r.Header.Get(RequestHeader), true) || r.Header.Get(InstanceHeader) != id {
			t.Error("missing control headers")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"protocol_version": 1, "instance_id": id, "applied_revision": 2, "config_schema_version": 1, "signature_state": "off"})
	}))
	defer srv.Close()
	c := NewClient("secret")
	if e := c.Push(context.Background(), strings.TrimPrefix(srv.URL, "http://"), id, Snapshot{Revision: 1, Body: []byte(`{}`)}); e != ErrInvalid {
		t.Fatalf("mismatched ack: %v", e)
	}
}
