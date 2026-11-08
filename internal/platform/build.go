// Package platform holds OS specific helpers (process trees, paths, locks) and
// runtime identity for the panel. M1 only needs the build information used by
// /api/health; the process helpers arrive with the task runner.
package platform

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// BuildInfo mirrors the shape of runtimeBuild in back/shared/runtimeBuild.ts.
// Optional fields are omitted when unavailable, just like JSON.stringify drops
// undefined values in the Node implementation.
type BuildInfo struct {
	InstanceID   string `json:"instanceId"`
	ParentPid    int    `json:"parentPid"`
	Version      string `json:"version,omitempty"`
	SourceCommit string `json:"sourceCommit,omitempty"`
	// BackendSha256 is the Node build's hash of static/build/app.js. The Go
	// panel has no bundle to hash, so it reports the hash of the executable
	// instead: same purpose (identify the running build in support requests),
	// different artifact.
	BackendSha256 string `json:"backendSha256,omitempty"`
}

var versionPattern = regexp.MustCompile(`(?m)^\x{FEFF}?version:\s*["']?(\d+\.\d+\.\d+(?:-[\w.-]+)?(?:\+[\w.-]+)?)`)

// embeddedVersion is version.yaml compiled into the executable. A single-file
// distribution ships no manifest next to the binary, so /api/health would
// otherwise report an empty version; the file on disk still wins when present.
// It is assigned once during boot, before any request is served.
var embeddedVersion []byte

// SetEmbeddedVersion installs the compiled-in version manifest fallback.
func SetEmbeddedVersion(content []byte) { embeddedVersion = content }

// versionContent returns the manifest text, preferring the file on disk.
func versionContent(root string) []byte {
	if content, err := os.ReadFile(filepath.Join(root, "version.yaml")); err == nil {
		return content
	}
	return embeddedVersion
}

// DetectBuild captures the runtime identity once, before serving requests.
func DetectBuild(root string) BuildInfo {
	return BuildInfo{
		InstanceID:    newInstanceID(),
		ParentPid:     os.Getppid(),
		Version:       ReadVersion(root),
		BackendSha256: selfHash(),
	}
}

// selfHash returns the SHA-256 of the running executable, the Go equivalent of
// the backend bundle hash runtimeBuild.ts reports. It is computed once at boot.
func selfHash() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	file, err := os.Open(exe)
	if err != nil {
		return ""
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return ""
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// ReadVersion extracts the version from version.yaml without a YAML dependency.
func ReadVersion(root string) string {
	content := versionContent(root)
	if len(content) == 0 {
		return ""
	}
	match := versionPattern.FindSubmatch(content)
	if len(match) < 2 {
		return ""
	}
	return strings.TrimSpace(string(match[1]))
}

// VersionDoc is the subset of version.yaml the panel API exposes
// (parseVersion in back/config/util.ts).
type VersionDoc struct {
	Version       string
	ChangeLog     string
	ChangeLogLink string
	PublishTime   string
}

// ReadVersionDoc parses version.yaml without a YAML dependency: the file is a
// flat mapping plus one literal block scalar.
func ReadVersionDoc(root string) VersionDoc {
	content := versionContent(root)
	if len(content) == 0 {
		return VersionDoc{}
	}
	return ParseVersionDoc(string(content))
}

// ParseVersionDoc parses the version document text.
func ParseVersionDoc(content string) VersionDoc {
	var doc VersionDoc
	lines := strings.Split(strings.TrimPrefix(content, "\uFEFF"), "\n")
	for index := 0; index < len(lines); index++ {
		line := strings.TrimRight(lines[index], "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, raw, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		raw = strings.TrimSpace(raw)
		if raw == "|" || raw == ">" || raw == "|-" || raw == ">-" {
			blockStyle := raw
			// Literal block: every following line indented deeper belongs to it.
			var block []string
			for index+1 < len(lines) {
				next := strings.TrimRight(lines[index+1], "\r")
				if strings.TrimSpace(next) == "" {
					block = append(block, "")
					index++
					continue
				}
				if !strings.HasPrefix(next, " ") && !strings.HasPrefix(next, "\t") {
					break
				}
				block = append(block, strings.TrimSpace(next))
				index++
			}
			// A `|` block keeps one trailing newline; `|-` strips it.
			raw = strings.TrimRight(strings.Join(block, "\n"), "\n")
			if blockStyle == "|" && raw != "" {
				raw += "\n"
			}
		} else {
			raw = strings.Trim(raw, `"'`)
		}
		switch key {
		case "version":
			doc.Version = raw
		case "changeLog":
			doc.ChangeLog = raw
		case "changeLogLink":
			doc.ChangeLogLink = raw
		case "publishTime":
			doc.PublishTime = raw
		}
	}
	return doc
}

// newInstanceID returns a random UUID v4, matching randomUUID() in Node.
func newInstanceID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80

	var out [36]byte
	hex.Encode(out[0:8], buf[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], buf[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], buf[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], buf[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], buf[10:16])
	return string(out[:])
}
