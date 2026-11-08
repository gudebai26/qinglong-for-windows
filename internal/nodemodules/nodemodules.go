// Package nodemodules materializes the vendored Node runtime that the ql API
// (shell/preload/client.js) requires: @grpc/grpc-js, @grpc/proto-loader,
// protobufjs, undici, nodemailer and their transitive dependencies.
//
// The files are embedded in the executable so a single-file download still
// exposes QLAPI to JavaScript tasks. On boot they are copied to
// <QL_DIR>/node_modules - the exact path a Node deployment uses, which keeps
// NODE_PATH, the preload probe and user scripts unchanged.
//
// The copy never overwrites: a Node installation upgraded in place already has
// a ~1GB node_modules tree, and replacing it with the vendored subset would
// break every task that imports a package the panel does not ship. Only missing
// files are written, one atomic rename each, and a revision marker records
// which embedded set the directory was completed from.
package nodemodules

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"path/filepath"
	"strings"
)

// Revision identifies the embedded file set. Bump it whenever a file under
// node_modules changes, so the marker on disk no longer matches and the next
// boot re-runs the (still non-destructive) fill pass.
const Revision = "1"

// MarkerName records the revision last materialized in a root directory.
const MarkerName = ".node-modules-revision"

// DirName is the directory next to the executable that holds the runtime.
const DirName = "node_modules"

// Result reports what a call to Ensure did.
type Result struct {
	// Extracted counts files newly written into the target directory.
	Extracted int
	// Skipped counts embedded files already present on disk (never touched).
	Skipped int
	// Missing counts files that could not be written; the call still returns
	// an error in that case.
	Missing int
	// Revision is the marker value found on disk before the call ("" when the
	// marker was absent).
	PreviousRevision string
	// UpToDate is true when the marker already matched and the top-level tree
	// was complete, so the walk was skipped entirely.
	UpToDate bool
}

// Changed reports whether anything was written.
func (r Result) Changed() bool { return r.Extracted > 0 }

// Ensure copies every embedded file under root/node_modules that does not
// already exist. It is safe to call on every boot and safe to interrupt: each
// file is written to a temporary name in the destination directory and renamed,
// so a half-written file never appears under its final name.
//
// A failure to read or write one file aborts the pass (the remaining files are
// reported as missing) but leaves the marker untouched, so the next boot
// retries. Callers treat any error as a warning: the panel still starts, only
// the ql API degrades.
func Ensure(fsys fs.FS, root string) (Result, error) {
	var result Result
	if fsys == nil {
		return result, errors.New("nodemodules: no embedded file system")
	}

	target := filepath.Join(root, DirName)
	marker := filepath.Join(root, MarkerName)
	result.PreviousRevision = readMarker(marker)

	total, err := Count(fsys)
	if err != nil {
		return result, err
	}

	// Fast path: a complete install from the current revision needs no walk.
	if result.PreviousRevision == Revision && topLevelPresent(fsys, target) {
		result.UpToDate = true
		result.Skipped = total
		return result, nil
	}

	if err := os.MkdirAll(target, 0o755); err != nil {
		return result, fmt.Errorf("create %s: %w", target, err)
	}

	walkErr := fs.WalkDir(fsys, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		// Embedded paths are slash-separated; join them onto the OS path.
		dest := filepath.Join(target, filepath.FromSlash(name))
		if entry.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}

		// Anything on disk wins - including a size-0 placeholder a user created
		// on purpose. Overwriting is what breaks a 1GB Node install.
		if _, err := os.Lstat(dest); err == nil {
			result.Skipped++
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat %s: %w", dest, err)
		}

		content, err := fs.ReadFile(fsys, name)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", name, err)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(dest), err)
		}
		if err := writeFileAtomic(dest, content); err != nil {
			return fmt.Errorf("write %s: %w", dest, err)
		}
		result.Extracted++
		return nil
	})
	if walkErr != nil {
		result.Missing = total - result.Extracted - result.Skipped
		return result, walkErr
	}

	// The marker is written last: an interrupted pass therefore looks stale on
	// the next boot and is retried (still without overwriting anything).
	if err := writeFileAtomic(marker, []byte(Revision+"\n")); err != nil {
		return result, fmt.Errorf("write revision marker: %w", err)
	}
	return result, nil
}

// Count returns the number of regular files in the embedded set.
func Count(fsys fs.FS) (int, error) {
	total := 0
	err := fs.WalkDir(fsys, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			total++
		}
		return nil
	})
	return total, err
}

// topLevelPresent reports whether every top-level entry of the embedded set
// exists in dir. It is the cheap completeness check for the up-to-date path:
// 24 stat calls instead of a full walk of 1000+ files.
func topLevelPresent(fsys fs.FS, dir string) bool {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if _, err := os.Lstat(filepath.Join(dir, entry.Name())); err != nil {
			return false
		}
	}
	return true
}

func readMarker(path string) string {
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(content))
}

// writeFileAtomic writes content to a temporary file beside dest and renames it
// into place, so readers never observe a partially written file.
func writeFileAtomic(dest string, content []byte) error {
	dir := filepath.Dir(dest)
	// filepath.Base, not path.Base: on Windows the destination uses backslashes
	// and a slash-only split leaves the whole path in the pattern.
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(dest)+".ql-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}

	if _, err := tmp.Write(content); err != nil {
		cleanup()
		return err
	}
	// Flush before the rename: on Windows a crash between rename and flush can
	// leave a size-0 file, and the non-overwrite rule would keep it forever.
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, dest); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// EnsureCopy is a small helper for tests and callers that hold a directory
// rather than an fs.FS.
func EnsureCopy(dir, root string) (Result, error) {
	return Ensure(os.DirFS(dir), root)
}
