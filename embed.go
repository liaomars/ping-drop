// Package pingdrop 提供内嵌前端编译产物与 SPA 路由回退处理器。
package pingdrop

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:web/dist
var embeddedWebFS embed.FS

// NewSPAHandler 返回托管内嵌前端构建产物的 HTTP Handler，并在未知路径时回退至 index.html。
func NewSPAHandler() http.Handler {
	distFS, err := fs.Sub(embeddedWebFS, "web/dist")
	if err != nil {
		panic("embedded web/dist missing: " + err.Error())
	}
	return newSPAHandler(distFS)
}

func newSPAHandler(distFS fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(distFS))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")

		cleanPath := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if cleanPath == "" || cleanPath == "." {
			cleanPath = "index.html"
		}
		if strings.HasPrefix(path.Base(cleanPath), ".") {
			http.NotFound(w, r)
			return
		}

		if info, statErr := fs.Stat(distFS, cleanPath); statErr == nil && !info.IsDir() {
			if strings.HasPrefix(cleanPath, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else if cleanPath == "index.html" {
				w.Header().Set("Cache-Control", "no-cache")
			}
			fileServer.ServeHTTP(w, r)
			return
		}

		if cleanPath != "index.html" && strings.Contains(path.Base(cleanPath), ".") {
			http.NotFound(w, r)
			return
		}

		indexBytes, readErr := fs.ReadFile(distFS, "index.html")
		if readErr != nil {
			http.Error(w, "frontend build artifacts not found", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(indexBytes)
	})
}
