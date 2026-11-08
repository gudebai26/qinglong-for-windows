package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qinglong-go/internal/config"
)

func TestUniqPathMatchesShell(t *testing.T) {
	cases := []struct {
		url    string
		branch string
		want   string
	}{
		{"https://github.com/whyour/qinglong.git", "", "whyour_qinglong"},
		{"https://github.com/whyour/qinglong", "", "whyour_qinglong"},
		// The shell trims one trailing slash for the repo name but keeps it for
		// the owner, so a trailing slash yields "qinglong_qinglong". Verified
		// against shell/update.sh, not guessed.
		{"https://github.com/whyour/qinglong/", "", "qinglong_qinglong"},
		{"https://github.com/whyour/qinglong.git", "dev", "whyour_qinglong_dev"},
		{"git@github.com:whyour/qinglong.git", "", "whyour_qinglong"},
		{"git@github.com:whyour/qinglong.git", "main", "whyour_qinglong_main"},
		// A colon before the owner (https://host:443/owner/repo) is stripped,
		// and the last dot of the owner is dropped, exactly like the shell.
		{"https://github.com:443/whyour/qinglong.git", "", "whyour_qinglong"},
		{"https://example.com/user.name/repo.git", "", "name_repo"},
		// Local file URLs (used by the M7 acceptance test).
		{"file:///C:/ql-m7/repo-src", "", "ql-m7_repo-src"},
	}
	for _, testCase := range cases {
		if got := uniqPath(testCase.url, testCase.branch); got != testCase.want {
			t.Errorf("uniqPath(%q,%q) = %q, want %q", testCase.url, testCase.branch, got, testCase.want)
		}
	}
}

func TestCronMetadataPrefersCronAnnotation(t *testing.T) {
	cases := []struct {
		name     string
		contents string
		filename string
		wantName string
		wantCron string
	}{
		{
			name:     "double quoted annotation",
			contents: "// cron: \"10 6 * * *\"\nconsole.log('a');\n",
			filename: "ql-m7_repo-src/sub/a.js",
			wantName: "a.js",
			wantCron: "10 6 * * *",
		},
		{
			name:     "hash comment annotation",
			contents: "# cron: \"20 7 * * *\"\nprint('b')\n",
			filename: "sub/b.py",
			wantName: "b.py",
			wantCron: "20 7 * * *",
		},
		{
			name:     "no annotation falls back",
			contents: "console.log('c');\n",
			filename: "sub/c.ts",
			wantName: "c.ts",
			wantCron: "0 3 * * *",
		},
		{
			name:     "new Env wins over the file name",
			contents: "new Env('任务A');\n// cron: \"0 5 * * *\"\n",
			filename: "sub/x.js",
			wantName: "任务A",
			wantCron: "0 5 * * *",
		},
		{
			name:     "filename bearing expression wins",
			contents: "0 0,12 * * * sub/y.js\n",
			filename: "sub/y.js",
			wantName: "y.js",
			wantCron: "0 0,12 * * *",
		},
		{
			name:     "cron quoted form",
			contents: "cron \"30 9 * * 1\"\n",
			filename: "z.js",
			wantName: "z.js",
			wantCron: "30 9 * * 1",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			meta := cronMetadata(testCase.contents, testCase.filename, "0 3 * * *")
			if meta.Name != testCase.wantName {
				t.Errorf("name = %q, want %q", meta.Name, testCase.wantName)
			}
			if meta.Schedule != testCase.wantCron {
				t.Errorf("schedule = %q, want %q", meta.Schedule, testCase.wantCron)
			}
		})
	}
}

func TestDiffCronLists(t *testing.T) {
	add, drop := diffCronLists(
		[]string{"repo/a.js", "repo/b.js"},
		[]string{"repo/b.js", "repo/old.js"},
	)
	if strings.Join(add, ",") != "repo/a.js" {
		t.Errorf("add = %v", add)
	}
	if strings.Join(drop, ",") != "repo/old.js" {
		t.Errorf("drop = %v", drop)
	}

	// Empty task list: every script is new.
	add, drop = diffCronLists([]string{"repo/a.js"}, nil)
	if len(add) != 1 || len(drop) != 0 {
		t.Errorf("add=%v drop=%v", add, drop)
	}

	// Empty script list: every task is stale.
	add, drop = diffCronLists(nil, []string{"repo/a.js"})
	if len(add) != 0 || len(drop) != 1 {
		t.Errorf("add=%v drop=%v", add, drop)
	}
}

func TestFilterByRegexAndExtensions(t *testing.T) {
	files := []string{"sub/a.js", "sub/b.py", "sub/c.ts", "black/x.js", "lib/requirements.txt"}
	matched := filterByRegex(files, "black", false)
	if strings.Join(matched, ",") != "black/x.js" {
		t.Errorf("include filter = %v", matched)
	}
	kept := filterByRegex(files, "black", true)
	if strings.Join(kept, ",") != "sub/a.js,sub/b.py,sub/c.ts,lib/requirements.txt" {
		t.Errorf("exclude filter = %v", kept)
	}
	// An invalid pattern keeps the input, like grep failing open in the shell.
	if got := filterByRegex(files, "(", false); len(got) != len(files) {
		t.Errorf("invalid pattern = %v", got)
	}

	matcher := extensionMatcher([]string{"js", "ts"})
	selected := make([]string, 0, len(files))
	for _, file := range files {
		if matcher(file) {
			selected = append(selected, file)
		}
	}
	if strings.Join(selected, ",") != "sub/a.js,sub/c.ts,black/x.js" {
		t.Errorf("extension filter = %v", selected)
	}
}

func TestSubscriptionStorageNames(t *testing.T) {
	repo := &subscriptionDoc{URL: "https://github.com/whyour/qinglong.git", Type: "public-repo"}
	script, repoName, raw := subscriptionStorageNames(repo)
	if script != "whyour_qinglong" || repoName != "whyour_qinglong" || raw != "" {
		t.Errorf("repo names = %q %q %q", script, repoName, raw)
	}

	branch := &subscriptionDoc{URL: "https://github.com/whyour/qinglong.git", Type: "public-repo", Branch: "dev"}
	if script, _, _ := subscriptionStorageNames(branch); script != "whyour_qinglong_dev" {
		t.Errorf("branch script dir = %q", script)
	}

	file := &subscriptionDoc{URL: "https://example.com/a/b/user.js", Type: "file"}
	script, repoName, raw = subscriptionStorageNames(file)
	if script != "raw_b_user.js" || repoName != "" || raw != "b_user.js" {
		t.Errorf("raw names = %q %q %q", script, repoName, raw)
	}

	legacy := &subscriptionDoc{Alias: "legacy"}
	if script, _, _ := subscriptionStorageNames(legacy); script != "legacy" {
		t.Errorf("alias fallback = %q", script)
	}
}

func TestValidateSubscriptionAlias(t *testing.T) {
	valid := []string{"qinglong", "my repo", "a/b", "仓库", "a.b-c_d+e"}
	// "my repo" contains a space and must be rejected like Node does.
	valid = valid[:1]
	for _, alias := range valid {
		if err := validateSubscriptionAlias(alias); err != nil {
			t.Errorf("alias %q rejected: %v", alias, err)
		}
	}
	invalid := []string{"", "my repo", `a\b`, "~home", "a//b", "a/../b", "./a", strings.Repeat("a", 201)}
	for _, alias := range invalid {
		if err := validateSubscriptionAlias(alias); err == nil {
			t.Errorf("alias %q accepted", alias)
		}
	}

	if alias, err := subscriptionSshAlias("plain"); err != nil || alias != "plain" {
		t.Errorf("plain ssh alias = %q %v", alias, err)
	}
	hashed, err := subscriptionSshAlias("a/b")
	if err != nil || !strings.HasPrefix(hashed, "~sub_") || len(hashed) != len("~sub_")+64 {
		t.Errorf("hashed ssh alias = %q %v", hashed, err)
	}
}

func TestSubscriptionURLRewritesPrivateRepos(t *testing.T) {
	ssh := &subscriptionDoc{
		URL: "git@github.com:whyour/qinglong.git", Type: "private-repo",
		PullType: "ssh-key", Alias: "qinglong",
	}
	url, host := subscriptionURL(ssh)
	if host != "github.com" || url != "git@qinglong:whyour/qinglong.git" {
		t.Errorf("ssh url = %q host = %q", url, host)
	}

	password := &subscriptionDoc{
		URL: "https://github.com/whyour/qinglong.git", Type: "private-repo",
		PullType: "user-pwd", PullOption: map[string]string{"username": "u", "password": "p"},
	}
	url, host = subscriptionURL(password)
	if host != "github.com" || url != "https://u:p@github.com/whyour/qinglong.git" {
		t.Errorf("password url = %q host = %q", url, host)
	}
}

func TestSubscriptionLogWritesNodeFormat(t *testing.T) {
	root := t.TempDir()
	logger, err := openSubscriptionLog(root, "alias/2026-11-07-10-00-00.log", testTime())
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	logger.Write("拉取 repo 成功...\n")
	logger.Close(testTime().Add(3_000_000_000), testTime())

	content, err := os.ReadFile(filepath.Join(root, "alias", "2026-11-07-10-00-00.log"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	text := string(content)
	if !strings.HasPrefix(text, "## 开始执行... ") {
		t.Errorf("missing header: %q", text)
	}
	if !strings.Contains(text, "\n拉取 repo 成功...\n") {
		t.Errorf("chunk not prefixed with a newline: %q", text)
	}
	if !strings.HasSuffix(text, "耗时 3 秒"+LogEndSymbol) {
		t.Errorf("footer = %q", text)
	}
	if strings.Contains(text, "## 执行结束... ") == false {
		t.Errorf("missing footer marker: %q", text)
	}
}

func TestOpenSubscriptionLogRejectsEscapingPath(t *testing.T) {
	root := t.TempDir()
	if _, err := openSubscriptionLog(root, "../escape.log", testTime()); err == nil {
		t.Fatal("expected the path to be rejected")
	}
}

func TestSubscriptionExtensionsPrecedence(t *testing.T) {
	cfg := &config.Config{Values: map[string]string{}}
	service := &SubscriptionService{cfg: cfg}

	if got := service.subscriptionExtensions(&subscriptionDoc{}); strings.Join(got, ",") != "js,py,ts" {
		t.Errorf("default extensions = %v", got)
	}
	cfg.Values["RepoFileExtensions"] = "js mjs py pyc"
	if got := service.subscriptionExtensions(&subscriptionDoc{}); strings.Join(got, ",") != "js,mjs,py,pyc" {
		t.Errorf("config extensions = %v", got)
	}
	if got := service.subscriptionExtensions(&subscriptionDoc{Extensions: "js|ts"}); strings.Join(got, ",") != "js,ts" {
		t.Errorf("subscription extensions = %v", got)
	}
}

func TestValidateSubscriptionSchedule(t *testing.T) {
	if !ValidateSubscriptionSchedule("") {
		t.Error("empty schedule must be accepted")
	}
	if !ValidateSubscriptionSchedule("0 0 1 * *") {
		t.Error("valid schedule rejected")
	}
	if ValidateSubscriptionSchedule("not a cron") {
		t.Error("invalid schedule accepted")
	}
}

func testTime() time.Time {
	return time.Date(2026, 11, 7, 10, 0, 0, 0, time.Local)
}
