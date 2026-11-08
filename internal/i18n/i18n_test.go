package i18n

import "testing"

// The task log lines come from shell/lang/en.sh, not from back/shared/i18n.ts:
// the shell wrapper prints them, so the English build must use the shell's
// wording or a Go panel would read differently from a Node one.
func TestRunnerLogTranslationsMatchShellLang(t *testing.T) {
	SetLang("en")
	defer SetLang("zh")

	cases := []struct {
		key  string
		want string
	}{
		{"## 开始执行... %s\n\n", "## Starting... %s\n\n"},
		{
			"任务随机延迟 %s 秒，将于 %s 开始，配置文件参数 RandomDelay 置空可取消延迟\n",
			"Task delayed %s seconds, will start at %s. Set RandomDelay to empty to cancel delay\n",
		},
		{"执行前置命令\n", "Running pre-command\n"},
		{"\n执行前置命令结束\n", "\nPre-command finished\n"},
		{"\n执行后置命令\n", "\nRunning post-command\n"},
		{"\n执行后置命令结束", "\nPost-command finished"},
	}
	for _, item := range cases {
		if got := T(item.key); got != item.want {
			t.Errorf("T(%q) = %q, want %q", item.key, got, item.want)
		}
	}

	if got := Tf("任务随机延迟 %s 秒，将于 %s 开始，配置文件参数 RandomDelay 置空可取消延迟\n",
		284, "2026-11-08 19:34:44"); got != "Task delayed 284 seconds, will start at 2026-11-08 19:34:44. Set RandomDelay to empty to cancel delay\n" {
		t.Errorf("delay message = %q", got)
	}
}

// Chinese is the fallback: the key is returned unchanged, exactly like the
// Node dictionary.
func TestChineseReturnsKey(t *testing.T) {
	SetLang("zh")
	key := "任务随机延迟 %s 秒，将于 %s 开始，配置文件参数 RandomDelay 置空可取消延迟\n"
	if got := T(key); got != key {
		t.Errorf("T(key) = %q, want the key itself", got)
	}
	if got := Tf(key, 284, "2026-11-08 19:34:44"); got != "任务随机延迟 284 秒，将于 2026-11-08 19:34:44 开始，配置文件参数 RandomDelay 置空可取消延迟\n" {
		t.Errorf("delay message = %q", got)
	}
}
