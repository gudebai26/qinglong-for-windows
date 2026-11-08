package service

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"qinglong-go/internal/cronexpr"
)

var importLinePattern = regexp.MustCompile(`^((\@[a-zA-Z]+\s+)|(([^\s]+)\s+([^\s]+)\s+([^\s]+)\s+([^\s]+)\s+([^\s]+)\s+))`)

// ImportCrontab is the best-effort port of CronService.importCrontab. The Node
// handler returns before `crontab -l` completes; on Windows the binary is
// absent, so this is a no-op there.
func (s *CronService) ImportCrontab() {
	output, err := exec.Command("crontab", "-l").Output()
	if err != nil {
		return
	}
	ctx := context.Background()
	lines := strings.Split(string(output), "\n")
	prefix := time.Now().UnixMilli()

	for reverseIndex := len(lines) - 1; reverseIndex >= 0; reverseIndex-- {
		line := strings.ReplaceAll(lines[reverseIndex], "\t", " ")
		command := strings.TrimSpace(importLinePattern.ReplaceAllString(line, ""))
		schedule := strings.TrimSpace(strings.Replace(line, command, "", 1))
		if command == "" || schedule == "" || !cronexpr.IsValid(schedule) {
			continue
		}
		name := strconv.FormatInt(prefix, 10) + "_" + strconv.FormatInt(int64(reverseIndex), 10)
		existing, err := s.store.CrontabByCommandSchedule(ctx, command, schedule)
		if err != nil {
			continue
		}
		if existing == nil {
			_, _ = s.Create(ctx, &CrontabInput{
				Name:     &name,
				NameSet:  true,
				Command:  command,
				Schedule: schedule,
			})
			continue
		}
		// Keep the existing row, only refresh the two imported fields.
		_, _ = s.store.UpdateCrontabs(ctx, []int64{existing.ID}, map[string]any{
			"command":   command,
			"schedule":  schedule,
			"timestamp": jsDateString(time.Now()),
			"saved":     0,
		})
		_ = s.RefreshCrontab(ctx)
	}
}
