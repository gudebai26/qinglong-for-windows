package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"qinglong-go/internal/i18n"
	"qinglong-go/internal/platform"
)

// lastVersionURL is where the panel asks for the newest release
// (config.lastVersionFile).
const lastVersionURL = "https://qn.whyour.cn/version.yaml"

// versionPattern is the version shape /^\d+\.\d+\.\d+$/.
var versionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// UpgradeCheck is PUT /api/system/update-check.
type UpgradeCheck struct {
	HasNewVersion bool   `json:"hasNewVersion"`
	LastVersion   string `json:"lastVersion"`
	LastLog       string `json:"lastLog"`
	LastLogLink   string `json:"lastLogLink"`
}

// CheckUpdate implements PUT /api/system/update-check.
func (s *SystemService) CheckUpdate(ctx context.Context) *Reply {
	client := &http.Client{Timeout: 15 * time.Second}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		lastVersionURL+"?t="+strconv.FormatInt(time.Now().UnixMilli(), 10), nil)
	if err != nil {
		return &Reply{Code: 400, Message: i18n.T("检查更新失败，请检查网络后重试")}
	}
	response, err := client.Do(request)
	if err != nil {
		return &Reply{Code: 400, Message: i18n.T("检查更新失败，请检查网络后重试")}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return &Reply{Code: 400, Message: i18n.T("检查更新失败，请检查网络后重试")}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return &Reply{Code: 400, Message: i18n.T("检查更新失败，请检查网络后重试")}
	}
	last := platform.ParseVersionDoc(string(body))
	if !versionPattern.MatchString(last.Version) {
		return &Reply{Code: 400, Message: i18n.T("检查更新失败，请检查网络后重试")}
	}
	current := platform.ReadVersionDoc(s.cfg.RootPath)
	return &Reply{Code: 200, Data: UpgradeCheck{
		HasNewVersion: hasNewVersion(current.Version, last.Version),
		LastVersion:   last.Version,
		LastLog:       last.ChangeLog,
		LastLogLink:   last.ChangeLogLink,
	}}
}

// hasNewVersion ports checkHasNewVersion: compare major, minor then patch.
func hasNewVersion(current, last string) bool {
	currentParts, currentOK := versionParts(current)
	lastParts, lastOK := versionParts(last)
	if !currentOK || !lastOK {
		return false
	}
	for index := 0; index < 3; index++ {
		if currentParts[index] < lastParts[index] {
			return true
		}
		if currentParts[index] > lastParts[index] {
			return false
		}
	}
	return false
}

func versionParts(value string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimSpace(value), ".")
	if len(parts) < 3 {
		return out, false
	}
	for index := 0; index < 3; index++ {
		parsed, err := strconv.Atoi(parts[index])
		if err != nil {
			return out, false
		}
		out[index] = parsed
	}
	return out, true
}

// UpgradeStatus implements GET /api/system/update-status. The state file is the
// one the Node CLI writes, so a panel migrated from Node keeps reporting the
// last upgrade instead of a blank slate.
func (s *SystemService) UpgradeStatus() map[string]any {
	stateFile := filepath.Join(s.cfg.TmpPath, "panel-upgrade.json")
	state := map[string]any{"status": "idle"}
	if content, err := os.ReadFile(stateFile); err == nil {
		parsed := map[string]any{}
		if json.Unmarshal(content, &parsed) == nil {
			state = parsed
		}
	}
	// Bound the log read to the last 32 KiB, like getStatus does.
	logFile := filepath.Join(s.cfg.TmpPath, "panel-upgrade.log")
	if file, err := os.Open(logFile); err == nil {
		defer file.Close()
		if info, err := file.Stat(); err == nil && info.Size() > 0 {
			offset := info.Size() - 32768
			if offset < 0 {
				offset = 0
			}
			buffer := make([]byte, info.Size()-offset)
			if _, err := file.ReadAt(buffer, offset); err == nil {
				state["log"] = string(buffer)
			}
		}
	}
	return map[string]any{"code": 200, "data": state}
}

// UpdateSystem implements PUT /api/system/update and PUT /api/update/system.
// The single-binary build has no package manager to download into, so the
// endpoint reports that online upgrade is unavailable instead of pretending.
func (s *SystemService) UpdateSystem() *Reply {
	return &Reply{Code: 400, Message: i18n.T("当前版本不支持在线升级")}
}

// Reload implements PUT /api/system/reload and the three /api/update routes.
func (s *SystemService) Reload(target string) *Reply {
	if target != "" && target != "system" && target != "data" {
		return &Reply{Code: 400, Message: i18n.T("参数错误")}
	}
	if target == "system" {
		return &Reply{Code: 400, Message: i18n.T("当前版本不支持在线升级")}
	}
	// Node spawns `ql reload` and exits; the supervisor restarts the worker.
	if s.reload != nil {
		go func() {
			// Give the 200 response time to flush before the listener closes.
			// 100ms is enough when idle, but a busy panel can still have other
			// requests queued; 250ms stays imperceptible to the user while
			// leaving the reply safely on the wire.
			time.Sleep(250 * time.Millisecond)
			s.reload()
		}()
	}
	return &Reply{Code: 200}
}

// StartNodeMirror implements PUT /api/system/config/node-mirror. It applies the
// registry and, when the panel already installed global packages, reinstalls
// them so they come from the new mirror.
func (s *SystemService) StartNodeMirror(mirror string) (int, error) {
	pnpm := s.tools.Path("pnpm")
	if pnpm == "" {
		return 0, &BusinessError{Code: 400, Message: "未检测到 pnpm，请先安装 pnpm"}
	}
	var args []string
	if strings.TrimSpace(mirror) == "" {
		args = []string{"config", "delete", "registry"}
	} else {
		args = []string{"config", "set", "registry", mirror}
	}
	command := exec.Command(pnpm, args...)
	platform.PrepareCommand(command)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return 0, err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return 0, err
	}
	if err := command.Start(); err != nil {
		return 0, err
	}
	pid := command.Process.Pid

	go func() {
		defer func() {
			s.sock.Send(SockMessage{Type: "updateNodeMirror", Message: "update node mirror end", Status: "completed"})
		}()
		pump := func(reader io.Reader) {
			buffer := make([]byte, 4096)
			for {
				read, err := reader.Read(buffer)
				if read > 0 && s.sock != nil {
					s.sock.Send(SockMessage{Type: "updateNodeMirror", Message: string(buffer[:read])})
				}
				if err != nil {
					return
				}
			}
		}
		done := make(chan struct{}, 2)
		go func() { pump(stdout); done <- struct{}{} }()
		go func() { pump(stderr); done <- struct{}{} }()
		<-done
		<-done
		if err := command.Wait(); err != nil && s.sock != nil {
			s.sock.Send(SockMessage{Type: "updateNodeMirror", Message: err.Error()})
			return
		}
		// `&& pnpm i -g` in Node: refresh the installed packages.
		count, err := s.store.CountDependencesByTypeStatus(context.Background(), DepTypeNodejs, DepStatusInstalled)
		if err != nil || count == 0 {
			return
		}
		install := exec.Command(pnpm, "i", "-g")
		platform.PrepareCommand(install)
		output, err := install.CombinedOutput()
		if s.sock == nil {
			return
		}
		if len(output) > 0 {
			s.sock.Send(SockMessage{Type: "updateNodeMirror", Message: string(output)})
		}
		if err != nil {
			s.sock.Send(SockMessage{Type: "updateNodeMirror", Message: err.Error()})
		}
	}()
	return pid, nil
}

// SetNodeMirror stores the mirror and kicks off the registry update.
func (s *SystemService) SetNodeMirror(ctx context.Context, mirror *string, present bool) (*Reply, error) {
	if !present {
		// No key in the body: Node answers {code:200,data:{}} without touching
		// pnpm or the stored setting.
		return &Reply{Code: 200, Data: map[string]any{}}, nil
	}
	patch := map[string]any{"nodeMirror": nil}
	value := ""
	if mirror != nil {
		patch["nodeMirror"] = *mirror
		value = *mirror
	}
	if _, err := s.updateInfo(ctx, patch); err != nil {
		return nil, err
	}
	pid, err := s.StartNodeMirror(value)
	if err != nil {
		return nil, err
	}
	echo := map[string]any{"pid": pid}
	if present {
		if mirror != nil {
			echo["nodeMirror"] = *mirror
		} else {
			echo["nodeMirror"] = nil
		}
	}
	return &Reply{Code: 200, Data: echo}, nil
}

// SetLinuxMirror implements PUT /api/system/config/linux-mirror. Rewriting the
// apt sources needs a Linux host and root privileges; the single-file Windows
// build reports that instead of hanging like the Node handler does.
func (s *SystemService) SetLinuxMirror(ctx context.Context, mirror *string) (*Reply, error) {
	if runtime.GOOS != "linux" {
		return &Reply{Code: 400, Message: i18n.T("当前系统不支持修改 Linux 镜像源")}, nil
	}
	return &Reply{Code: 400, Message: i18n.T("当前版本不支持在线修改 Linux 镜像源")}, nil
}
