package runner

import "testing"

// The settings page owns the notification channel; config.sh and the
// environment page are explicit operator choices. A saved channel must fill the
// blanks only, and an empty placeholder (`export BARK_PUSH=""`) must not block.
func TestApplyEnvSourcePrecedence(t *testing.T) {
	r := &Runner{}
	env := newEnvList(nil)
	env.Set("BARK_PUSH", "from-config")
	env.Set("WEBHOOK_URL", "")

	r.SetEnvSource(func() map[string]string {
		return map[string]string{
			"BARK_PUSH":   "from-panel",
			"WEBHOOK_URL": "https://example.test/hook",
			"BARK_GROUP":  "qinglong",
			"EMPTY_ONE":   "",
		}
	})
	r.applyEnvSource(env)

	if got := env.Get("BARK_PUSH"); got != "from-config" {
		t.Errorf("BARK_PUSH = %q, want the config.sh value", got)
	}
	if got := env.Get("WEBHOOK_URL"); got != "https://example.test/hook" {
		t.Errorf("WEBHOOK_URL = %q, want the panel value", got)
	}
	if got := env.Get("BARK_GROUP"); got != "qinglong" {
		t.Errorf("BARK_GROUP = %q, want qinglong", got)
	}
	if env.Has("EMPTY_ONE") {
		t.Error("an empty panel value must not become an environment variable")
	}
}

// A nil source (unit tests, the CLI) must leave the environment untouched.
func TestApplyEnvSourceWithoutSource(t *testing.T) {
	r := &Runner{}
	env := newEnvList(nil)
	env.Set("BARK_PUSH", "x")
	r.applyEnvSource(env)
	if got := env.Get("BARK_PUSH"); got != "x" {
		t.Errorf("BARK_PUSH = %q, want x", got)
	}
}
