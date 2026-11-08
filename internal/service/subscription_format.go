package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// This file ports the subscription naming rules of shell/update.sh and
// back/config/subscription.ts. They decide where a repository lands on disk, so
// they have to match the Node panel byte for byte or a Go run would leave the
// scripts of an existing subscription in a second directory.

// subscriptionAliasMaxLen mirrors assertSubscriptionAlias.
const subscriptionAliasMaxLen = 200

// subscriptionSshAliasPattern is the "safe enough to use as an SSH host alias"
// test in getSubscriptionSshAlias.
var subscriptionSshAliasPattern = regexp.MustCompile(`^[\p{L}\p{N}_.+-]+$`)

// validateSubscriptionAlias ports assertSubscriptionAlias: no whitespace, no
// backslash, no control characters, no path traversal and no leading "~".
func validateSubscriptionAlias(alias string) error {
	if alias == "" || len(alias) > subscriptionAliasMaxLen {
		return errors.New("Invalid subscription alias")
	}
	if strings.HasPrefix(alias, "~") {
		return errors.New("Invalid subscription alias")
	}
	for _, r := range alias {
		if unicode.IsSpace(r) || r == '\\' || unicode.IsControl(r) {
			return errors.New("Invalid subscription alias")
		}
	}
	for _, part := range strings.Split(alias, "/") {
		if part == "" || part == "." || part == ".." {
			return errors.New("Invalid subscription alias")
		}
	}
	return nil
}

// subscriptionSshAlias ports getSubscriptionSshAlias: ordinary SSH names stay
// readable, path-like aliases move into a reserved hashed namespace.
func subscriptionSshAlias(alias string) (string, error) {
	if err := validateSubscriptionAlias(alias); err != nil {
		return "", err
	}
	if subscriptionSshAliasPattern.MatchString(alias) && !strings.HasSuffix(alias, ".config") {
		return alias, nil
	}
	sum := sha256.Sum256([]byte(alias))
	return "~sub_" + hex.EncodeToString(sum[:]), nil
}

// shellQuote ports back/shared/shellQuote.ts.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// subscriptionURL port of formatUrl. host is empty unless the URL had to be
// rewritten for a private repository.
func subscriptionURL(doc *subscriptionDoc) (url string, host string) {
	url = doc.URL
	if doc.Type != "private-repo" {
		return url, ""
	}
	switch doc.PullType {
	case "ssh-key":
		host = regexReplaceFirst(url, `.*@([^:]+):.*`, "$1")
		alias, err := subscriptionSshAlias(doc.Alias)
		if err != nil {
			return url, host
		}
		return strings.Replace(url, host, alias, 1), host
	case "user-pwd":
		host = regexReplaceFirst(url, `.*://([^/]+)/.*`, "$1")
		username := doc.PullOption["username"]
		password := doc.PullOption["password"]
		return strings.Replace(url, host, username+":"+password+"@"+host, 1), host
	default:
		return url, ""
	}
}

// regexReplaceFirst mirrors JS String.replace(/re/, "$1"): no match means the
// input is returned unchanged.
func regexReplaceFirst(value, pattern, replacement string) string {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return value
	}
	location := re.FindStringSubmatchIndex(value)
	if location == nil {
		return value
	}
	expanded := re.ExpandString(nil, replacement, value, location)
	return string(expanded)
}

// uniqPath ports get_uniq_path: "<author>_<repo>[_<branch>]".
func uniqPath(url, branch string) string {
	urlTmp := strings.TrimSuffix(url, "/")
	repoTmp := urlTmp
	if index := strings.LastIndex(urlTmp, "/"); index >= 0 {
		repoTmp = urlTmp[index+1:]
	}
	repo := repoTmp
	if index := strings.LastIndex(repoTmp, "."); index >= 0 {
		repo = repoTmp[:index]
	}

	parent := url
	if index := strings.LastIndex(url, "/"); index >= 0 {
		parent = url[:index]
	}
	authorTmp1 := parent
	if index := strings.LastIndex(parent, "/"); index >= 0 {
		authorTmp1 = parent[index+1:]
	}
	authorTmp2 := authorTmp1
	if index := strings.LastIndex(authorTmp1, ":"); index >= 0 {
		authorTmp2 = authorTmp1[index+1:]
	}
	author := authorTmp2
	if index := strings.LastIndex(authorTmp2, "."); index >= 0 {
		author = authorTmp2[index+1:]
	}

	result := author + "_" + repo
	if branch != "" {
		result = result + "_" + branch
	}
	return result
}

// subscriptionStorageNames ports getSubscriptionStorageNames for the shell
// engine (the only one Go implements). It is used by DELETE ?force=true to know
// which directories belong to a subscription.
func subscriptionStorageNames(doc *subscriptionDoc) (script, repo, raw string) {
	if doc.URL == "" {
		return doc.Alias, doc.Alias, ""
	}
	url, _ := subscriptionURL(doc)
	trimmed := strings.TrimSuffix(url, "/")
	basename := trimmed
	if index := strings.LastIndex(trimmed, "/"); index >= 0 {
		basename = trimmed[index+1:]
	}
	repoName := basename
	if index := strings.LastIndex(basename, "."); index >= 0 {
		repoName = basename[:index]
	}
	parent := url
	if index := strings.LastIndex(url, "/"); index >= 0 {
		parent = url[:index]
	}
	owner := parent
	if index := strings.LastIndex(parent, "/"); index >= 0 {
		owner = parent[index+1:]
	}
	author := owner
	if index := strings.LastIndex(owner, ":"); index >= 0 {
		author = owner[index+1:]
	}
	if index := strings.LastIndex(author, "."); index >= 0 {
		author = author[index+1:]
	}
	stem := author + "_" + repoName
	if doc.Type == "file" {
		suffix := ""
		if index := strings.LastIndex(url, "."); index >= 0 {
			suffix = "." + url[index+1:]
		}
		rawName := stem + suffix
		return "raw_" + rawName, "", rawName
	}
	directory := stem
	if doc.Branch != "" {
		directory = stem + "_" + doc.Branch
	}
	return directory, directory, ""
}

// isSubscriptionStorageName ports isSubscriptionStorageName: a relative,
// non-empty name whose segments are all ordinary.
func isSubscriptionStorageName(name string) bool {
	if name == "" || path.IsAbs(name) || filepath.IsAbs(name) {
		return false
	}
	if strings.ContainsAny(name, "\x00\r\n\\") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// subscriptionCommand ports formatCommand: the `ql repo`/`ql raw` command line
// the Node panel stores on the plain object it returns to the client. The
// column itself stays NULL (the create schema has no `command` field), but the
// create/update responses carry it, so the value has to match.
func subscriptionCommand(doc *subscriptionDoc) string {
	url, _ := subscriptionURL(doc)
	args := make([]string, 0, 12)
	if doc.Type == "file" {
		args = append(args, "raw", url, doc.Proxy)
	} else {
		args = append(args,
			"repo", url, doc.Whitelist, doc.Blacklist, doc.Dependences,
			doc.Branch, doc.Extensions, doc.Proxy)
	}
	args = append(args, boolString(doc.AutoAddCron), boolString(doc.AutoDelCron))

	quoted := make([]string, 0, len(args)+2)
	quoted = append(quoted, "SUB_ID="+shellQuote(strconv.FormatInt(doc.ID, 10)), "ql")
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
