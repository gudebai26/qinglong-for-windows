package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"qinglong-go/internal/i18n"
)

// This file ports back/services/sshKey.ts. A private repository pulled over SSH
// needs a per-subscription key plus an SSH config entry, and Node wires those
// into ~/.ssh/config through an Include directive. The Go panel writes the same
// files so a data directory can move between the two implementations.

var (
	sshHostPattern  = regexp.MustCompile(`^[a-zA-Z0-9.-]+$`)
	sshProxyPattern = regexp.MustCompile(`^[a-zA-Z0-9.:\[\]-]+$`)
)

// sshConfigPath is ~/.ssh/config. Node reads os.userInfo().homedir, which is
// what os.UserHomeDir returns on both platforms for the panel user.
func sshConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("cannot resolve the user home directory")
	}
	return filepath.Join(home, ".ssh", "config"), nil
}

// sshConfigHeader is the Include line Node prepends once.
func (s *SubscriptionService) sshConfigHeader() string {
	return "Include " + filepath.Join(s.cfg.SshdPath, "*.config")
}

// initSSHConfigFile creates ~/.ssh/config and makes sure it includes the
// panel-managed directory.
func (s *SubscriptionService) initSSHConfigFile() error {
	target, err := sshConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	content := ""
	if existing, err := os.ReadFile(target); err == nil {
		content = string(existing)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else if err := os.WriteFile(target, nil, 0o600); err != nil {
		return err
	}
	header := s.sshConfigHeader()
	if strings.Contains(content, header) {
		return nil
	}
	return os.WriteFile(target, []byte(header+"\n\n"+content), 0o600)
}

// resolveSubscriptionPath ports resolveSubscriptionPath.
func (s *SubscriptionService) resolveSubscriptionPath(alias, suffix string) (string, error) {
	if !regexp.MustCompile(`^~sub_[a-f0-9]{64}$`).MatchString(alias) {
		checked := alias
		if strings.HasPrefix(checked, "~global_") {
			checked = checked[len("~global_"):]
		}
		if err := validateSubscriptionAlias(checked); err != nil {
			return "", err
		}
	}
	resolved := resolveFileAccess(s.cfg.SshdPath, []string{alias + suffix}, nil)
	if resolved == "" {
		return "", errors.New("Invalid subscription path")
	}
	return resolved, nil
}

func (s *SubscriptionService) writeSSHFile(alias, suffix, content string, mode os.FileMode) error {
	target, err := s.resolveSubscriptionPath(alias, suffix)
	if err != nil {
		return err
	}
	// Node removes the previous file first: private keys are 0400, so an
	// in-place rewrite of an existing key fails with EACCES.
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.WriteFile(target, []byte(content), mode)
}

func (s *SubscriptionService) removeSSHFile(alias, suffix string) {
	target, err := s.resolveSubscriptionPath(alias, suffix)
	if err != nil {
		return
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.logger.Warn("[subscription] remove ssh file failed", "path", target, "error", err)
	}
}

// generatePrivateKeyFile writes the key with a trailing newline (Node appends
// os.EOL) and 0400 permissions.
func (s *SubscriptionService) generatePrivateKeyFile(alias, key string) error {
	return s.writeSSHFile(alias, "", key+"\n", 0o400)
}

// generateSingleSSHConfig writes <data>/ssh.d/<alias>.config.
func (s *SubscriptionService) generateSingleSSHConfig(alias, host, proxy string) error {
	if !sshHostPattern.MatchString(host) || (proxy != "" && !sshProxyPattern.MatchString(proxy)) {
		return &NotFoundError{Status: 400, Message: "Invalid SSH host or proxy"}
	}
	if host == "github.com" {
		host = "ssh.github.com\n    Port 443\n    HostkeyAlgorithms +ssh-rsa"
	}
	proxyLine := ""
	if proxy != "" {
		proxyLine = "    ProxyCommand nc -v -x " + proxy + " %h %p 2>/dev/null\n"
	}
	keyPath, err := s.resolveSubscriptionPath(alias, "")
	if err != nil {
		return err
	}
	config := fmt.Sprintf("Host %s\n    Hostname %s\n    IdentityFile %s\n    StrictHostKeyChecking no\n%s",
		alias, host, keyPath, proxyLine)
	return s.writeSSHFile(alias, ".config", config, 0o600)
}

// SetSSHConfig replays setSshConfig for every stored subscription.
func (s *SubscriptionService) SetSSHConfig(ctx context.Context) error {
	if err := s.initSSHConfigFile(); err != nil {
		return err
	}
	rows, err := s.store.AllSubscriptions(ctx)
	if err != nil {
		return err
	}
	for i := range rows {
		doc := toDoc(&rows[i])
		if doc.Type != "private-repo" || doc.PullType != "ssh-key" {
			continue
		}
		alias, err := subscriptionSshAlias(doc.Alias)
		if err != nil {
			s.logger.Warn("[subscription] skip invalid ssh config", "id", doc.ID, "error", err)
			continue
		}
		_, host := subscriptionURL(doc)
		key := doc.PullOption["private_key"]
		s.removeSSHFile(alias, "")
		s.removeSSHFile(alias, ".config")
		if err := s.generatePrivateKeyFile(alias, key); err != nil {
			s.logger.Warn("[subscription] write private key failed", "id", doc.ID, "error", err)
			continue
		}
		if err := s.generateSingleSSHConfig(alias, host, doc.Proxy); err != nil {
			s.logger.Warn(i18n.T("跳过订阅 %s 的无效 SSH 配置: %s"), "id", doc.ID, "error", err)
		}
	}
	return nil
}

// removeSshConfigForSubscription drops the key and config of a subscription
// that is being updated or deleted.
func (s *SubscriptionService) removeSshConfigForSubscription(doc *subscriptionDoc) {
	if doc.Type != "private-repo" || doc.PullType != "ssh-key" {
		return
	}
	alias, err := subscriptionSshAlias(doc.Alias)
	if err != nil {
		return
	}
	s.removeSSHFile(alias, "")
	s.removeSSHFile(alias, ".config")
}

// AddGlobalSSHKey installs the "use this key for every host" entry the settings
// page writes (back/services/sshKey.ts addGlobalSSHKey).
func (s *SubscriptionService) AddGlobalSSHKey(key, alias string) error {
	name := "~global_" + alias
	if err := s.generatePrivateKeyFile(name, key); err != nil {
		return err
	}
	target, err := s.resolveSubscriptionPath(name, ".config")
	if err != nil {
		return err
	}
	identity, err := s.resolveSubscriptionPath(name, "")
	if err != nil {
		return err
	}
	config := "Host *\n    IdentityFile " + filepath.ToSlash(identity) +
		"\n    StrictHostKeyChecking no\n"
	return os.WriteFile(target, []byte(config), 0o600)
}

// RemoveGlobalSSHKey drops the private key and its wildcard host block.
func (s *SubscriptionService) RemoveGlobalSSHKey(alias string) {
	name := "~global_" + alias
	s.removeSSHFile(name, "")
	s.removeSSHFile(name, ".config")
}
