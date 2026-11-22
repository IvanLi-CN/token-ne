package httpapi

import (
	"net/http"

	"github.com/IvanLi-CN/token-ne/server/internal/version"
)

func (a *API) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"version":  version.Version,
		"commit":   version.Commit,
		"built_at": version.BuiltAt,
	})
}
