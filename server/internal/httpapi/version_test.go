package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/IvanLi-CN/token-ne/server/internal/version"
)

func TestVersionEndpoint(t *testing.T) {
	oldVersion, oldCommit, oldBuiltAt := version.Version, version.Commit, version.BuiltAt
	version.Version, version.Commit, version.BuiltAt = "1.2.3", "abc1234", "2026-11-22T10:00:00Z"
	t.Cleanup(func() {
		version.Version, version.Commit, version.BuiltAt = oldVersion, oldCommit, oldBuiltAt
	})
	router, _, _, _ := testAPI(t)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/version", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var payload map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["version"] != "1.2.3" || payload["commit"] != "abc1234" || payload["built_at"] != "2026-11-22T10:00:00Z" {
		t.Fatalf("payload = %#v", payload)
	}
}
