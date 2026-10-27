//go:build standalone

package main

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// The standalone frontend must be generated with npm run build:standalone first.
//go:embed webdist
var bundledWeb embed.FS

func webHandler() http.Handler {
	assets, err := fs.Sub(bundledWeb, "webdist")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		relative := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if relative != "" {
			info, statErr := fs.Stat(assets, relative)
			if statErr != nil || info.IsDir() {
				copyRequest := r.Clone(r.Context())
				copyRequest.URL.Path = "/"
				files.ServeHTTP(w, copyRequest)
				return
			}
		}
		files.ServeHTTP(w, r)
	})
}
