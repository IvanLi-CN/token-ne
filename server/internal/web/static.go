package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
)

//go:embed dist/*
var assets embed.FS
var hashed = regexp.MustCompile("[.-][a-fA-F0-9]{8,}[.-]")

func Handler() http.Handler {
	root, _ := fs.Sub(assets, "dist")
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			w.WriteHeader(405)
			return
		}
		for _, p := range []string{"/api", "/v1", "/internal"} {
			if r.URL.Path == p || strings.HasPrefix(r.URL.Path, p+"/") {
				http.NotFound(w, r)
				return
			}
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "." || name == "" {
			name = "index.html"
		}
		info, e := fs.Stat(root, name)
		if e != nil || info.IsDir() {
			name = "index.html"
		}
		if name == "index.html" {
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			b, _ := fs.ReadFile(root, name)
			if r.Method != "HEAD" {
				_, _ = w.Write(b)
			}
			return
		}
		if hashed.MatchString(name) {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}
