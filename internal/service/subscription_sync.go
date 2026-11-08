package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"qinglong-go/internal/cronexpr"
	"qinglong-go/internal/i18n"
	"qinglong-go/internal/platform"
	"qinglong-go/internal/store"
)

// This file ports the shell subscription engine (shell/update.sh) to Go. The
// observable contract is the file tree under data/scripts/<uniq_path> and the
// rows add_cron creates, so every rule below mirrors the shell one instead of
// inventing a cleaner design:
//
//	gen_list_repo  -> file selection + copy order
//	diff_cron      -> add/drop lists
//	add_cron       -> cron name/schedule extraction and the `task <path>` command
//	del_cron       -> delete rows and the stale script files
//	update_raw     -> single file download

// execHandle lets Stop kill the child of an in-flight sync.
type execHandle struct {
	mu  sync.Mutex
	cmd *exec.Cmd
}

func (h *execHandle) set(cmd *exec.Cmd) {
	h.mu.Lock()
	h.cmd = cmd
	h.mu.Unlock()
}

func (h *execHandle) kill() {
	h.mu.Lock()
	cmd := h.cmd
	h.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = platform.KillProcessTree(cmd.Process.Pid)
}

// runShellCommand ports promiseExec: the output of a shell command, with
// stderr preferred on failure. sub_before/sub_after are POSIX snippets, so
// bash is preferred even on Windows (where the platform default is cmd.exe).
func (s *SubscriptionService) runShellCommand(ctx context.Context, command string) (string, error) {
	file, args := platform.PosixShellCommand(command, s.tools.Path("bash"))
	cmd := exec.CommandContext(ctx, file, args...)
	platform.PrepareCommand(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		if text := strings.TrimSpace(stderr.String()); text != "" {
			return text, err
		}
		return "", err
	}
	if text := stdout.String(); text != "" {
		return text, nil
	}
	return stderr.String(), nil
}

// syncSubscription dispatches on the subscription type.
func (s *SubscriptionService) syncSubscription(ctx context.Context, doc *subscriptionDoc, log *subscriptionLog, run *subscriptionRun) error {
	if doc.Type == "file" {
		return s.syncRaw(ctx, doc, log, run)
	}
	return s.syncRepo(ctx, doc, log, run)
}

// proxyEnv merges the configured proxy into the process environment. git and
// curl both honour http_proxy/https_proxy, which is how the shell engine does
// it (set_proxy/unset_proxy).
func (s *SubscriptionService) proxyEnv(proxy string) []string {
	env := os.Environ()
	value := strings.TrimSpace(proxy)
	if value == "" {
		value = strings.TrimSpace(s.cfg.Get("ProxyUrl"))
	}
	if value == "" {
		return env
	}
	return append(env, "http_proxy="+value, "https_proxy="+value,
		"HTTP_PROXY="+value, "HTTPS_PROXY="+value)
}

// execStreamed runs a command, streaming both pipes into the subscription log
// the way the Node service forwards child stdout/stderr.
func (s *SubscriptionService) execStreamed(ctx context.Context, log *subscriptionLog, run *subscriptionRun, dir string, env []string, name string, args ...string) (int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	platform.PrepareCommand(cmd)
	cmd.Dir = dir
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return -1, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return -1, err
	}
	if err := cmd.Start(); err != nil {
		return -1, err
	}
	if run != nil {
		run.setCommand(&execHandle{cmd: cmd})
		defer run.setCommand(nil)
	}

	var wg sync.WaitGroup
	stream := func(reader io.Reader) {
		defer wg.Done()
		buffer := make([]byte, 32*1024)
		for {
			read, readErr := reader.Read(buffer)
			if read > 0 {
				log.Write(string(buffer[:read]))
			}
			if readErr != nil {
				return
			}
		}
	}
	wg.Add(2)
	go stream(stdout)
	go stream(stderr)
	wg.Wait()

	err = cmd.Wait()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return -1, err
}

// syncRepo ports update_repo + diff_scripts.
func (s *SubscriptionService) syncRepo(ctx context.Context, doc *subscriptionDoc, log *subscriptionLog, run *subscriptionRun) error {
	url, _ := subscriptionURL(doc)
	uniq := uniqPath(url, doc.Branch)
	repoPath := filepath.Join(s.cfg.RepoPath, uniq)
	scriptsDir := filepath.Join(s.cfg.ScriptPath, uniq)

	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		return err
	}
	log.Write(i18n.Tf("开始拉取仓库 %s 到 %s\n", uniq, repoPath))

	if err := os.RemoveAll(repoPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	git := s.tools.Path("git")
	if git == "" {
		// The API rejects this before queueing; the guard covers a git binary
		// that disappeared while the panel was running.
		log.Write(i18n.T("未检测到 git，无法拉取仓库订阅，请先安装 git"))
		return nil
	}
	args := []string{"clone", "-q", "--depth=1"}
	if doc.Branch != "" {
		args = append(args, "-b", doc.Branch)
	}
	args = append(args, url, repoPath)
	code, err := s.execStreamed(ctx, log, run, s.cfg.RootPath, s.proxyEnv(doc.Proxy), git, args...)
	if err != nil {
		return err
	}
	if code != 0 {
		log.Write(i18n.Tf("拉取 %s 失败，请检查日志...\n", uniq))
		return nil
	}
	log.Write(i18n.Tf("拉取 %s 成功...\n", uniq))
	return s.diffScripts(ctx, doc, uniq, repoPath, scriptsDir, log)
}

// subscriptionExtensions resolves the extension list: the subscription value,
// then RepoFileExtensions, then the Go default (js py ts).
func (s *SubscriptionService) subscriptionExtensions(doc *subscriptionDoc) []string {
	raw := doc.Extensions
	if strings.TrimSpace(raw) == "" {
		raw = s.cfg.Get("RepoFileExtensions")
	}
	if strings.TrimSpace(raw) == "" {
		raw = "js py ts"
	}
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '|' || r == '\n' || r == ' ' || r == '\t' || r == '\r'
	})
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		result = append(result, strings.TrimPrefix(field, "."))
	}
	return result
}

// findFiles walks root in directory order (like `find`) and returns the
// matching files as slash separated paths relative to root.
func findFiles(root string, match func(name string) bool) ([]string, error) {
	var result []string
	var walk func(dir, prefix string) error
	walk = func(dir, prefix string) error {
		handle, err := os.Open(dir)
		if err != nil {
			return err
		}
		names, err := handle.Readdirnames(-1)
		handle.Close()
		if err != nil {
			return err
		}
		for _, name := range names {
			relative := prefix + name
			full := filepath.Join(dir, name)
			info, err := os.Lstat(full)
			if err != nil {
				continue
			}
			if info.IsDir() {
				if err := walk(full, relative+"/"); err != nil {
					continue
				}
				continue
			}
			if info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			if match(relative) {
				result = append(result, relative)
			}
		}
		return nil
	}
	if err := walk(root, ""); err != nil {
		return nil, err
	}
	return result, nil
}

// extensionMatcher ports `find . -name "*.ext" -o ...`.
func extensionMatcher(extensions []string) func(string) bool {
	return func(name string) bool {
		for _, extension := range extensions {
			if strings.HasSuffix(name, "."+extension) {
				return true
			}
		}
		return false
	}
}

// filterByRegex ports `egrep <pattern>` / `egrep -v <pattern>`.
func filterByRegex(files []string, pattern string, invert bool) []string {
	if strings.TrimSpace(pattern) == "" {
		return files
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		// grep would fail with a syntax error and the pipeline would return the
		// unfiltered input; keeping every file is the closest behaviour.
		return files
	}
	result := make([]string, 0, len(files))
	for _, file := range files {
		if re.MatchString(file) != invert {
			result = append(result, file)
		}
	}
	return result
}

// copyFile ports `cp -f <src> <dst>`, creating the parent directory.
func copyScriptFile(source, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	if err := os.Remove(destination); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// copyTree ports `cp -rf <src>/* <dst>`: regular files and directories only,
// symlinks are skipped so a repository cannot link outside the data directory.
func copyTree(source, destination string) error {
	entries, err := os.ReadDir(source)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		from := filepath.Join(source, entry.Name())
		to := filepath.Join(destination, entry.Name())
		info, err := os.Lstat(from)
		if err != nil {
			continue
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			continue
		case info.IsDir():
			if err := copyTree(from, to); err != nil {
				return err
			}
		default:
			if err := copyScriptFile(from, to); err != nil {
				return err
			}
		}
	}
	return nil
}

// diffScripts ports diff_scripts: generate the lists, then add/drop tasks.
func (s *SubscriptionService) diffScripts(ctx context.Context, doc *subscriptionDoc, uniq, repoPath, scriptsDir string, log *subscriptionLog) error {
	scriptsList, userList, err := s.genListRepo(doc, uniq, repoPath, scriptsDir, log)
	if err != nil {
		return err
	}
	listDir := filepath.Join(s.cfg.LogPath, ".tmp")
	if err := os.MkdirAll(listDir, 0o755); err != nil {
		return err
	}
	addPath := filepath.Join(listDir, uniq+"_add.list")
	dropPath := filepath.Join(listDir, uniq+"_drop.list")

	add, drop := diffCronLists(scriptsList, userList)
	if err := os.WriteFile(addPath, []byte(joinLines(add)), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(dropPath, []byte(joinLines(drop)), 0o644); err != nil {
		return err
	}

	if len(drop) > 0 {
		log.Write(i18n.Tf("检测到有%s的定时任务:", i18n.T("失效")) + "\n" + joinLines(drop))
		if doc.AutoDelCron {
			s.delCron(ctx, doc, uniq, drop, scriptsDir, log)
		}
	}
	if len(add) > 0 {
		log.Write(i18n.Tf("检测到有%s的定时任务:", i18n.T("新")) + "\n" + joinLines(add))
		if doc.AutoAddCron {
			s.addCron(ctx, doc, uniq, add, scriptsDir, log)
		}
	}
	return nil
}

// genListRepo ports gen_list_repo and returns the two lists diff_cron needs.
func (s *SubscriptionService) genListRepo(doc *subscriptionDoc, uniq, repoPath, scriptsDir string, log *subscriptionLog) ([]string, []string, error) {
	listDir := filepath.Join(s.cfg.LogPath, ".tmp")
	if err := os.MkdirAll(listDir, 0o755); err != nil {
		return nil, nil, err
	}
	// rm -f $dir_list_tmp/${uniq_path}*.list
	if entries, err := os.ReadDir(listDir); err == nil {
		for _, entry := range entries {
			name := entry.Name()
			if strings.HasPrefix(name, uniq) && strings.HasSuffix(name, ".list") {
				_ = os.Remove(filepath.Join(listDir, name))
			}
		}
	}

	extensions := s.subscriptionExtensions(doc)
	all, err := findFiles(repoPath, extensionMatcher(extensions))
	if err != nil {
		return nil, nil, err
	}
	selected := filterByRegex(all, doc.Whitelist, false)
	selected = filterByRegex(selected, doc.Blacklist, true)

	// cp -f $file_notify_js / $file_notify_py into the subscription directory.
	for _, name := range []string{"sendNotify.js", "notify.py"} {
		source := filepath.Join(s.cfg.ScriptPath, name)
		if info, err := os.Stat(source); err == nil && info.Mode().IsRegular() {
			if err := copyScriptFile(source, filepath.Join(scriptsDir, name)); err != nil {
				log.Write(err.Error())
			}
		}
	}

	if strings.TrimSpace(doc.Dependences) != "" {
		dependencies := filterByRegex(all, doc.Dependences, false)
		for _, file := range dependencies {
			if err := copyScriptFile(filepath.Join(repoPath, filepath.FromSlash(file)), filepath.Join(scriptsDir, filepath.FromSlash(file))); err != nil {
				log.Write(err.Error())
			}
		}
	}

	// cp -rf $dir_dep/* into the subscription directory.
	if err := copyTree(s.cfg.DepPath, scriptsDir); err != nil {
		log.Write(err.Error())
	}

	scripts := make([]string, 0, len(selected))
	for _, file := range selected {
		relative := uniq + "/" + file
		if err := copyScriptFile(filepath.Join(repoPath, filepath.FromSlash(file)), filepath.Join(scriptsDir, filepath.FromSlash(file))); err != nil {
			log.Write(err.Error())
			continue
		}
		scripts = append(scripts, relative)
	}
	if err := os.WriteFile(filepath.Join(listDir, uniq+"_scripts.list"), []byte(joinLines(scripts)), 0o644); err != nil {
		return nil, nil, err
	}

	users, err := s.userScriptList(doc, uniq)
	if err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(filepath.Join(listDir, uniq+"_user.list"), []byte(joinLines(users)), 0o644); err != nil {
		return nil, nil, err
	}
	return scripts, users, nil
}

// userScriptList ports the grep/perl pipeline that extracts the script paths of
// this subscription from crontab.list.
func (s *SubscriptionService) userScriptList(doc *subscriptionDoc, uniq string) ([]string, error) {
	content, err := os.ReadFile(filepath.Join(s.cfg.ConfigPath, "crontab.list"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		return nil, err
	}
	seen := map[string]struct{}{}
	result := make([]string, 0, 8)
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if !strings.Contains(line, "task") || !strings.Contains(line, " "+uniq) {
			continue
		}
		task := taskPathFromCommand(line)
		if task == "" || !strings.Contains(task, uniq) {
			continue
		}
		if _, ok := seen[task]; ok {
			continue
		}
		seen[task] = struct{}{}
		result = append(result, task)
	}
	sort.Strings(result)
	return result, nil
}

// taskPathFromCommand extracts the argument of `task <path>` from a
// crontab.list line, unquoting it when needed.
func taskPathFromCommand(line string) string {
	index := strings.Index(line, "task ")
	if index < 0 {
		return ""
	}
	rest := strings.TrimSpace(line[index+len("task "):])
	if rest == "" {
		return ""
	}
	if rest[0] == '\'' || rest[0] == '"' {
		quote := rest[0]
		if end := strings.IndexByte(rest[1:], quote); end >= 0 {
			value := rest[1 : 1+end]
			return strings.ReplaceAll(value, `'\''`, "'")
		}
		return ""
	}
	if fields := strings.Fields(rest); len(fields) > 0 {
		return fields[0]
	}
	return ""
}

// diffCronLists ports diff_cron: set difference in both directions, keeping the
// order of the source list.
func diffCronLists(scripts, users []string) (add []string, drop []string) {
	scriptSet := make(map[string]struct{}, len(scripts))
	for _, item := range scripts {
		scriptSet[item] = struct{}{}
	}
	userSet := make(map[string]struct{}, len(users))
	for _, item := range users {
		userSet[item] = struct{}{}
	}
	add = make([]string, 0, len(scripts))
	for _, item := range scripts {
		if _, ok := userSet[item]; !ok {
			add = append(add, item)
		}
	}
	drop = make([]string, 0, len(users))
	for _, item := range users {
		if _, ok := scriptSet[item]; !ok {
			drop = append(drop, item)
		}
	}
	return add, drop
}

func joinLines(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.Join(values, "\n") + "\n"
}

// addCron ports add_cron: extract a schedule and a name from each new script,
// then create the task with a `task <path>` command.
func (s *SubscriptionService) addCron(ctx context.Context, doc *subscriptionDoc, uniq string, files []string, scriptsDir string, log *subscriptionLog) {
	log.Write(i18n.T("\n开始尝试自动添加定时任务..."))
	fallback := s.defaultCronRule()
	for _, file := range files {
		fileName := strings.TrimPrefix(file, uniq+"/")
		fileName = strings.TrimPrefix(fileName, uniq+"_")
		full := filepath.Join(s.cfg.ScriptPath, filepath.FromSlash(file))
		info, err := os.Stat(full)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		content, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		meta := cronMetadata(string(content), file, fallback)
		command := "task " + file
		name := meta.Name
		created, err := s.crons.CreateFromAPI(ctx, &CrontabInput{
			Name:     &name,
			NameSet:  true,
			Command:  command,
			Schedule: meta.Schedule,
			SubID:    &doc.ID,
			SubIDSet: true,
		})
		if err != nil {
			log.Write(i18n.Tf("%s -> 添加失败(%s)", name, err.Error()))
			continue
		}
		_ = created
		log.Write(i18n.Tf("%s -> 添加成功", name))
	}
	s.notifyChange(ctx, i18n.Tf("%s 新增任务", uniq), strings.Join(files, "\n"))
}

// delCron ports del_cron: delete the rows of dropped scripts and the files.
func (s *SubscriptionService) delCron(ctx context.Context, doc *subscriptionDoc, uniq string, files []string, scriptsDir string, log *subscriptionLog) {
	log.Write(i18n.T("\n开始尝试自动删除失效的定时任务..."))
	details := make([]string, 0, len(files))
	ids := make([]int64, 0, len(files))

	rows, _, err := s.store.ListCrontabs(ctx, "sub_id = ?", []any{doc.ID}, store.DefaultCrontabOrder, 0, 0)
	if err != nil {
		log.Write(err.Error())
		return
	}
	for _, file := range files {
		for _, row := range rows {
			if taskPathFromCommand(row.Command) != file {
				continue
			}
			ids = append(ids, row.ID)
			break
		}
		name := ""
		full := filepath.Join(s.cfg.ScriptPath, filepath.FromSlash(file))
		if content, err := os.ReadFile(full); err == nil {
			name = cronMetadata(string(content), file, "").Name
			_ = os.Remove(full)
		}
		if name == "" {
			name = file
		}
		details = append(details, name)
	}
	if len(ids) > 0 {
		if err := s.crons.Remove(ctx, ids); err != nil {
			log.Write(i18n.Tf("失败(%s)", err.Error()))
		} else {
			log.Write(i18n.T("成功"))
		}
	}
	s.notifyChange(ctx, i18n.Tf("%s 删除任务%s", uniq, ""), strings.Join(details, "\n"))
}

// notifyChange ports notify_api: a best-effort notification, never fatal.
func (s *SubscriptionService) notifyChange(ctx context.Context, title, content string) {
	if s.notify == nil || strings.TrimSpace(title) == "" {
		return
	}
	reply := s.notify.Notify(ctx, NotificationRequest{Title: title, Content: content})
	if reply != nil && reply.Code != 200 {
		s.logger.Warn("[subscription] notify failed", "message", reply.Message)
	}
}

// defaultCronRule mirrors share.sh: DefaultCronRule when configured, otherwise
// a random minute/hour pair. Node randomises too, so a byte-identical
// crontab.list requires DefaultCronRule or a cron annotation in the script.
func (s *SubscriptionService) defaultCronRule() string {
	if rule := strings.TrimSpace(s.cfg.Get("DefaultCronRule")); rule != "" {
		return rule
	}
	now := time.Now()
	return fmt.Sprintf("%d %d * * *", now.Nanosecond()%60, now.Hour())
}

// cronMetadata ports cronMetadata() from the CLI engine, which is the readable
// reimplementation of add_cron's perl pipeline.
type cronMeta struct {
	Name     string
	Schedule string
}

var (
	newEnvPattern     = regexp.MustCompile(`new\s+Env\(\s*['"]([^'"\r\n]+)['"]`)
	nameLinePattern   = regexp.MustCompile(`(?m)^\s*(?:#\s*)?name:\s*(.+)$`)
	cronColonPattern  = regexp.MustCompile(`\bcron:\s*([^\r\n]+)`)
	cronQuotedPattern = regexp.MustCompile(`\bcron\s+["']([^"']+)["']`)
	cronExprPattern   = regexp.MustCompile(`(?:[\d*](?:[\d*/,-]*[\d*])?\s+){4,5}[\d*](?:[\d*/,-]*[\d*])?`)
)

func cronMetadata(contents, filename, fallback string) cronMeta {
	base := filepath.Base(filepath.FromSlash(filename))

	name := ""
	if match := newEnvPattern.FindStringSubmatch(contents); match != nil {
		name = match[1]
	}
	if name == "" {
		if match := nameLinePattern.FindStringSubmatch(contents); match != nil {
			name = strings.TrimSpace(match[1])
		}
	}
	if name == "" {
		name = base
	}

	explicit := ""
	if match := cronColonPattern.FindStringSubmatch(contents); match != nil {
		// `awk -F ":" '{print $2}' | xargs` strips the quoting around
		// `cron: "0 1 * * *"`, so the surrounding quotes must go too.
		explicit = strings.TrimSpace(match[1])
		explicit = strings.Trim(explicit, `"'`)
		explicit = strings.TrimSpace(strings.TrimRight(explicit, ","))
	}
	if explicit == "" {
		if match := cronQuotedPattern.FindStringSubmatch(contents); match != nil {
			explicit = match[1]
		}
	}

	// add_cron prefers a filename-bearing expression, deduplicated and sorted.
	matches := make([]string, 0, 4)
	for _, line := range strings.Split(contents, "\n") {
		if !strings.Contains(line, base) {
			continue
		}
		if expression := cronExprPattern.FindString(line); expression != "" {
			matches = append(matches, strings.TrimSpace(expression))
		}
	}
	sort.Strings(matches)
	unique := make([]string, 0, len(matches))
	for _, item := range matches {
		if item == "" || (len(unique) > 0 && unique[len(unique)-1] == item) {
			continue
		}
		unique = append(unique, item)
	}

	schedule := fallback
	if len(unique) > 0 {
		schedule = unique[0]
	} else if explicit != "" {
		schedule = explicit
	}
	return cronMeta{Name: strings.ReplaceAll(name, "\r", ""),
		Schedule: strings.ReplaceAll(schedule, "\r", "")}
}

// syncRaw ports update_raw: download the file, copy it into data/scripts and
// create a task when autoAddCron is on.
func (s *SubscriptionService) syncRaw(ctx context.Context, doc *subscriptionDoc, log *subscriptionLog, run *subscriptionRun) error {
	_ = run
	url, _ := subscriptionURL(doc)
	uniq := uniqPath(url, "")
	suffix := ""
	if index := strings.LastIndex(url, "."); index >= 0 {
		suffix = url[index+1:]
	}
	rawName := uniq + "." + suffix
	rawPath := filepath.Join(s.cfg.RawPath, rawName)
	log.Write(i18n.Tf("开始下载：%s 保存路径：%s\n", url, rawPath))

	if err := os.MkdirAll(s.cfg.RawPath, 0o755); err != nil {
		return err
	}
	if err := s.download(ctx, doc.Proxy, url, rawPath+".new"); err != nil {
		log.Write(i18n.Tf("下载 %s 失败，保留之前正常下载的版本...\n", rawName))
		_ = os.Remove(rawPath + ".new")
		return nil
	}
	if err := os.Rename(rawPath+".new", rawPath); err != nil {
		return err
	}
	log.Write(i18n.Tf("下载 %s 成功...\n", rawName))

	fileName := "raw_" + rawName
	target := filepath.Join(s.cfg.ScriptPath, fileName)
	if err := copyScriptFile(rawPath, target); err != nil {
		return err
	}
	if !doc.AutoAddCron {
		return nil
	}
	rows, _, err := s.store.ListCrontabs(ctx, "sub_id = ?", []any{doc.ID}, nil, 0, 0)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if taskPathFromCommand(row.Command) == fileName {
			return nil
		}
	}
	content, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	meta := cronMetadata(string(content), fileName, s.defaultCronRule())
	command := "task " + fileName
	name := meta.Name
	if _, err := s.crons.CreateFromAPI(ctx, &CrontabInput{
		Name:     &name,
		NameSet:  true,
		Command:  command,
		Schedule: meta.Schedule,
		SubID:    &doc.ID,
		SubIDSet: true,
	}); err != nil {
		log.Write(i18n.Tf("%s -> 添加失败(%s)", name, err.Error()))
		return nil
	}
	log.Write(i18n.Tf("%s -> 添加成功", name))
	s.notifyChange(ctx, i18n.T("新增任务通知"), "\n"+name)
	return nil
}

// download fetches url into destination, honouring the panel proxy.
func (s *SubscriptionService) download(ctx context.Context, proxy, source, destination string) error {
	transport := &http.Transport{}
	value := strings.TrimSpace(proxy)
	if value == "" {
		value = strings.TrimSpace(s.cfg.Get("ProxyUrl"))
	}
	if value != "" {
		if parsed, err := url.Parse(value); err == nil {
			transport.Proxy = http.ProxyURL(parsed)
		}
	} else {
		transport.Proxy = http.ProxyFromEnvironment
	}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Minute}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("download %s: %s", source, response.Status)
	}
	file, err := os.Create(destination)
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, response.Body); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// ValidateSubscriptionSchedule mirrors the `param schedule error` branch.
func ValidateSubscriptionSchedule(schedule string) bool {
	if strings.TrimSpace(schedule) == "" {
		return true
	}
	return cronexpr.IsValid(schedule)
}
