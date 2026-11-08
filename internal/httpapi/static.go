package httpapi

import (
	"bytes"
	"fmt"
	"html"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"strings"
)

// contentTypeOverrides pins the MIME types the React bundle relies on. Go's
// mime package reads the Windows registry, which can return different values
// for .js/.css than express.static does.
var contentTypeOverrides = map[string]string{
	".html":  "text/html; charset=utf-8",
	".htm":   "text/html; charset=utf-8",
	".js":    "application/javascript; charset=utf-8",
	".mjs":   "application/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".json":  "application/json; charset=utf-8",
	".map":   "application/json; charset=utf-8",
	".svg":   "image/svg+xml",
	".txt":   "text/plain; charset=utf-8",
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".ttf":   "font/ttf",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".gif":   "image/gif",
	".ico":   "image/x-icon",
	".webp":  "image/webp",
}

func contentTypeFor(name string) string {
	ext := strings.ToLower(path.Ext(name))
	if value, ok := contentTypeOverrides[ext]; ok {
		return value
	}
	if value := mime.TypeByExtension(ext); value != "" {
		return value
	}
	return "application/octet-stream"
}

// isAPIPath reports whether a request belongs to the JSON API namespace.
func isAPIPath(p string) bool {
	return p == "/api" || strings.HasPrefix(p, "/api/")
}

// staticHandler serves the embedded front-end and implements the SPA fallback:
// non-/api requests without a file extension return index.html, while missing
// assets keep a real 404 so a broken build is visible instead of silently
// returning HTML with a 200.
func staticHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Express only registers the SPA fallback with app.get('*'), so a
		// non-GET request that matched no route falls through to finalhandler
		// and gets its HTML error page instead of the JSON envelope.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeExpressNotFound(w, r)
			return
		}
		if isAPIPath(r.URL.Path) {
			writeError(w, http.StatusNotFound, "Not Found")
			return
		}

		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			serveIndex(w, r, deps)
			return
		}

		if info, err := fs.Stat(deps.Web, name); err == nil && !info.IsDir() {
			serveEmbeddedFile(w, r, deps.Web, name, info)
			return
		}

		if path.Ext(name) != "" {
			writeText(w, http.StatusNotFound, "Not Found")
			return
		}
		serveIndex(w, r, deps)
	}
}

// writeExpressNotFound reproduces finalhandler's HTML 404 page, which Express
// serves whenever no route (including the GET-only SPA fallback) matches.
func writeExpressNotFound(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusNotFound)
	if r.Method == http.MethodHead {
		return
	}
	fmt.Fprintf(w, "<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n"+
		"<title>Error</title>\n</head>\n<body>\n<pre>Cannot %s %s</pre>\n</body>\n</html>\n",
		r.Method, html.EscapeString(r.URL.Path))
}

func serveIndex(w http.ResponseWriter, r *http.Request, deps Deps) {
	info, err := fs.Stat(deps.Web, "index.html")
	if err != nil {
		writeText(w, http.StatusNotFound, "Not Found")
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	serveEmbeddedFile(w, r, deps.Web, "index.html", info)
}

func serveEmbeddedFile(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string, info fs.FileInfo) {
	content, err := fs.ReadFile(fsys, name)
	if err != nil {
		writeText(w, http.StatusNotFound, "Not Found")
		return
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", contentTypeFor(name))
	}
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "public, max-age=0")
	}
	http.ServeContent(w, r, name, info.ModTime(), bytes.NewReader(content))
}

// uploadsHandler serves data/upload under /api/static with the hardened
// headers express.static sets in back/loaders/express.ts.
func uploadsHandler(cfgUploadPath string) http.Handler {
	fileServer := http.FileServer(noDirFS{http.Dir(cfgUploadPath)})
	stripped := http.StripPrefix("/api/static", fileServer)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeText(w, http.StatusNotFound, "Not Found")
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
		stripped.ServeHTTP(w, r)
	})
}

// noDirFS hides directory listings: a directory only resolves when it contains
// an index.html, matching express.static's default behaviour.
type noDirFS struct {
	root http.FileSystem
}

func (n noDirFS) Open(name string) (http.File, error) {
	file, err := n.root.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !info.IsDir() {
		return file, nil
	}

	indexName := path.Join(name, "index.html")
	index, err := n.root.Open(indexName)
	if err != nil {
		file.Close()
		return nil, os.ErrNotExist
	}
	file.Close()
	return index, nil
}
