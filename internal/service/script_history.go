package service

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/andybalholm/brotli"
)

// ScriptHistory ports back/shared/scriptHistory.ts. Version history is stored
// beside the database (data/script-history/<sha256(relative path)>.br), never
// inside data/scripts, so a history file can never be listed as a script and
// the archive survives a script being renamed or deleted.
//
// The archive is a Brotli-compressed JSON document. Compression parameters
// (quality 4, window 22) and the JSON key order match the Node writer byte for
// byte, which lets both implementations take over each other's files.
const (
	historyVersionsLimit = 20
	historyFileBytes     = 1 << 20
	historyUnpackedBytes = 8 << 20
	historyArchiveBytes  = 2 << 20
	historyTotalBytes    = 32 << 20
	historyFilesLimit    = 1000
)

// ScriptHistoryError carries the HTTP status the Node service throws.
type ScriptHistoryError struct {
	Status  int
	Message string
}

func (e *ScriptHistoryError) Error() string { return e.Message }

// HistoryUnavailableError is the 413 that tells the frontend the current file
// cannot be versioned but may still be saved with skipHistory.
type HistoryUnavailableError struct {
	ScriptHistoryError
	CurrentHash string
}

type historySource string

const (
	sourceBaseline historySource = "baseline"
	sourceSave     historySource = "save"
	sourceRestore  historySource = "restore"
	sourceExternal historySource = "external"
)

// historyVersion is one stored snapshot. Field order is part of the on-disk
// format: Node writes {id,createdAt,source,hash,content}.
type historyVersion struct {
	ID        string        `json:"id"`
	CreatedAt string        `json:"createdAt"`
	Source    historySource `json:"source"`
	Hash      string        `json:"hash"`
	Content   string        `json:"content"`
}

// historyHead labels the next snapshot. It is only used when its hash still
// matches the file, so a failed write cannot invent a version.
type historyHead struct {
	Hash      string        `json:"hash"`
	CreatedAt string        `json:"createdAt"`
	Source    historySource `json:"source"`
}

type historyArchive struct {
	Format   int              `json:"format"`
	Path     string           `json:"path"`
	Versions []historyVersion `json:"versions"`
	Head     *historyHead     `json:"head,omitempty"`
}

// VersionSummary is one entry of GET /api/scripts/history.
type VersionSummary struct {
	ID        string        `json:"id"`
	CreatedAt string        `json:"createdAt"`
	Source    historySource `json:"source"`
	Hash      string        `json:"hash"`
	Size      int           `json:"size"`
	Identical bool          `json:"identical"`
}

// HistoryList is the history index response.
type HistoryList struct {
	Versions    []VersionSummary `json:"versions"`
	CurrentHash string           `json:"currentHash"`
	Limit       int              `json:"limit"`
}

// HistoryDetail is one version plus the file it belongs to.
type HistoryDetail struct {
	Version     historyVersion `json:"version"`
	Current     string         `json:"current"`
	CurrentHash string         `json:"currentHash"`
}

// HistorySaveResult mirrors the Node return value. historyRecorded is present
// only when the write produced (or deliberately skipped) a snapshot, and
// cleanupPending only when a snapshot was written, so the two shapes stay
// distinguishable on the wire.
type HistorySaveResult struct {
	Content         string `json:"content"`
	Changed         bool   `json:"changed"`
	HistoryRecorded *bool  `json:"historyRecorded,omitempty"`
	CleanupPending  *bool  `json:"cleanupPending,omitempty"`
}

func boolPtr(value bool) *bool { return &value }

// ScriptHistory is the storage core. It is safe for concurrent use inside one
// process; the Node panel additionally guards the file with proper-lockfile,
// which is unnecessary here because the Go panel is the only writer.
type ScriptHistory struct {
	scriptRoot  string
	historyRoot string
	blacklist   []string

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewScriptHistory builds the service for one scripts root and one history
// root (data/script-history).
func NewScriptHistory(scriptRoot, historyRoot string, blacklist []string) *ScriptHistory {
	return &ScriptHistory{
		scriptRoot:  scriptRoot,
		historyRoot: historyRoot,
		blacklist:   blacklist,
		locks:       map[string]*sync.Mutex{},
	}
}

// Root exposes the history directory (used by tests and bootstrap logging).
func (h *ScriptHistory) Root() string { return h.historyRoot }

type historyTarget struct {
	file     string
	relative string
	archive  string
}

func hashContent(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func newUUID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand does not fail on supported platforms; keep a usable id.
		now := time.Now().UnixNano()
		for i := 0; i < 8; i++ {
			buf[i] = byte(now >> (8 * i))
		}
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}

func isoTimestamp(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// lockFor returns the mutex guarding one script file.
func (h *ScriptHistory) lockFor(file string) *sync.Mutex {
	key := file
	if runtime.GOOS == "windows" {
		key = strings.ToLower(file)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing, ok := h.locks[key]; ok {
		return existing
	}
	created := &sync.Mutex{}
	h.locks[key] = created
	return created
}

func (h *ScriptHistory) target(directory, filename string) (*historyTarget, error) {
	candidate := resolveFileAccess(h.scriptRoot, []string{directory, filename}, h.blacklist)
	if candidate == "" {
		return nil, &ScriptHistoryError{Status: 403, Message: "暂无权限"}
	}
	file, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, &ScriptHistoryError{Status: 404, Message: "脚本不存在"}
		}
		return nil, err
	}
	root, err := filepath.EvalSymlinks(h.scriptRoot)
	if err != nil {
		return nil, err
	}
	relative := pathRelative(root, file)
	key := hashContent(relative)
	return &historyTarget{
		file:     file,
		relative: relative,
		archive:  filepath.Join(h.historyRoot, key+".br"),
	}, nil
}

// readText reads a script that can be versioned: a regular file of at most
// 1 MiB that round-trips as UTF-8 without NUL bytes.
func (h *ScriptHistory) readText(file string) (string, error) {
	handle, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer handle.Close()

	info, err := handle.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", &ScriptHistoryError{Status: 400, Message: "请选择脚本文件"}
	}
	if info.Size() > historyFileBytes {
		return "", &ScriptHistoryError{Status: 413, Message: "历史版本仅支持不超过 1 MiB 的 UTF-8 文本脚本"}
	}
	// Bounded read even if another process grows the file after the stat.
	buf := make([]byte, historyFileBytes+1)
	size := 0
	for size < len(buf) {
		read, err := handle.Read(buf[size:])
		size += read
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", err
		}
		if read == 0 {
			break
		}
	}
	content := buf[:size]
	if size > historyFileBytes ||
		bytes.IndexByte(content, 0) >= 0 ||
		!utf8.Valid(content) {
		return "", &ScriptHistoryError{Status: 413, Message: "历史版本仅支持不超过 1 MiB 的 UTF-8 文本脚本"}
	}
	return string(content), nil
}

func invalidArchive() error {
	return &ScriptHistoryError{Status: 500, Message: "历史版本读取失败，请检查历史文件"}
}

// readArchive loads and validates one archive, returning an empty archive when
// the file does not exist yet.
func (h *ScriptHistory) readArchive(file, relative string) (*historyArchive, error) {
	empty := &historyArchive{Format: 1, Path: relative, Versions: []historyVersion{}}

	info, err := os.Lstat(file)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return empty, nil
		}
		return nil, invalidArchive()
	}
	if !info.Mode().IsRegular() || info.Size() > historyArchiveBytes {
		return nil, invalidArchive()
	}
	compressed, err := os.ReadFile(file)
	if err != nil {
		return nil, invalidArchive()
	}
	limited := &io.LimitedReader{R: brotli.NewReader(bytes.NewReader(compressed)), N: historyUnpackedBytes + 1}
	raw, err := io.ReadAll(limited)
	if err != nil || len(raw) > historyUnpackedBytes {
		return nil, invalidArchive()
	}

	var archive historyArchive
	if err := json.Unmarshal(raw, &archive); err != nil {
		return nil, invalidArchive()
	}
	if archive.Format != 1 || archive.Path != relative ||
		archive.Versions == nil || len(archive.Versions) > historyVersionsLimit {
		return nil, invalidArchive()
	}
	for _, version := range archive.Versions {
		switch version.Source {
		case sourceBaseline, sourceSave, sourceRestore, sourceExternal:
		default:
			return nil, invalidArchive()
		}
		if len(version.Content) > historyFileBytes || hashContent(version.Content) != version.Hash {
			return nil, invalidArchive()
		}
	}
	return &archive, nil
}

// List returns the versions of one script, newest first.
func (h *ScriptHistory) List(directory, filename string) (*HistoryList, error) {
	target, err := h.target(directory, filename)
	if err != nil {
		return nil, err
	}
	current, err := h.readText(target.file)
	if err != nil {
		return nil, err
	}
	archive, err := h.readArchive(target.archive, target.relative)
	if err != nil {
		return nil, err
	}
	currentHash := hashContent(current)
	versions := make([]VersionSummary, 0, len(archive.Versions))
	for i := len(archive.Versions) - 1; i >= 0; i-- {
		version := archive.Versions[i]
		versions = append(versions, VersionSummary{
			ID:        version.ID,
			CreatedAt: version.CreatedAt,
			Source:    version.Source,
			Hash:      version.Hash,
			Size:      len(version.Content),
			Identical: version.Hash == currentHash,
		})
	}
	return &HistoryList{Versions: versions, CurrentHash: currentHash, Limit: historyVersionsLimit}, nil
}

// Detail returns one stored version together with the current file.
func (h *ScriptHistory) Detail(directory, filename, id string) (*HistoryDetail, error) {
	target, err := h.target(directory, filename)
	if err != nil {
		return nil, err
	}
	current, err := h.readText(target.file)
	if err != nil {
		return nil, err
	}
	archive, err := h.readArchive(target.archive, target.relative)
	if err != nil {
		return nil, err
	}
	for _, version := range archive.Versions {
		if version.ID == id {
			return &HistoryDetail{
				Version:     version,
				Current:     current,
				CurrentHash: hashContent(current),
			}, nil
		}
	}
	return nil, &ScriptHistoryError{Status: 404, Message: "历史版本不存在或已清理"}
}

// SaveOptions carries the optimistic-lock fields of the save endpoints.
type SaveOptions struct {
	SkipHistory  bool
	ExpectedHash string
}

// Save writes content, snapshotting the previous revision first.
func (h *ScriptHistory) Save(directory, filename, content string, options SaveOptions) (*HistorySaveResult, error) {
	return h.mutate(directory, filename, &content, "", options)
}

// Restore writes a stored version back to disk.
func (h *ScriptHistory) Restore(directory, filename, id, expectedHash string) (*HistorySaveResult, error) {
	return h.mutate(directory, filename, nil, id, SaveOptions{ExpectedHash: expectedHash})
}

func (h *ScriptHistory) mutate(directory, filename string, content *string, restoreID string, options SaveOptions) (*HistorySaveResult, error) {
	target, err := h.target(directory, filename)
	if err != nil {
		return nil, err
	}

	lock := h.lockFor(target.file)
	lock.Lock()
	defer lock.Unlock()

	current, readErr := h.readText(target.file)
	if readErr == nil && content != nil &&
		(len(*content) > historyFileBytes || strings.ContainsRune(*content, 0)) {
		readErr = &ScriptHistoryError{Status: 413, Message: "历史版本仅支持不超过 1 MiB 的 UTF-8 文本脚本"}
	}
	if readErr != nil {
		historyErr, ok := readErr.(*ScriptHistoryError)
		// Only the oversized/non-UTF-8 case is recoverable: the file cannot be
		// versioned, but it can still be overwritten without history.
		if content == nil || !ok || historyErr.Status != 413 {
			return nil, readErr
		}
		currentHash, err := hashFile(target.file)
		if err != nil {
			return nil, err
		}
		if !options.SkipHistory {
			return nil, &HistoryUnavailableError{
				ScriptHistoryError: ScriptHistoryError{Status: 413, Message: "历史版本仅支持不超过 1 MiB 的 UTF-8 文本脚本"},
				CurrentHash:        currentHash,
			}
		}
		if options.ExpectedHash != currentHash {
			return nil, &ScriptHistoryError{Status: 409, Message: "文件已变化，请刷新历史版本后重试"}
		}
		if err := h.preparedWrite(target.file, *content); err != nil {
			return nil, err
		}
		return &HistorySaveResult{Content: *content, Changed: true, HistoryRecorded: boolPtr(false)}, nil
	}

	if (restoreID != "" || options.SkipHistory) && hashContent(current) != options.ExpectedHash {
		return nil, &ScriptHistoryError{Status: 409, Message: "文件已变化，请刷新历史版本后重试"}
	}
	if content != nil && *content == current {
		return &HistorySaveResult{Content: current, Changed: false}, nil
	}

	if err := os.MkdirAll(h.historyRoot, 0o700); err != nil {
		return nil, err
	}

	archive, err := h.readArchive(target.archive, target.relative)
	if err != nil {
		return nil, err
	}

	var restored *historyVersion
	if restoreID != "" {
		for i := range archive.Versions {
			if archive.Versions[i].ID == restoreID {
				restored = &archive.Versions[i]
				break
			}
		}
		if restored == nil {
			return nil, &ScriptHistoryError{Status: 404, Message: "历史版本不存在或已清理"}
		}
	}

	next := current
	source := sourceSave
	if content != nil {
		next = *content
	} else {
		next = restored.Content
		source = sourceRestore
	}
	if next == current {
		return &HistorySaveResult{Content: next, Changed: false}, nil
	}
	if len(next) > historyFileBytes || strings.ContainsRune(next, 0) {
		return nil, &ScriptHistoryError{Status: 413, Message: "历史版本仅支持不超过 1 MiB 的 UTF-8 文本脚本"}
	}

	currentHash := hashContent(current)
	if len(archive.Versions) == 0 || archive.Versions[len(archive.Versions)-1].Hash != currentHash {
		createdAt := isoTimestamp(time.Now())
		versionSource := sourceBaseline
		if archive.Head != nil && archive.Head.Hash == currentHash {
			createdAt = archive.Head.CreatedAt
			versionSource = archive.Head.Source
		} else if len(archive.Versions) > 0 {
			versionSource = sourceExternal
		}
		archive.Versions = append(archive.Versions, historyVersion{
			ID:        newUUID(),
			CreatedAt: createdAt,
			Source:    versionSource,
			Hash:      currentHash,
			Content:   current,
		})
	}
	// Only prior contents are stored. The head labels the next snapshot and is
	// used only when its hash still matches the file.
	archive.Head = &historyHead{
		Hash:      hashContent(next),
		CreatedAt: isoTimestamp(time.Now()),
		Source:    source,
	}

	packed, err := h.pack(archive)
	if err != nil {
		return nil, err
	}
	cleanupPending, err := h.store(target.archive, packed, target.file, next)
	if err != nil {
		return nil, err
	}
	return &HistorySaveResult{
		Content:         next,
		Changed:         true,
		HistoryRecorded: boolPtr(true),
		CleanupPending:  boolPtr(cleanupPending),
	}, nil
}

// hashFile streams a file through SHA-256 for files too large to version.
func hashFile(file string) (string, error) {
	handle, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer handle.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, handle); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// pack trims the archive to the version limit and compresses it, dropping the
// oldest versions until both the packed and unpacked sizes fit.
func (h *ScriptHistory) pack(archive *historyArchive) ([]byte, error) {
	if len(archive.Versions) > historyVersionsLimit {
		archive.Versions = archive.Versions[len(archive.Versions)-historyVersionsLimit:]
	}
	for {
		serialized, err := json.Marshal(archive)
		if err != nil {
			return nil, err
		}
		if len(serialized) <= historyUnpackedBytes {
			var buffer bytes.Buffer
			writer := brotli.NewWriterOptions(&buffer, brotli.WriterOptions{Quality: 4, LGWin: 22})
			if _, err := writer.Write(serialized); err != nil {
				return nil, err
			}
			if err := writer.Close(); err != nil {
				return nil, err
			}
			if buffer.Len() <= historyArchiveBytes {
				return buffer.Bytes(), nil
			}
		}
		if len(archive.Versions) <= 1 {
			return nil, &ScriptHistoryError{Status: 413, Message: "历史版本容量不足，无法安全保存"}
		}
		archive.Versions = archive.Versions[1:]
	}
}

// preparedWrite stages the new script next to the original and publishes it
// with a rename, so a failed write never truncates the file. The existing
// ownership and mode are preserved.
func (h *ScriptHistory) preparedWrite(file, content string) error {
	info, err := os.Stat(file)
	if err != nil {
		return err
	}
	temporary := filepath.Join(filepath.Dir(file), ".ql-save-"+newUUID()+".tmp")
	defer os.Remove(temporary)

	handle, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := handle.WriteString(content); err != nil {
		handle.Close()
		return err
	}
	if err := handle.Sync(); err != nil {
		handle.Close()
		return err
	}
	if err := handle.Close(); err != nil {
		return err
	}
	if err := os.Chmod(temporary, info.Mode().Perm()); err != nil {
		return err
	}
	return os.Rename(temporary, file)
}

// store publishes the archive and only then the script. On a failed commit the
// previous archive is restored, so history and content never diverge.
func (h *ScriptHistory) store(archiveFile string, packed []byte, scriptFile, content string) (bool, error) {
	entries, err := os.ReadDir(h.historyRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	type entry struct {
		file string
		size int64
		time int64
	}
	archivePattern := regexp.MustCompile(`^[a-f0-9]{64}\.br$`)
	candidates := make([]entry, 0, len(entries))
	for _, item := range entries {
		if item.IsDir() || !archivePattern.MatchString(item.Name()) {
			continue
		}
		full := filepath.Join(h.historyRoot, item.Name())
		if equalPath(full, archiveFile) {
			continue
		}
		info, err := os.Lstat(full)
		if err != nil {
			continue
		}
		candidates = append(candidates, entry{file: full, size: info.Size(), time: info.ModTime().UnixMilli()})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].time < candidates[j].time })

	total := int64(len(packed))
	for _, item := range candidates {
		total += item.size
	}
	victims := make([]string, 0, 4)
	for len(candidates) > 0 && (total > historyTotalBytes || len(candidates) >= historyFilesLimit) {
		oldest := candidates[0]
		candidates = candidates[1:]
		victims = append(victims, oldest.file)
		total -= oldest.size
	}
	if total > historyTotalBytes {
		return false, &ScriptHistoryError{Status: 413, Message: "历史版本容量不足，无法安全保存"}
	}

	previous, err := os.ReadFile(archiveFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}

	pending := filepath.Join(h.historyRoot, ".pending.tmp")
	publish := func(payload []byte) error {
		if err := os.Remove(pending); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		handle, err := os.OpenFile(pending, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		if _, err := handle.Write(payload); err != nil {
			handle.Close()
			return err
		}
		if err := handle.Sync(); err != nil {
			handle.Close()
			return err
		}
		if err := handle.Close(); err != nil {
			return err
		}
		return os.Rename(pending, archiveFile)
	}
	defer os.Remove(pending)

	if err := publish(packed); err != nil {
		return false, err
	}
	if err := h.preparedWrite(scriptFile, content); err != nil {
		// The script is untouched; put the exact history back.
		if len(previous) > 0 {
			_ = publish(previous)
		} else {
			_ = os.Remove(archiveFile)
		}
		return false, err
	}

	// Quota eviction is post-commit maintenance: a failure is reported as a
	// warning, never as a failed save.
	cleanupPending := false
	for _, victim := range victims {
		if err := os.Remove(victim); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupPending = true
		}
	}
	return cleanupPending, nil
}
