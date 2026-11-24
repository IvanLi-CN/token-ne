package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/IvanLi-CN/token-ne/server/internal/version"
)

type versionPayload struct {
	Version  string `json:"version"`
	Commit   string `json:"commit"`
	BuiltAt  string `json:"built_at"`
	Features struct {
		SecondPhaseModelManagement bool `json:"second_phase_model_management"`
	} `json:"features"`
}

func getVersion(t *testing.T, router http.Handler) (versionPayload, *httptest.ResponseRecorder) {
	t.Helper()
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/version", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var payload versionPayload
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	return payload, recorder
}

func TestVersionEndpoint(t *testing.T) {
	oldVersion, oldCommit, oldBuiltAt := version.Version, version.Commit, version.BuiltAt
	version.Version, version.Commit, version.BuiltAt = "1.2.3", "abc1234", "2026-11-22T10:00:00Z"
	t.Cleanup(func() {
		version.Version, version.Commit, version.BuiltAt = oldVersion, oldCommit, oldBuiltAt
	})
	router, repo, cfg, _ := testAPI(t)
	payload, recorder := getVersion(t, router)
	if payload.Version != "1.2.3" || payload.Commit != "abc1234" || payload.BuiltAt != "2026-11-22T10:00:00Z" {
		t.Fatalf("payload = %#v", payload)
	}
	if payload.Features.SecondPhaseModelManagement {
		t.Fatal("second_phase_model_management must default to false")
	}
	if !strings.Contains(recorder.Body.String(), `"second_phase_model_management":false`) {
		t.Fatalf("features.second_phase_model_management missing from %s", recorder.Body.String())
	}
	contract(t, "/api/version", http.MethodGet, recorder)

	cfg.SecondPhaseModelManagement = true
	payload, recorder = getVersion(t, NewRouter(cfg, repo))
	if !payload.Features.SecondPhaseModelManagement {
		t.Fatalf("enabled deployment switch not published: %s", recorder.Body.String())
	}
	contract(t, "/api/version", http.MethodGet, recorder)
}
