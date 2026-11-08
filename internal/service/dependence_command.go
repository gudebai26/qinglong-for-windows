package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"qinglong-go/internal/i18n"
	"qinglong-go/internal/platform"
)

// This file ports the command table of back/config/util.ts (getInstallCommand,
// getUninstallCommand, getGetCommand, detectOS) plus the Python version parsing
// the dependency service shares with it.

// Dependence type values (back/data/dependence.ts DependenceTypes).
const (
	DepTypeNodejs  int64 = 0
	DepTypePython3 int64 = 1
	DepTypeLinux   int64 = 2
)

// Dependence status values (back/data/dependence.ts DependenceStatus).
const (
	DepStatusInstalling    int64 = 0
	DepStatusInstalled     int64 = 1
	DepStatusInstallFailed int64 = 2
	DepStatusRemoving      int64 = 3
	DepStatusRemoved       int64 = 4
	DepStatusRemoveFailed  int64 = 5
	DepStatusQueued        int64 = 6
	DepStatusCancelled     int64 = 7
)

// pythonPipScript is the installed-check program Node feeds to `python -c`.
// It answers with the installed version (or the name) only when the
// requirement is satisfied, and prints nothing otherwise.
const pythonPipScript = `from importlib.metadata import version, PackageNotFoundError
import importlib.util
import sys
try:
 try:
  from packaging.requirements import Requirement
 except ImportError:
  from pip._vendor.packaging.requirements import Requirement
 requirement = Requirement(sys.argv[1])
 if requirement.marker and not requirement.marker.evaluate():
  print('not applicable')
 else:
  try:
   installed = version(requirement.name)
  except PackageNotFoundError:
   if not requirement.specifier and not requirement.extras and not requirement.url:
    if importlib.util.find_spec(requirement.name):
     print(requirement.name)
  else:
   if requirement.specifier.contains(installed, prereleases=True):
    print(installed)
except Exception:
 pass`

var (
	githubURLPattern     = regexp.MustCompile(`^(?i:git\+https?://|https?://)`)
	requirementsPattern  = regexp.MustCompile(`(?i)requirements.*\.(txt|in)$`)
	nodeVersionPattern   = regexp.MustCompile(`(.*)@([0-9.\-+a-zA-Z]*)`)
	pythonVersionPattern = regexp.MustCompile(`(.*)==([0-9.\-+a-zA-Z]*)`)
	// pipPackageNamePattern strips extras, specifiers and markers.
	pipPackageNamePattern = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*)(?:\[[^\]]+\])?`)
)

// versionSeparator mirrors versionDependenceCommandTypes.
func versionSeparator(depType int64) string {
	switch depType {
	case DepTypeNodejs:
		return "@"
	case DepTypePython3:
		return "=="
	case DepTypeLinux:
		return "="
	default:
		return ""
	}
}

func isGitHubURL(name string) bool { return githubURLPattern.MatchString(name) }

func isRequirementsFile(name string) bool { return requirementsPattern.MatchString(name) }

func isPyprojectToml(name string) bool { return strings.HasSuffix(name, "pyproject.toml") }

// IsPythonSourceDependency reports dependencies that cannot be probed.
func IsPythonSourceDependency(name string) bool {
	source := strings.TrimSpace(name)
	return isGitHubURL(source) || isRequirementsFile(source) || isPyprojectToml(source)
}

// pythonVenvDir mirrors process.env.PYTHON_VENV_DIR.
func (s *DependenceService) pythonVenvDir() string {
	return strings.TrimSpace(os.Getenv("PYTHON_VENV_DIR"))
}

// pythonInstallDir mirrors process.env.PYTHON_HOME (PYTHON_INSTALL_DIR).
func (s *DependenceService) pythonInstallDir() string {
	return strings.TrimSpace(os.Getenv("PYTHON_HOME"))
}

// pythonExecutablePath returns the interpreter without quoting, for argv-based
// execution. pythonExecutable keeps the quoted form for log messages.
func (s *DependenceService) pythonExecutablePath() string {
	if venv := s.pythonVenvDir(); venv != "" {
		return resolvePath(s.cfg.RootPath, venv, "bin/python3")
	}
	if path := s.tools.Path("python"); path != "" {
		return path
	}
	return "python3"
}

// pythonExecutable ports getPythonExecutable. Outside a venv the panel uses
// the same interpreter the task runner resolves (python3, then python), so a
// package installed here is importable by a task.
func (s *DependenceService) pythonExecutable() string {
	return shellQuote(s.pythonExecutablePath())
}

// pythonPipArgs returns the argv prefix that runs pip through the task
// interpreter. Node hardcodes `pip3`, which on Windows often belongs to a
// different interpreter than the `python3` that runs tasks.
func (s *DependenceService) pythonPipArgs() []string {
	executable := s.pythonExecutablePath()
	if s.pythonVenvDir() != "" {
		return []string{executable, "-m", "pip"}
	}
	if s.pythonHasPip(executable) {
		return []string{executable, "-m", "pip"}
	}
	if path := s.tools.Path("pip"); path != "" {
		return []string{path}
	}
	return []string{"pip3"}
}

// pythonPip ports getPythonPip for log messages and validation.
func (s *DependenceService) pythonPip() string {
	return strings.Join(quoteArgs(s.pythonPipArgs()), " ")
}

// pythonHasPip probes `<python> -m pip --version` once. The probe runs without a
// shell: it must not depend on bash, which is exactly what a machine installing
// its first Python package may be missing.
func (s *DependenceService) pythonHasPip(executable string) bool {
	s.pipMu.Lock()
	defer s.pipMu.Unlock()
	if s.pipChecked {
		return s.pipAvailable
	}
	s.pipChecked = true
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s.pipAvailable = exec.CommandContext(ctx, executable, "-m", "pip", "--version").Run() == nil
	return s.pipAvailable
}

// pipInstallPrefix is ` --prefix='<PYTHON_HOME>'` when a prefix is configured
// outside a venv.
func (s *DependenceService) pipInstallPrefix() string {
	installDir := s.pythonInstallDir()
	if installDir == "" || s.pythonVenvDir() != "" {
		return ""
	}
	return " --prefix=" + shellQuote(installDir)
}

// pipRootUserAction is dropped inside a venv, where --root-user-action is
// meaningless.
func (s *DependenceService) pipRootUserAction() string {
	if s.pythonVenvDir() != "" {
		return ""
	}
	return " --root-user-action=ignore"
}

// resolveDependencyFile ports the [scriptPath, repoPath] lookup of
// getInstallCommand: a requirements file must live inside scripts or repo.
func (s *DependenceService) resolveDependencyFile(name string) (string, error) {
	roots := []string{s.cfg.ScriptPath, s.cfg.RepoPath}
	for _, root := range roots {
		if root == "" {
			continue
		}
		resolved := resolveFileAccess(root, []string{name}, nil)
		if resolved == "" {
			continue
		}
		if info, err := os.Stat(resolved); err == nil && info.Mode().IsRegular() {
			return resolved, nil
		}
	}
	return "", errors.New("Python dependency file must be inside scripts or repo")
}

// quoteArgs renders an argument vector the way a shell would echo it, for logs
// and for the rare case that needs a real shell (a proxy script must be
// sourced, which no argv form can express).
func quoteArgs(args []string) []string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		if arg == "" || strings.ContainsAny(arg, " \t\"'") {
			quoted[i] = shellQuote(arg)
			continue
		}
		quoted[i] = arg
	}
	return quoted
}

// errNoInverse marks a dependency that cannot be uninstalled as a unit: a
// pyproject directory or a source URL installs several distributions, and
// guessing which ones to remove would be worse than doing nothing.
var errNoInverse = errors.New("该依赖由多个发行包组成，请按包名单独卸载")

// installCommandArgs builds the argv for an install.
//
// This is the execution path, and it deliberately avoids a shell: the command
// table is POSIX syntax, and a machine installing its first Python package may
// have no bash at all (Git for Windows not installed). cmd.exe cannot parse the
// quoted form and answers with "The filename, directory name, or volume label
// syntax is incorrect".
func (s *DependenceService) installCommandArgs(depType int64, name string) ([]string, error) {
	name = strings.TrimSpace(name)
	switch depType {
	case DepTypePython3:
		args := append(s.pythonPipArgs(), "install", "--disable-pip-version-check")
		if s.pythonVenvDir() == "" {
			args = append(args, "--root-user-action=ignore")
		}
		if dir := s.pythonInstallDir(); dir != "" && s.pythonVenvDir() == "" {
			args = append(args, "--prefix="+dir)
		}
		if isRequirementsFile(name) || isPyprojectToml(name) {
			file, err := s.resolveDependencyFile(name)
			if err != nil {
				return nil, err
			}
			if isRequirementsFile(name) {
				return append(args, "-r", file), nil
			}
			return append(args, filepath.Dir(file)), nil
		}
		return append(args, "--", name), nil
	case DepTypeNodejs:
		pnpm := s.tools.Path("pnpm")
		if pnpm == "" {
			// ensureNodeTooling installs pnpm before this runs, so a bare name is
			// only a fallback for callers that build the command for display.
			pnpm = "pnpm"
		}
		return []string{pnpm, "add", "-g", name}, nil
	case DepTypeLinux:
		return append(strings.Fields(s.linuxCommands().install), name), nil
	default:
		return nil, errors.New("unsupported dependency type")
	}
}

// uninstallCommandArgs mirrors installCommandArgs.
func (s *DependenceService) uninstallCommandArgs(depType int64, name string) ([]string, error) {
	name = strings.TrimSpace(name)
	switch depType {
	case DepTypePython3:
		if isRequirementsFile(name) {
			file, err := s.resolveDependencyFile(name)
			if err != nil {
				return nil, err
			}
			args := append(s.pythonPipArgs(), "uninstall", "--disable-pip-version-check")
			if s.pythonVenvDir() == "" {
				args = append(args, "--root-user-action=ignore")
			}
			return append(args, "-y", "-r", file), nil
		}
		if isPyprojectToml(name) || isGitHubURL(name) {
			return nil, errNoInverse
		}
		packageName := name
		if match := pipPackageNamePattern.FindStringSubmatch(name); match != nil {
			packageName = match[1]
		}
		args := append(s.pythonPipArgs(), "uninstall", "--disable-pip-version-check")
		if s.pythonVenvDir() == "" {
			args = append(args, "--root-user-action=ignore")
		}
		return append(args, "-y", "--", packageName), nil
	case DepTypeNodejs:
		pnpm := s.tools.Path("pnpm")
		if pnpm == "" {
			return nil, errors.New("未检测到 pnpm")
		}
		return []string{pnpm, "remove", "-g", name}, nil
	case DepTypeLinux:
		return append(strings.Fields(s.linuxCommands().uninstall), name), nil
	default:
		return nil, errors.New("unsupported dependency type")
	}
}

// dependencyCommandArgs dispatches on install/uninstall for the executor.
func (s *DependenceService) dependencyCommandArgs(depType int64, name string, isInstall bool) ([]string, error) {
	if isInstall {
		return s.installCommandArgs(depType, name)
	}
	return s.uninstallCommandArgs(depType, name)
}

// getInstallCommand ports getInstallCommand and is used for validation and log
// messages; execution goes through installCommandArgs.
func (s *DependenceService) getInstallCommand(depType int64, name string) (string, error) {
	args, err := s.installCommandArgs(depType, name)
	if err != nil {
		return "", err
	}
	return strings.Join(quoteArgs(args), " "), nil
}

// getUninstallCommand ports getUninstallCommand.
func (s *DependenceService) getUninstallCommand(depType int64, name string) (string, error) {
	args, err := s.uninstallCommandArgs(depType, name)
	if errors.Is(err, errNoInverse) {
		// Keep the Node behaviour: the row is removable, the message explains
		// that the distributions have to be removed by name.
		return "printf '%s\\n' " + shellQuote(i18n.T(
			"Remove installed Python distributions separately by package name.")), nil
	}
	if err != nil {
		return "", err
	}
	return strings.Join(quoteArgs(args), " "), nil
}

// getGetCommand ports getGetCommand.
func (s *DependenceService) getGetCommand(depType int64, name string) string {
	trimmed := strings.TrimSpace(name)
	if depType == DepTypePython3 && IsPythonSourceDependency(trimmed) {
		// Nothing to probe: a source dependency always installs.
		return `echo ""`
	}
	switch depType {
	case DepTypeNodejs:
		return `pnpm ls -g  | grep "` + trimmed + `" | head -1`
	case DepTypePython3:
		return s.pythonExecutable() + " -c " + shellQuote(pythonPipScript) + " " + shellQuote(trimmed)
	case DepTypeLinux:
		if osType, ok := s.detectOS(); ok && osType == "Alpine" {
			return "apk info -es " + trimmed
		}
		return maybeSudo("dpkg-query -s " + trimmed)
	default:
		return `echo ""`
	}
}

// linuxCommandSet mirrors LINUX_DEPENDENCE_COMMAND.
type linuxCommandSet struct {
	install   string
	uninstall string
	info      string
	check     func(string) bool
}

func (s *DependenceService) linuxCommands() linuxCommandSet {
	osType, ok := s.detectOS()
	if !ok {
		// No package manager: the apt-get spelling mirrors getOsTypeSync's
		// fallback. Nothing runs it, because the worker checks detectOS first.
		osType = "Debian"
	}
	switch osType {
	case "Alpine":
		return linuxCommandSet{
			install:   "apk add --no-check-certificate",
			uninstall: "apk del",
			info:      "apk info -es",
			check:     func(info string) bool { return strings.Contains(info, "installed") },
		}
	default:
		return linuxCommandSet{
			install:   maybeSudo("apt-get install -y"),
			uninstall: maybeSudo("apt-get remove -y"),
			info:      maybeSudo("dpkg-query -s"),
			check:     func(info string) bool { return strings.Contains(info, "install ok installed") },
		}
	}
}

// detectOS ports detectOS: QL_OS_TYPE wins, then /etc/os-release, and anything
// that is not Linux reports "unsupported" (which is what the dependency page
// shows on Windows).
func (s *DependenceService) detectOS() (string, bool) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("QL_OS_TYPE"))) {
	case "alpine":
		return "Alpine", true
	case "debian":
		return "Debian", true
	case "ubuntu":
		return "Ubuntu", true
	}
	if runtime.GOOS != "linux" {
		return "", false
	}
	content, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "", false
	}
	info := string(content)
	switch {
	case strings.Contains(info, "Debian"):
		return "Debian", true
	case strings.Contains(info, "Ubuntu"):
		return "Ubuntu", true
	case strings.Contains(info, "Alpine"):
		return "Alpine", true
	case strings.Contains(info, "CentOS"), strings.Contains(info, "Red Hat"):
		return "Debian", true
	default:
		return "", false
	}
}

// maybeSudo mirrors config/container.ts: containers need sudo for apt/apk.
func maybeSudo(command string) string {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("QL_CONTAINER")), "true") {
		return "sudo " + command
	}
	return command
}

// splitDependencyVersion ports the `(.*)<sep>([0-9.\-+a-zA-Z]*)` parse shared
// by isDependenceInstalled and installOrUninstallDependency.
func splitDependencyVersion(depType int64, name string) (string, string) {
	separator := versionSeparator(depType)
	if separator == "" || depType == DepTypePython3 || !strings.Contains(name, separator) {
		return name, ""
	}
	pattern := nodeVersionPattern
	if depType == DepTypeLinux {
		pattern = pythonVersionPattern
	}
	match := pattern.FindStringSubmatch(name)
	if match == nil {
		return name, ""
	}
	if match[2] == "" || match[1] == "" {
		return name, ""
	}
	return match[1], match[2]
}

// normalizeDependencyInfo mirrors `.replace(/\s{2,}/, ' ').replace(/\s+$/, ”)`
// — note the first replace is not global.
func normalizeDependencyInfo(info string) string {
	if index := regexp.MustCompile(`\s{2,}`).FindStringIndex(info); index != nil {
		info = info[:index[0]] + " " + info[index[1]:]
	}
	return strings.TrimRight(info, " \t\r\n\v\f")
}

// runOutput ports promiseExecSuccess: stdout, or "" on failure. It goes
// through the same shell as the installer, because the probe commands are
// POSIX command lines too (single-quoted arguments, pipes).
func (s *DependenceService) runOutput(ctx context.Context, command string) string {
	file, args := platform.PosixShellCommand(command, s.tools.Path("bash"))
	cmd := exec.CommandContext(ctx, file, args...)
	platform.PrepareCommand(cmd)
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(output)
}
