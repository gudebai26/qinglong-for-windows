package httpapi

import (
	"bufio"
	"compress/gzip"
	"mime"
	"net"
	"net/http"
	"strings"
)

// compress mirrors the unconditional `app.use(compression())` the Node panel
// installs in httpApplication.ts: JSON, text and front-end assets are gzipped
// when the client asks for it. The websocket endpoint and every streaming or
// binary response are deliberately left alone so the compression layer can
// never buffer a live log, break a websocket upgrade or corrupt a download.
func compress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead || !acceptsGzip(r) || skipCompressionPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		writer := &compressionWriter{ResponseWriter: w, request: r}
		next.ServeHTTP(writer, r)
		writer.close()
	})
}

// acceptsGzip reports whether the client negotiated gzip and did not exclude it.
func acceptsGzip(r *http.Request) bool {
	header := r.Header.Get("Accept-Encoding")
	if header == "" {
		return false
	}
	best := ""
	for _, part := range strings.Split(header, ",") {
		coding := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		if coding == "gzip" || coding == "*" {
			best = coding
		}
	}
	return best == "gzip" || best == "*"
}

// skipCompressionPath excludes the endpoints whose transport is not a plain
// buffered response: the SockJS socket (already intercepted before this layer)
// and the upload directory, which serves arbitrary user files and may answer
// byte-range requests that gzip would corrupt.
func skipCompressionPath(path string) bool {
	return strings.HasPrefix(path, "/api/ws") || strings.HasPrefix(path, "/api/static/")
}

// compressibleContentType decides from the declared media type, mirroring the
// `compressible` lookup the express middleware does.
func compressibleContentType(value string) bool {
	if value == "" || strings.HasPrefix(value, "text/") {
		return value != "" // text/* is compressible; empty falls through to sniffing
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	switch {
	case strings.HasSuffix(mediaType, "+json"):
		return true
	case mediaType == "application/json":
		return true
	case mediaType == "application/javascript", mediaType == "application/xml",
		mediaType == "application/wasm", mediaType == "image/svg+xml":
		return true
	case strings.HasPrefix(mediaType, "image/"), strings.HasPrefix(mediaType, "audio/"),
		strings.HasPrefix(mediaType, "video/"), mediaType == "application/octet-stream",
		mediaType == "text/event-stream":
		return false
	default:
		// Unknown binary types stay uncompressed; anything Go would sniff as
		// text is covered by the text/ prefix above.
		return false
	}
}

// compressionWriter lazily turns into a gzip writer once the handler reveals a
// compressible content type. Until then it passes bytes through untouched, so a
// handler that never sets a compressible type (or a streaming/binary one) is
// indistinguishable from the uncompressed path.
type compressionWriter struct {
	http.ResponseWriter
	request     *http.Request
	gz          *gzip.Writer
	status      int
	wroteHeader bool
	compressed  bool
}

func (c *compressionWriter) decide() bool {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	if c.status < 200 || c.status == http.StatusNoContent || c.status == http.StatusNotModified {
		return false
	}
	if c.Header().Get("Content-Encoding") != "" || c.Header().Get("Content-Range") != "" {
		return false
	}
	if c.request.Header.Get("Range") != "" {
		return false
	}
	return compressibleContentType(c.Header().Get("Content-Type"))
}

func (c *compressionWriter) WriteHeader(status int) {
	if c.wroteHeader {
		return
	}
	c.wroteHeader = true
	c.status = status
	if !c.decide() {
		c.ResponseWriter.WriteHeader(status)
		return
	}
	c.compressed = true
	c.Header().Set("Content-Encoding", "gzip")
	c.Header().Add("Vary", "Accept-Encoding")
	c.Header().Del("Content-Length")
	c.gz, _ = gzip.NewWriterLevel(c.ResponseWriter, gzip.DefaultCompression)
	c.ResponseWriter.WriteHeader(status)
}

func (c *compressionWriter) Write(p []byte) (int, error) {
	if !c.wroteHeader {
		c.WriteHeader(http.StatusOK)
	}
	if c.gz != nil {
		return c.gz.Write(p)
	}
	return c.ResponseWriter.Write(p)
}

// Flush forwards a streaming flush. When compressing, the gzip stream is
// flushed first so a chunked/SSE-style response still reaches the client in
// real time instead of waiting for the final close.
func (c *compressionWriter) Flush() {
	if c.gz != nil {
		_ = c.gz.Flush()
	}
	if flusher, ok := c.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// close finishes the gzip stream after the handler returns. It must be called
// exactly once by the middleware.
func (c *compressionWriter) close() {
	if c.gz != nil {
		_ = c.gz.Close()
		c.gz = nil
	}
}

// Unwrap lets http.ResponseController and the panic recovery reach the
// underlying writer.
func (c *compressionWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// Hijack forwards a connection upgrade to the underlying writer. /api/ws is
// excluded from compression, but the middleware must not hide the hijacker in
// case a future handler needs it.
func (c *compressionWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hijacker, ok := c.ResponseWriter.(http.Hijacker); ok {
		return hijacker.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}
