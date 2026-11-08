package service

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"qinglong-go/internal/i18n"
)

// SampleFile is one starter file offered by the config page
// (SAMPLE_FILES in back/config/const.ts).
type SampleFile struct {
	Title  string `json:"title"`
	Value  string `json:"value"`
	Target string `json:"target"`
}

// sampleFiles mirrors the constant list.
var sampleFiles = []SampleFile{
	{Title: "config.sample.sh", Value: "sample/config.sample.sh", Target: "config.sh"},
	{Title: "notify.js", Value: "sample/notify.js", Target: "data/scripts/sendNotify.js"},
	{Title: "notify.py", Value: "sample/notify.py", Target: "data/scripts/notify.py"},
}

// sampleBaseURL is where the sample files are fetched from when the local
// checkout does not ship them.
const sampleBaseURL = "https://gitlab.com/whyour/qinglong/-/raw/master/"

// SampleFiles implements GET /api/configs/samples.
func (s *SystemService) SampleFiles() []SampleFile { return sampleFiles }

// ConfigFile is one row of GET /api/configs/files.
type ConfigFile struct {
	Title string `json:"title"`
	Value string `json:"value"`
}

// ConfigFiles implements GET /api/configs/files.
func (s *SystemService) ConfigFiles(ctx context.Context) ([]ConfigFile, error) {
	entries, err := os.ReadDir(s.cfg.ConfigPath)
	if err != nil {
		return nil, err
	}
	blocked := map[string]struct{}{}
	for _, name := range blackFileList {
		blocked[name] = struct{}{}
	}
	out := make([]ConfigFile, 0, len(entries))
	for _, entry := range entries {
		if _, skip := blocked[entry.Name()]; skip {
			continue
		}
		out = append(out, ConfigFile{Title: entry.Name(), Value: entry.Name()})
	}
	return out, nil
}

// ConfigDetail implements GET /api/configs/detail. A file outside data/config
// or on the blacklist answers 403 with HTTP 200, like the Node handler.
func (s *SystemService) ConfigDetail(ctx context.Context, filePath string) (*Reply, error) {
	if filePath == "" || strings.HasPrefix(filePath, "data/scripts/") {
		return &Reply{Code: 403, Message: i18n.T("文件无法访问")}, nil
	}
	resolved := resolveFileAccess(s.cfg.ConfigPath, []string{filePath}, blackFileList)
	if resolved == "" {
		return &Reply{Code: 403, Message: i18n.T("文件无法访问")}, nil
	}

	var content string
	if strings.HasPrefix(filePath, "sample/") {
		client := &http.Client{Timeout: 15 * time.Second}
		response, err := client.Get(sampleBaseURL + filePath)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
		if err != nil {
			return nil, err
		}
		content = string(body)
	} else {
		body, err := os.ReadFile(resolved)
		if err != nil {
			return nil, err
		}
		content = string(body)
	}
	return &Reply{Code: 200, Data: content}, nil
}

// SaveConfig implements POST /api/configs/save.
func (s *SystemService) SaveConfig(ctx context.Context, name, content string) (*Reply, error) {
	if strings.HasPrefix(name, "data/scripts/") {
		return &Reply{Code: 403, Message: i18n.T("文件无法访问")}, nil
	}
	resolved := resolveFileAccess(s.cfg.ConfigPath, []string{name}, blackFileList)
	if resolved == "" {
		return &Reply{Code: 403, Message: i18n.T("文件无法访问")}, nil
	}
	if err := writeFileWithLock(resolved, content); err != nil {
		return nil, err
	}
	// config.sh feeds RandomDelay, CommandTimeoutTime and the other task
	// settings. Node picks edits up on the next run because task.sh sources the
	// file; the panel caches the values in memory, so refresh them here or the
	// change would only apply after a restart.
	if s.cfg != nil && resolved == filepath.Join(s.cfg.ConfigPath, "config.sh") {
		if err := s.cfg.Reload(); err != nil {
			s.logger.Warn("[config] reload config.sh failed", "error", err)
		}
	}
	return &Reply{Code: 200, Message: i18n.T("保存成功")}, nil
}

// ConfigsGone implements GET /api/configs/:file, the retired endpoint.
func (s *SystemService) ConfigsGone() *Reply {
	return &Reply{Code: 410, Message: i18n.T("接口已下线，请使用 /configs/detail 接口")}
}

// blackFileList mirrors config.blackFileList.
var blackFileList = []string{
	"auth.json",
	"config.sh.sample",
	"cookie.sh",
	"crontab.list",
	"dependence-proxy.sh",
	"env.sh",
	"env.js",
	"env.py",
	"token.json",
	"grpc",
	"__pycache__",
}
