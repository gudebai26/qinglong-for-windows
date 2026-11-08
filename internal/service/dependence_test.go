package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qinglong-go/internal/config"
	"qinglong-go/internal/platform"
)

func newDependenceTestService(t *testing.T) (*DependenceService, string) {
	t.Helper()
	root := t.TempDir()
	data := filepath.Join(root, "data")
	cfg := &config.Config{
		RootPath:   root,
		DataPath:   data,
		ConfigPath: filepath.Join(data, "config"),
		ScriptPath: filepath.Join(data, "scripts"),
		RepoPath:   filepath.Join(data, "repo"),
		Values:     map[string]string{},
	}
	for _, dir := range []string{cfg.ConfigPath, cfg.ScriptPath, cfg.RepoPath} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	return &DependenceService{cfg: cfg, tools: platform.NewToolchain()}, data
}

func TestGetInstallCommandNodejs(t *testing.T) {
	service, _ := newDependenceTestService(t)
	command, err := service.getInstallCommand(DepTypeNodejs, " is-number ")
	if err != nil {
		t.Fatalf("install command: %v", err)
	}
	if command != "pnpm add -g is-number" {
		t.Errorf("nodejs install = %q", command)
	}
	uninstall, err := service.getUninstallCommand(DepTypeNodejs, "is-number")
	if err != nil {
		t.Fatalf("uninstall command: %v", err)
	}
	if uninstall != "pnpm remove -g is-number" {
		t.Errorf("nodejs uninstall = %q", uninstall)
	}
	probe := service.getGetCommand(DepTypeNodejs, "is-number")
	if probe != `pnpm ls -g  | grep "is-number" | head -1` {
		t.Errorf("nodejs probe = %q", probe)
	}
}

func TestGetInstallCommandPythonInVenv(t *testing.T) {
	service, _ := newDependenceTestService(t)
	t.Setenv("PYTHON_VENV_DIR", "venv")
	t.Setenv("PYTHON_HOME", "")

	command, err := service.getInstallCommand(DepTypePython3, "six")
	if err != nil {
		t.Fatalf("install command: %v", err)
	}
	if !strings.HasSuffix(command, "-m pip install --disable-pip-version-check -- 'six'") {
		t.Errorf("venv install = %q", command)
	}
	// A venv drops --root-user-action and --prefix.
	if strings.Contains(command, "--root-user-action") || strings.Contains(command, "--prefix") {
		t.Errorf("venv install kept host flags: %q", command)
	}
	if !strings.Contains(command, "venv") || !strings.Contains(command, "bin") {
		t.Errorf("venv install does not use the venv interpreter: %q", command)
	}

	uninstall, err := service.getUninstallCommand(DepTypePython3, "six")
	if err != nil {
		t.Fatalf("uninstall command: %v", err)
	}
	if !strings.HasSuffix(uninstall, "-m pip uninstall --disable-pip-version-check -y -- 'six'") {
		t.Errorf("venv uninstall = %q", uninstall)
	}
}

func TestGetInstallCommandPythonHostFlags(t *testing.T) {
	service, _ := newDependenceTestService(t)
	t.Setenv("PYTHON_VENV_DIR", "")
	t.Setenv("PYTHON_HOME", "/opt/pyhome")

	if got := service.pipInstallPrefix(); got != " --prefix='/opt/pyhome'" {
		t.Errorf("prefix = %q", got)
	}
	if got := service.pipRootUserAction(); got != " --root-user-action=ignore" {
		t.Errorf("root user action = %q", got)
	}

	t.Setenv("PYTHON_VENV_DIR", "venv")
	if got := service.pipInstallPrefix(); got != "" {
		t.Errorf("venv prefix = %q", got)
	}
	if got := service.pipRootUserAction(); got != "" {
		t.Errorf("venv root user action = %q", got)
	}
}

func TestGetInstallCommandPythonRequirementFiles(t *testing.T) {
	service, data := newDependenceTestService(t)
	t.Setenv("PYTHON_VENV_DIR", "venv")

	requirements := filepath.Join(data, "scripts", "requirements.txt")
	if err := os.WriteFile(requirements, []byte("six\n"), 0o644); err != nil {
		t.Fatalf("write requirements: %v", err)
	}
	command, err := service.getInstallCommand(DepTypePython3, "requirements.txt")
	if err != nil {
		t.Fatalf("requirements install: %v", err)
	}
	if !strings.Contains(command, " -r '") || !strings.Contains(command, "requirements.txt'") {
		t.Errorf("requirements install = %q", command)
	}

	// pyproject.toml installs the project directory.
	project := filepath.Join(data, "repo", "author_repo")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	if err := os.WriteFile(filepath.Join(project, "pyproject.toml"), []byte("[project]\n"), 0o644); err != nil {
		t.Fatalf("write pyproject: %v", err)
	}
	command, err = service.getInstallCommand(DepTypePython3, "author_repo/pyproject.toml")
	if err != nil {
		t.Fatalf("pyproject install: %v", err)
	}
	if !strings.HasSuffix(command, "'"+filepath.ToSlash(project)+"'") &&
		!strings.HasSuffix(command, "'"+project+"'") {
		t.Errorf("pyproject install = %q", command)
	}

	// A file outside scripts/repo is refused, exactly like Node.
	if _, err := service.getInstallCommand(DepTypePython3, "missing-requirements.txt"); err == nil {
		t.Error("missing requirements file accepted")
	}
	if _, err := service.getUninstallCommand(DepTypePython3, "missing-requirements.txt"); err == nil {
		t.Error("missing requirements uninstall accepted")
	}
}

func TestGetUninstallCommandPythonSourceDependencies(t *testing.T) {
	service, _ := newDependenceTestService(t)
	t.Setenv("PYTHON_VENV_DIR", "venv")

	command, err := service.getUninstallCommand(DepTypePython3, "https://github.com/foo/bar/archive/main.zip")
	if err != nil {
		t.Fatalf("source uninstall: %v", err)
	}
	if !strings.HasPrefix(command, "printf ") {
		t.Errorf("source uninstall = %q", command)
	}

	// Extras and specifiers are stripped before uninstalling.
	command, err = service.getUninstallCommand(DepTypePython3, "requests[socks]>=2.0")
	if err != nil {
		t.Fatalf("extras uninstall: %v", err)
	}
	if !strings.HasSuffix(command, " -- 'requests'") {
		t.Errorf("extras uninstall = %q", command)
	}
}

func TestGetInstallCommandLinuxOnWindows(t *testing.T) {
	service, _ := newDependenceTestService(t)
	t.Setenv("QL_OS_TYPE", "")
	if _, err := service.getInstallCommand(DepTypeLinux, "curl"); err == nil {
		t.Skip("running on linux; the unsupported branch is Windows/macOS only")
	}

	// With an explicit OS type the Linux command is built even off Linux, which
	// is how the panel supports Alpine containers from a Debian host.
	t.Setenv("QL_OS_TYPE", "alpine")
	command, err := service.getInstallCommand(DepTypeLinux, "curl")
	if err != nil {
		t.Fatalf("alpine install: %v", err)
	}
	if command != "apk add --no-check-certificate curl" {
		t.Errorf("alpine install = %q", command)
	}
}

func TestPythonSourceDependency(t *testing.T) {
	cases := map[string]bool{
		"git+https://github.com/foo/bar": true,
		"https://github.com/foo/bar":     true,
		"requirements.txt":               true,
		"prod-requirements.in":           true,
		"pyproject.toml":                 true,
		"requests":                       false,
		"requests==2.0":                  false,
	}
	for name, want := range cases {
		if got := IsPythonSourceDependency(name); got != want {
			t.Errorf("IsPythonSourceDependency(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestSplitDependencyVersion(t *testing.T) {
	if name, version := splitDependencyVersion(DepTypeNodejs, "is-number@7.0.0"); name != "is-number" || version != "7.0.0" {
		t.Errorf("nodejs split = %q %q", name, version)
	}
	if name, version := splitDependencyVersion(DepTypePython3, "requests==2.0"); name != "requests==2.0" || version != "" {
		t.Errorf("python split must be skipped = %q %q", name, version)
	}
	if name, version := splitDependencyVersion(DepTypeNodejs, "is-number"); name != "is-number" || version != "" {
		t.Errorf("plain split = %q %q", name, version)
	}
}

func TestNormalizeDependencyInfo(t *testing.T) {
	// Only the first run of spaces collapses, like /\s{2,}/ without /g.
	if got := normalizeDependencyInfo("a  b  c   "); got != "a b  c" {
		t.Errorf("normalize = %q", got)
	}
	if got := normalizeDependencyInfo(""); got != "" {
		t.Errorf("normalize empty = %q", got)
	}
}
