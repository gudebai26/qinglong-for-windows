// Package preload ships the Node and Python startup hooks the panel injects
// into user tasks. The scripts are the same files the Node panel keeps in
// shell/preload: they provide the ql API, $done, notifications and the
// per-task before hook. They are embedded so the Go panel stays a single
// executable, and written to <root>/shell/preload on first boot so the
// paths handed to NODE_OPTIONS / PYTHONPATH look exactly like the Node ones.
package preload

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed all:assets
var assets embed.FS

//go:embed protos/api.proto
var apiProto []byte

// Asset returns one embedded preload file. The subscription sync uses it to
// seed data/scripts/sendNotify.js and notify.py, which every repository copy
// carries so `require('./sendNotify')` works inside a subscription script.
func Asset(name string) ([]byte, error) {
	return fs.ReadFile(assets, "assets/"+name)
}

// ConfigSample returns the content of data/config/config.sh on a fresh install.
// Node copies sample/config.sample.sh there in initFile.ts; the embedded copy
// keeps the single-file build self-contained.
func ConfigSample() ([]byte, error) {
	return fs.ReadFile(assets, "assets/config.sample.sh")
}

// Files lists the embedded preload scripts.
var Files = []string{
	"sitecustomize.js",
	"sitecustomize.py",
	"client.js",
	"client.py",
	"esm-loader.mjs",
	"__ql_notify__.js",
	"__ql_notify__.py",
	"notificationAudit.js",
	"env.js",
	"env.py",
}

// generatedFiles are written by the environment module (set_envs), so an
// upgrade must never reset them to the empty embedded copy.
var generatedFiles = map[string]struct{}{"env.js": {}, "env.py": {}}

// preloadRevision identifies the shipped hook set. Materialize rewrites the
// hooks when the marker on disk is older, so a fixed hook actually reaches an
// existing data/ directory; Node ships shell/preload inside the package and
// overwrites it on upgrade the same way. Bump it whenever a file in Files
// changes.
const preloadRevision = "3"

// revisionFile records the revision last written into a preload directory.
const revisionFile = ".preload-revision"

// Materialize writes the embedded scripts into dir. A hook that already exists
// is left untouched unless the shipped revision moved; env.js/env.py are never
// rewritten because the environment module owns their content.
func Materialize(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create preload directory: %w", err)
	}
	stale := readRevision(dir) != preloadRevision

	for _, name := range Files {
		target := filepath.Join(dir, name)
		_, generated := generatedFiles[name]
		if _, err := os.Stat(target); err == nil {
			if !stale || generated {
				continue
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect %s: %w", target, err)
		}
		content, err := fs.ReadFile(assets, "assets/"+name)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", name, err)
		}
		if err := writeAtomic(target, content); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(dir, revisionFile), []byte(preloadRevision), 0o644)
}

func readRevision(dir string) string {
	content, err := os.ReadFile(filepath.Join(dir, revisionFile))
	if err != nil {
		return ""
	}
	return string(content)
}

// writeAtomic replaces target in one step: tasks started during boot keep
// loading the old file until they finish, instead of reading a half-written
// hook.
func writeAtomic(target string, content []byte) error {
	dir := filepath.Dir(target)
	temp, err := os.CreateTemp(dir, ".tmp-preload-*")
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", target, err)
	}
	name := temp.Name()
	defer os.Remove(name)
	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return fmt.Errorf("write %s: %w", target, err)
	}
	if err := temp.Chmod(0o644); err != nil {
		temp.Close()
		return fmt.Errorf("chmod %s: %w", target, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", target, err)
	}
	if err := os.Rename(name, target); err != nil {
		return fmt.Errorf("replace %s: %w", target, err)
	}
	return nil
}

// JSEntry is the file passed to `node --require`.
func JSEntry(dir string) string { return filepath.Join(dir, "sitecustomize.js") }

// PYEntry is the directory added to PYTHONPATH; Python imports sitecustomize
// from it automatically at interpreter start-up.
func PYEntry(dir string) string { return dir }

// ProtoPath is where the ql API schema must live. client.js loads it at
// require time and calls process.exit(1) when it is missing, which would kill
// every JavaScript task before it starts, so the Go panel ships the same schema
// the Node panel keeps in back/protos.
func ProtoPath(root string) string {
	return filepath.Join(root, "back", "protos", "api.proto")
}

// MaterializeProto writes back/protos/api.proto when it is absent.
func MaterializeProto(root string) error {
	target := ProtoPath(root)
	if _, err := os.Stat(target); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect %s: %w", target, err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create proto directory: %w", err)
	}
	if err := os.WriteFile(target, apiProto, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}
	return nil
}
