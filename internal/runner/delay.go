package runner

import (
	"math/rand"
	"strconv"
	"strings"
	"time"
)

// randomDelay ports random_delay() from shell/otask.sh. It returns the delay to
// wait and the message to log (empty when no delay applies).
//
// The delay only applies to a plain `task <script>` run: an explicit `now`,
// `conc`, `desi`, an extra argument, or a manual run from the panel (which sets
// no_delay) all start immediately.
func (r *Runner) randomDelay(plan *Plan, now time.Time) (time.Duration, string) {
	maxSeconds := r.cfg.GetInt("RandomDelay", 0)
	if maxSeconds <= 0 {
		return 0, ""
	}
	if len(plan.ArgsAll) != 1 {
		return 0, ""
	}
	if plan.Env["real_time"] == "true" || plan.Env["no_delay"] == "true" {
		return 0, ""
	}

	extensions := r.cfg.GetDefault("RandomDelayFileExtensions", "js")
	if extensions != "" {
		matched := false
		for _, ext := range strings.Fields(extensions) {
			if strings.HasSuffix(plan.FileParam, "."+ext) {
				matched = true
				break
			}
		}
		if !matched {
			return 0, ""
		}
	}

	ignored := r.cfg.GetDefault("RandomDelayIgnoredMinutes", "0 30")
	minute := now.Minute()
	for _, item := range strings.Fields(ignored) {
		if parsed, err := strconv.Atoi(item); err == nil && parsed == minute {
			return 0, ""
		}
	}

	seconds := rand.Intn(maxSeconds) + 1
	startAt := now.Add(time.Duration(seconds) * time.Second)
	message := "任务随机延迟 " + itoa(seconds) + " 秒，将于 " +
		startAt.Format("2006-01-02 15:04:05") +
		" 开始，配置文件参数 RandomDelay 置空可取消延迟\n\n"
	return time.Duration(seconds) * time.Second, message
}
