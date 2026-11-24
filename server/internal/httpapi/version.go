package httpapi

import (
	"net/http"

	"github.com/IvanLi-CN/token-ne/server/internal/version"
)

// runtimeVersion is the GET /api/version payload. It mirrors the RuntimeVersion
// schema in openapi.yaml: features.second_phase_model_management is the single
// deployment-stage switch and is always present.
type runtimeVersion struct {
	Version  string                 `json:"version"`
	Commit   string                 `json:"commit"`
	BuiltAt  string                 `json:"built_at"`
	Features runtimeVersionFeatures `json:"features"`
}

type runtimeVersionFeatures struct {
	SecondPhaseModelManagement bool `json:"second_phase_model_management"`
}

func (a *API) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, runtimeVersion{
		Version:  version.Version,
		Commit:   version.Commit,
		BuiltAt:  version.BuiltAt,
		Features: runtimeVersionFeatures{SecondPhaseModelManagement: a.cfg.SecondPhaseModelManagement},
	})
}
