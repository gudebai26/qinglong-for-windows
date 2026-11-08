package platform

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Tool is one external executable the panel shells out to.
type Tool struct {
	Name    string
	Path    string
	Version string
	Found   bool
	// Required marks the tools whose absence breaks a whole page. The boot
	// doctor logs those at WARN so an operator sees the reason before the
	// first subscription or dependency request fails.
	Required bool
}

// ToolNames lists the executables probed by the doctor, in report order.
// "python" and "pip" are resolved through the Node names first (python3/pip3)
// and fall back to the Windows ones; the fallback is recorded in Tool.Path so
// callers never have to repeat it.
var ToolNames = []string{"git", "pnpm", "npm", "node", "python", "pip", "bash"}

// toolCandidates maps a logical tool name to the executables tried in order.
var toolCandidates = map[string][]string{
	"git":    {"git"},
	"pnpm":   {"pnpm"},
	"npm":    {"npm"},
	"node":   {"node"},
	"python": {"python3", "python"},
	"pip":    {"pip3", "pip"},
	"bash":   {"bash"},
}

// versionArgs asks a tool for its version. Only used for the boot log, so a
// failure just leaves the field empty.
var versionArgs = map[string][]string{
	"git":    {"--version"},
	"pnpm":   {"--version"},
	"npm":    {"--version"},
	"node":   {"--version"},
	"python": {"--version"},
	"pip":    {"--version"},
	"bash":   {"--version"},
}

// Toolchain probes the external tools once and caches the result. The panel
// only needs the executable path, and probing on every dependency install
// would add a process spawn to each request.
type Toolchain struct {
	mu     sync.Mutex
	cache  map[string]Tool
	lookup func(string) (string, error)
}

// NewToolchain returns a toolchain that resolves executables from PATH.
func NewToolchain() *Toolchain {
	return &Toolchain{cache: map[string]Tool{}, lookup: exec.LookPath}
}

// newToolchainWithLookup is the test seam.
func newToolchainWithLookup(lookup func(string) (string, error)) *Toolchain {
	return &Toolchain{cache: map[string]Tool{}, lookup: lookup}
}

// Find resolves one tool, probing at most once.
func (t *Toolchain) Find(name string) (Tool, bool) {
	if t == nil {
		return Tool{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if cached, ok := t.cache[name]; ok {
		return cached, cached.Found
	}
	tool := t.probe(name)
	t.cache[name] = tool
	return tool, tool.Found
}

// Path returns the executable path, or "" when the tool is missing.
func (t *Toolchain) Path(name string) string {
	tool, _ := t.Find(name)
	return tool.Path
}

// Refresh forgets the cached probes. It is called after a dependency install
// so a tool installed through the panel becomes visible without a restart.
func (t *Toolchain) Refresh() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cache = map[string]Tool{}
}

// Doctor reports every known tool. Missing git/pnpm are marked Required so the
// boot log can flag them, and the subscription/dependency services use the same
// data to return an actionable message instead of a timeout.
func (t *Toolchain) Doctor() []Tool {
	tools := make([]Tool, 0, len(ToolNames))
	for _, name := range ToolNames {
		tool, _ := t.Find(name)
		tool.Required = name == "git" || name == "pnpm"
		tools = append(tools, tool)
	}
	return tools
}

func (t *Toolchain) probe(name string) Tool {
	tool := Tool{Name: name}
	// bash and git get a wider search than PATH: on Windows they usually live
	// under <Git>\usr\bin and <Git>\cmd, which the installer does not add to
	// PATH unless the user ticked "Add to PATH".
	if name == "bash" || name == "git" {
		path := BashPath()
		if name == "git" {
			path = GitPath()
		}
		if path != "" {
			tool.Path = path
			tool.Found = true
			tool.Version = probeVersion(path, versionArgs[name])
		}
		return tool
	}
	candidates, ok := toolCandidates[name]
	if !ok {
		candidates = []string{name}
	}
	for _, candidate := range candidates {
		path, err := t.lookup(candidate)
		if err != nil || strings.TrimSpace(path) == "" {
			continue
		}
		tool.Path = path
		tool.Found = true
		tool.Version = probeVersion(path, versionArgs[name])
		return tool
	}
	return tool
}

// probeVersion runs `tool --version` with a short timeout. A hung tool (or one
// that opens a pager) must never delay the panel boot.
func probeVersion(path string, args []string) string {
	if len(args) == 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, path, args...).CombinedOutput()
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(output))
	if index := strings.IndexByte(line, '\n'); index >= 0 {
		line = strings.TrimSpace(line[:index])
	}
	return line
}

// pnpmRootCache memoizes `pnpm root -g`. The panel asks for it from the task
// runner (NODE_PATH) and from the dependency page, and each probe costs a
// process spawn.
var (
	pnpmRootOnce sync.Once
	pnpmRootPath string
	pnpmRootErr  string
)

// PnpmGlobalRoot returns the pnpm global module directory.
//
// pnpm refuses to answer (`ERR_PNPM_GLOBAL_BIN_DIR_NOT_IN_PATH`) when its
// global bin directory is missing from PATH. That is the same condition that
// makes `pnpm add -g` fail, so the caller gets the error text and can show
// pnpm's own guidance instead of a silent empty NODE_PATH entry.
func PnpmGlobalRoot() (string, error) {
	pnpmRootOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, "pnpm", "root", "-g").CombinedOutput()
		if err != nil {
			pnpmRootErr = strings.TrimSpace(string(output))
			if pnpmRootErr == "" {
				pnpmRootErr = err.Error()
			}
			return
		}
		value := strings.TrimSpace(string(output))
		if info, statErr := os.Stat(value); statErr == nil && info.IsDir() {
			pnpmRootPath = value
			return
		}
		pnpmRootErr = "pnpm root -g returned " + value
	})
	if pnpmRootPath == "" {
		if pnpmRootErr == "" {
			return "", exec.ErrNotFound
		}
		return "", errors.New(pnpmRootErr)
	}
	return pnpmRootPath, nil
}

// PnpmNodePathEntries returns the directories that must be on NODE_PATH for
// globally installed pnpm packages to resolve.
//
// pnpm 10 and older answer `pnpm root -g` with a directory that holds the
// packages directly (<global>/5/node_modules). pnpm 11+ returns the versioned
// global directory (<global>/v11) and stores every install under
// <hash>/node_modules, so those subdirectories are added as well.
func PnpmNodePathEntries() []string {
	root, err := PnpmGlobalRoot()
	if err != nil || root == "" {
		// pnpm root -g can fail with "global bin directory not in PATH" when
		// pnpm setup could not persist PNPM_HOME (e.g. a locked-down host). The
		// install still works; recover the root from the environment instead.
		root = fallbackPnpmGlobalRoot()
	}
	if root == "" {
		return nil
	}
	entries := make([]string, 0, 4)
	if isDirectory(root) {
		children, readErr := os.ReadDir(root)
		if readErr == nil {
			for _, child := range children {
				if !child.IsDir() {
					continue
				}
				candidate := filepath.Join(root, child.Name(), "node_modules")
				if isDirectory(candidate) {
					entries = append(entries, candidate)
				}
				// pnpm 11+ nests once more: <global>/v11/<hash>/node_modules.
				grandchildren, grandErr := os.ReadDir(filepath.Join(root, child.Name()))
				if grandErr == nil {
					for _, grand := range grandchildren {
						if !grand.IsDir() {
							continue
						}
						nested := filepath.Join(root, child.Name(), grand.Name(), "node_modules")
						if isDirectory(nested) {
							entries = append(entries, nested)
						}
					}
				}
			}
		}
		if nested := filepath.Join(root, "node_modules"); isDirectory(nested) {
			entries = append(entries, nested)
		}
		entries = append(entries, root)
	}
	return entries
}

// fallbackPnpmGlobalRoot derives the global root from PNPM_HOME when `pnpm
// root -g` cannot run. It mirrors the directory pnpm setup would have written
// into the user profile.
func fallbackPnpmGlobalRoot() string {
	home := strings.TrimSpace(os.Getenv("PNPM_HOME"))
	if home == "" {
		return ""
	}
	root := filepath.Join(home, "global")
	if isDirectory(root) {
		return root
	}
	return ""
}

func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// ResetPnpmGlobalRoot forgets the cached probe, so an install that just fixed
// the environment (or installed pnpm itself) is picked up.
func ResetPnpmGlobalRoot() {
	pnpmRootOnce = sync.Once{}
	pnpmRootPath = ""
	pnpmRootErr = ""
}

// pnpmBinHint extracts the directory pnpm asks for from its own error message:
// `The configured global bin directory "C:/.../pnpm/bin" is not in PATH`.
var pnpmBinHint = regexp.MustCompile(`global bin directory "([^"]+)" is not in PATH`)

// PnpmSetupHint turns pnpm's error into one line an operator can act on.
func PnpmSetupHint(err error) string {
	if err == nil {
		return ""
	}
	if match := pnpmBinHint.FindStringSubmatch(err.Error()); match != nil {
		return "pnpm 全局 bin 目录不在 PATH 中（" + match[1] + "），请执行 pnpm setup 后重启面板"
	}
	return strings.TrimSpace(err.Error())
}
