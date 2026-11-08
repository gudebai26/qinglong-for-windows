package service

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"qinglong-go/internal/config"
)

// scriptListBlacklist mirrors the literal list in back/api/script.ts: build
// artefacts of a script repository are hidden from the tree.
var scriptListBlacklist = []string{
	"node_modules", ".git", ".pnpm", "pnpm-lock.yaml", "yarn.lock", "package-lock.json",
}

// BusinessError is a panel-level failure that Node answers with HTTP 200 and a
// non-200 body code (`res.send({code:403,message})`).
type BusinessError struct {
	Code    int
	Message string
}

func (e *BusinessError) Error() string { return e.Message }

func forbidden() error {
	return &BusinessError{Code: 403, Message: "暂无权限"}
}

// ScriptService owns data/scripts: the tree, the file content, the version
// history and the ad-hoc run/stop endpoints.
type ScriptService struct {
	cfg     *config.Config
	crons   *CronService
	history *ScriptHistory
	logger  *slog.Logger
}

// NewScriptService wires the script endpoints to the cron service (for the
// run/stop endpoints) and to the history store.
func NewScriptService(cfg *config.Config, crons *CronService, logger *slog.Logger) *ScriptService {
	if logger == nil {
		logger = slog.Default()
	}
	return &ScriptService{
		cfg:     cfg,
		crons:   crons,
		history: NewScriptHistory(cfg.ScriptPath, cfg.ScriptHistoryPath, nil),
		logger:  logger,
	}
}

// History exposes the version store for the handlers.
func (s *ScriptService) History() *ScriptHistory { return s.history }

// writePaths mirrors config.writePathList: scripts may be written to the
// scripts directory and (with the blacklist applied) to data/config.
func (s *ScriptService) writePaths() []string {
	return []string{s.cfg.ConfigPath, s.cfg.ScriptPath}
}

// isPathAllowed ports isPathAllowed(): the target must resolve inside one of
// the writable roots, with panel secrets excluded under data/config.
func (s *ScriptService) isPathAllowed(target string) bool {
	resolved := resolvePath(target)
	for _, writable := range s.writePaths() {
		blacklist := []string(nil)
		if equalPath(resolvePath(writable), resolvePath(s.cfg.ConfigPath)) {
			blacklist = config.BlackFileList
		}
		if resolveFileAccess(writable, []string{resolved}, blacklist) != "" {
			return true
		}
	}
	return false
}

// existingWritableRealPath resolves an existing file and re-checks the real
// path, so a symlink cannot smuggle a write outside the writable roots.
func (s *ScriptService) existingWritableRealPath(target string) (string, error) {
	if !s.isPathAllowed(target) {
		return "", forbidden()
	}
	resolved := resolvePath(target)
	for _, writable := range s.writePaths() {
		root := resolvePath(writable)
		if !isStrictlyWithinPath(root, resolved) {
			continue
		}
		realPath, err := filepath.EvalSymlinks(resolved)
		if err != nil {
			return "", err
		}
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			return "", err
		}
		blacklist := []string(nil)
		if equalPath(root, resolvePath(s.cfg.ConfigPath)) {
			blacklist = config.BlackFileList
		}
		if !isStrictlyWithinPath(realRoot, realPath) ||
			resolveFileAccess(realRoot, []string{realPath}, blacklist) == "" {
			return "", forbidden()
		}
		return realPath, nil
	}
	return "", forbidden()
}

// CheckFilePath ports ScriptService.checkFilePath: "" means "not allowed".
func (s *ScriptService) CheckFilePath(directory, filename string) string {
	return resolveFileAccess(s.cfg.ScriptPath, []string{directory, filename}, nil)
}

// GetFile returns the script content, or "" for a path outside data/scripts.
// A missing file also yields "" - the panel shows an empty editor.
func (s *ScriptService) GetFile(directory, filename string) (string, error) {
	finalPath := s.CheckFilePath(directory, filename)
	if finalPath == "" {
		return "", nil
	}
	content, err := os.ReadFile(finalPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return string(content), nil
}

// ScriptEntry is one node of a flat listing (GET /api/scripts?path=...).
// Field order matches the object literal of readDir().
type ScriptEntry struct {
	Title      string `json:"title"`
	Type       string `json:"type"`
	Key        string `json:"key"`
	Parent     string `json:"parent"`
	Size       *int64 `json:"size,omitempty"`
	CreateTime int64  `json:"createTime"`
}

// ScriptDirEntry is a directory node of a flat listing; it carries an empty
// children array, exactly like readDir().
type ScriptDirEntry struct {
	Title      string `json:"title"`
	Type       string `json:"type"`
	Key        string `json:"key"`
	Parent     string `json:"parent"`
	CreateTime int64  `json:"createTime"`
	Children   []any  `json:"children"`
}

// ScriptTreeNode is a directory node of the recursive listing. The key order
// follows readDirsWithRunner's directory branch (title, key, type, ...).
type ScriptTreeNode struct {
	Title      string `json:"title"`
	Key        string `json:"key"`
	Type       string `json:"type"`
	Parent     string `json:"parent"`
	CreateTime int64  `json:"createTime"`
	Children   []any  `json:"children"`
}

// List returns the script tree. An empty directory means "recursive from the
// scripts root", which is what the frontend requests.
func (s *ScriptService) List(directory string) ([]any, error) {
	if directory != "" {
		if resolveFileAccess(s.cfg.ScriptPath, []string{directory}, nil) == "" {
			return nil, forbidden()
		}
		return s.readDir(directory)
	}
	return s.readDirs(s.cfg.ScriptPath, s.cfg.ScriptPath)
}

func isHiddenScriptEntry(name string, isLink bool) bool {
	if isLink {
		return true
	}
	for _, blocked := range scriptListBlacklist {
		if name == blocked {
			return true
		}
	}
	return false
}

// readDir lists one directory without recursion. The entries keep the
// directory order of the file system, like fs.readdir.
func (s *ScriptService) readDir(directory string) ([]any, error) {
	base := resolvePath(s.cfg.ScriptPath)
	absolute := resolvePath(base, directory)
	if !isWithinPath(base, absolute) {
		return nil, nil
	}
	relative := pathRelative(base, absolute)

	entries, err := os.ReadDir(absolute)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	result := make([]any, 0, len(entries))
	for _, entry := range entries {
		full := filepath.Join(absolute, entry.Name())
		info, err := os.Lstat(full)
		if err != nil {
			continue
		}
		if isHiddenScriptEntry(entry.Name(), info.Mode()&os.ModeSymlink != 0) {
			continue
		}
		key := joinDisplayPath(relative, entry.Name())
		if info.IsDir() {
			result = append(result, ScriptDirEntry{
				Title:      entry.Name(),
				Type:       "directory",
				Key:        key,
				Parent:     relative,
				CreateTime: fileBirthTime(info),
				Children:   []any{},
			})
			continue
		}
		size := info.Size()
		result = append(result, ScriptEntry{
			Title:      entry.Name(),
			Type:       "file",
			Key:        key,
			Parent:     relative,
			Size:       &size,
			CreateTime: fileBirthTime(info),
		})
	}
	return result, nil
}

// readDirs walks the tree recursively, skipping symlinks and blacklisted
// names, and sorts directories first then by title.
func (s *ScriptService) readDirs(dir, baseDir string) ([]any, error) {
	relative := pathRelative(baseDir, dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	type sortable struct {
		node  any
		title string
		isDir bool
	}
	items := make([]sortable, 0, len(entries))
	for _, entry := range entries {
		full := filepath.Join(dir, entry.Name())
		info, err := os.Lstat(full)
		if err != nil {
			continue
		}
		if isHiddenScriptEntry(entry.Name(), info.Mode()&os.ModeSymlink != 0) {
			continue
		}
		key := joinDisplayPath(relative, entry.Name())
		if info.IsDir() {
			children, err := s.readDirs(full, baseDir)
			if err != nil {
				return nil, err
			}
			items = append(items, sortable{
				node: ScriptTreeNode{
					Title:      entry.Name(),
					Key:        key,
					Type:       "directory",
					Parent:     relative,
					CreateTime: fileBirthTime(info),
					Children:   children,
				},
				title: entry.Name(),
				isDir: true,
			})
			continue
		}
		size := info.Size()
		items = append(items, sortable{
			node: ScriptEntry{
				Title:      entry.Name(),
				Type:       "file",
				Key:        key,
				Parent:     relative,
				Size:       &size,
				CreateTime: fileBirthTime(info),
			},
			title: entry.Name(),
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].isDir != items[j].isDir {
			return items[i].isDir
		}
		return collateCompare(items[i].title, items[j].title) < 0
	})
	result := make([]any, 0, len(items))
	for _, item := range items {
		result = append(result, item.node)
	}
	return result, nil
}

// joinDisplayPath mirrors path.join for the tree keys: the root listing has no
// parent, so keys are bare names.
func joinDisplayPath(relative, name string) string {
	if relative == "" {
		return name
	}
	return filepath.Join(relative, name)
}

// ducetPunctuation is the DUCET primary order of ASCII punctuation, which is
// what ICU's localeCompare uses. The panel sorts the tree with
// title.localeCompare(title), so '_' sorts before '-' and both before digits.
var ducetPunctuation = map[rune]int{
	' ': 0, '_': 1, '-': 2, ',': 3, ';': 4, ':': 5, '!': 6, '?': 7, '.': 8,
	'\'': 9, '"': 10, '(': 11, ')': 12, '[': 13, ']': 14, '{': 15, '}': 16,
	'@': 17, '*': 18, '/': 19, '\\': 20, '&': 21, '#': 22, '%': 23, '`': 24,
	'^': 25, '+': 26, '<': 27, '=': 28, '>': 29, '|': 30, '~': 31, '$': 32,
}

// collateCompare approximates String.prototype.localeCompare for file names.
//
// ICU compares one level at a time: all primary weights first (punctuation,
// then digits, then letters), and only when those are equal the tertiary
// weights decide (lowercase before uppercase). That is why "A.js" sorts before
// "a01.js" even though "a.js" sorts before "A.js".
//
// Non-ASCII text falls back to code points, the only part where Node's ICU
// pinyin collation cannot be reproduced.
func collateCompare(a, b string) int {
	left := []rune(a)
	right := []rune(b)
	if result := compareCollationLevel(left, right, false); result != 0 {
		return result
	}
	return compareCollationLevel(left, right, true)
}

func compareCollationLevel(left, right []rune, tertiary bool) int {
	for i := 0; i < len(left) && i < len(right); i++ {
		lc, lw, lt := collationWeight(left[i])
		rc, rw, rt := collationWeight(right[i])
		if tertiary {
			if lt != rt {
				return compareInt(lt, rt)
			}
			continue
		}
		if lc != rc {
			return compareInt(lc, rc)
		}
		if lw != rw {
			return compareInt(lw, rw)
		}
	}
	return compareInt(len(left), len(right))
}

func collationWeight(r rune) (class, weight, tertiary int) {
	switch {
	case r <= 0x20:
		return 0, int(r), 0
	case r < 0x7f:
		if rank, ok := ducetPunctuation[r]; ok {
			return 1, rank, 0
		}
		if unicode.IsPunct(r) || unicode.IsSymbol(r) {
			return 1, 0x1000 + int(r), 0
		}
		if r >= '0' && r <= '9' {
			return 2, int(r - '0'), 0
		}
	}
	if unicode.IsPunct(r) || unicode.IsSymbol(r) {
		return 1, 0x1000 + int(r), 0
	}
	if unicode.IsDigit(r) {
		return 2, int(r), 0
	}
	tertiary = 0
	if unicode.IsUpper(r) {
		tertiary = 1
	}
	return 3, int(unicode.ToLower(r)), tertiary
}

func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// Delete removes a file or a directory tree. Node's rmPath swallows errors, so
// the endpoint always answers 200.
func (s *ScriptService) Delete(directory, filename string) error {
	filePath := s.CheckFilePath(directory, filename)
	if filePath == "" {
		return forbidden()
	}
	if err := os.RemoveAll(filePath); err != nil {
		s.logger.Warn("[panel] remove script failed", "path", filePath, "error", err)
	}
	return nil
}

// DownloadPath resolves the file for POST /api/scripts/download.
func (s *ScriptService) DownloadPath(directory, filename string) (string, error) {
	filePath := s.CheckFilePath(directory, filename)
	if filePath == "" {
		return "", forbidden()
	}
	return filePath, nil
}

// Rename moves a script inside its directory.
func (s *ScriptService) Rename(directory, filename, newFilename string) error {
	filePath := filepath.Join(s.cfg.ScriptPath, directory, filename)
	newPath := filepath.Join(s.cfg.ScriptPath, directory, newFilename)
	if !s.isPathAllowed(filePath) || !s.isPathAllowed(newPath) {
		return forbidden()
	}
	if err := os.Rename(filePath, newPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Node surfaces fs.rename's ENOENT text; keep the same shape.
			return fmt.Errorf("ENOENT: no such file or directory, rename '%s' -> '%s'", filePath, newPath)
		}
		return err
	}
	return nil
}

// ScriptCreateInput is the POST /api/scripts body after multipart parsing.
type ScriptCreateInput struct {
	Filename       string
	Path           string
	Content        *string
	SkipHistory    bool
	ExpectedHash   string
	OriginFilename string
	Directory      string
	// UploadPath is the temporary file multer would have written to .tmp.
	UploadPath string
}

// Create handles every POST /api/scripts variant: upload, new directory, save
// as, and plain create.
func (s *ScriptService) Create(input ScriptCreateInput) (any, error) {
	directory := input.Path
	if directory == "" {
		directory = s.cfg.ScriptPath
	}
	if !strings.HasSuffix(directory, "/") && !strings.HasSuffix(directory, string(filepath.Separator)) {
		directory += "/"
	}
	if !strings.HasPrefix(directory, "/") && !filepath.IsAbs(directory) {
		// A relative directory is resolved against the scripts root. Node calls
		// path.join(scriptPath, path) unconditionally, which on Windows turns
		// the defaulted absolute scriptPath into
		// "…\scripts\…\scripts" and answers 403; the panel's own UI sends an
		// empty path for "create in the scripts root", so the intent wins here.
		directory = filepath.Join(s.cfg.ScriptPath, directory)
	}
	allowedRoot := false
	for _, writable := range s.writePaths() {
		if strings.HasPrefix(directory, writable) {
			allowedRoot = true
			break
		}
	}
	if !allowedRoot {
		return nil, forbidden()
	}

	if input.UploadPath != "" {
		uploadPath := filepath.Join(directory, input.Filename)
		if !s.isPathAllowed(uploadPath) {
			return nil, forbidden()
		}
		if err := copyFile(input.UploadPath, uploadPath); err != nil {
			return nil, err
		}
		_ = os.Remove(input.UploadPath)
		return nil, nil
	}

	if input.Directory != "" {
		dirPath := filepath.Join(directory, input.Directory)
		if !s.isPathAllowed(dirPath) {
			return nil, forbidden()
		}
		if err := os.MkdirAll(dirPath, 0o755); err != nil {
			return nil, err
		}
		return nil, nil
	}

	originFilename := input.OriginFilename
	if originFilename == "" {
		originFilename = input.Filename
	}
	originFilePath := filepath.Join(directory, originFilename)
	filePath := filepath.Join(directory, input.Filename)
	if !s.isPathAllowed(filePath) || !s.isPathAllowed(originFilePath) {
		return nil, forbidden()
	}

	// Create the parent directory only inside a writable root.
	parentPath := resolvePath(filepath.Dir(filePath))
	parentCreated := false
	for _, writable := range s.writePaths() {
		root := resolvePath(writable)
		if equalPath(parentPath, root) || isStrictlyWithinPath(root, parentPath) {
			if err := os.MkdirAll(parentPath, 0o755); err != nil {
				return nil, err
			}
			parentCreated = true
			break
		}
	}
	if !parentCreated {
		return nil, forbidden()
	}

	content := ""
	if input.Content != nil {
		content = *input.Content
	}

	exists := pathExists(filePath)
	if exists && resolveFileAccess(s.cfg.ScriptPath, []string{filePath}, nil) != "" {
		removeSource := input.Filename != originFilename
		backupSource := ""
		if removeSource {
			originReal, err := s.existingWritableRealPath(originFilePath)
			if err != nil {
				return nil, err
			}
			targetReal, err := s.existingWritableRealPath(filePath)
			if err != nil {
				return nil, err
			}
			// Aliases of one script are an in-place save: removing the source
			// would also remove the destination behind a target symlink.
			removeSource = originReal != targetReal
			backupSource = originReal
		}
		if removeSource {
			// Save-as removes the source after committing the destination, so
			// keep its original content too.
			backupName := strings.ReplaceAll(originFilename, "/", "")
			if err := copyFile(backupSource, filepath.Join(s.cfg.BakPath, backupName)); err != nil {
				return nil, err
			}
		}
		result, err := s.history.Save(directory, input.Filename, content, SaveOptions{
			SkipHistory:  input.SkipHistory,
			ExpectedHash: input.ExpectedHash,
		})
		if err != nil {
			return nil, err
		}
		if removeSource {
			_ = os.RemoveAll(originFilePath)
		}
		return result, nil
	}

	if err := writeFileWithLock(filePath, content); err != nil {
		return nil, err
	}
	return nil, nil
}

// Save handles PUT /api/scripts: the file must exist and is versioned.
func (s *ScriptService) Save(directory, filename, content string, options SaveOptions) (*HistorySaveResult, error) {
	if s.CheckFilePath(directory, filename) == "" {
		return nil, forbidden()
	}
	return s.history.Save(directory, filename, content, options)
}

// Restore handles PUT /api/scripts/history/restore.
func (s *ScriptService) Restore(directory, filename, id, expectedHash string) (*HistorySaveResult, error) {
	return s.history.Restore(directory, filename, id, expectedHash)
}

// SetSock forwards the websocket hub to the task runner, which streams the
// debug script output under the manuallyRunScript topic.
func (s *ScriptService) SetSock(sock *SockService) { s.crons.SetSock(sock) }

// swapPath builds the temporary script name the run/stop endpoints use.
func (s *ScriptService) swapPath(directory, filename string) string {
	name := strings.TrimSuffix(filename, filepath.Ext(filename))
	ext := filepath.Ext(filename)
	return filepath.Join(s.cfg.ScriptPath, directory, name+".swap"+ext)
}

// Run writes the editor content to a .swap file and executes it in real time,
// returning the pid like Node's runTask(..., 'start').
func (s *ScriptService) Run(directory, filename string, content string) (int64, error) {
	filePath := s.swapPath(directory, filename)
	if !s.isPathAllowed(filePath) {
		return 0, forbidden()
	}
	if err := writeFileWithLock(filePath, content); err != nil {
		return 0, err
	}
	pid, err := s.crons.RunScriptFile(filePath)
	if err != nil {
		return 0, err
	}
	return pid, nil
}

// Stop ends a running script and removes its log directory after three
// seconds, mirroring stopScript().
func (s *ScriptService) Stop(directory, filename string, pid int64) error {
	filePath := s.swapPath(directory, filename)
	if !s.isPathAllowed(filePath) {
		return forbidden()
	}
	name := strings.TrimSuffix(filename, filepath.Ext(filename))
	logPath := resolveFileAccess(s.cfg.LogPath, []string{directory, name + ".swap"}, nil)
	if logPath == "" {
		return forbidden()
	}
	if err := s.crons.StopScriptFile(pid); err != nil {
		s.logger.Warn("[panel] stop script failed", "path", filePath, "pid", pid, "error", err)
	}
	// The log directory of an ad-hoc run is disposable; Node removes it after
	// three seconds so a debug run does not pile up files.
	time.AfterFunc(3*time.Second, func() {
		_ = os.RemoveAll(logPath)
	})
	return nil
}

// writeFileWithLock writes a script, creating the parent directory when it is
// missing (Node creates the file first and lets the panel's own paths win).
func writeFileWithLock(filePath, content string) error {
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filePath, []byte(content), 0o644)
}

// fileExists reports whether the path exists (following symlinks).
func pathExists(filePath string) bool {
	_, err := os.Stat(filePath)
	return err == nil
}

// copyFile copies src to dst, replacing dst. The destination directory must
// exist: both call sites create it beforehand.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
