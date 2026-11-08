package service

import "strings"

// reservedTaskEnvNames ports back/shared/taskEnvName.ts. These names drive the
// task runner or the interpreters it starts, so values managed through the
// environment-variable API must never overwrite them.
var reservedTaskEnvNames = map[string]struct{}{
	"TASK_BEFORE": {}, "TASK_AFTER": {}, "WORK_DIR": {}, "TASK_SHELL_PARAMS": {},
	"SCRIPT_PARAMS": {}, "WHICH_PROGRAM": {}, "ISJSORPYTHONFILE": {},
	"EXPORTED_VARIABLES": {}, "FOUND_DOUBLE_DASH": {}, "LOG_DIR": {},
	"LOG_PATH": {}, "ID": {}, "BEGIN_TIMESTAMP": {}, "TIME_FORMAT": {},
	"MTIME_FORMAT": {}, "IS_MACOS": {}, "RANDOMDELAY": {},
	"RANDOMDELAYFILEEXTENSIONS": {}, "RANDOMDELAYIGNOREDMINUTES": {},
	"COMMAND_TIMEOUT_TIME": {}, "SET_U_ON": {}, "_TASK_EXIT_CODE": {},
	"PATH": {}, "HOME": {}, "SHELL": {}, "ENV": {}, "BASH_ENV": {},
	"BASHOPTS": {}, "SHELLOPTS": {}, "IFS": {}, "CDPATH": {}, "GLOBIGNORE": {},
	"PROMPT_COMMAND": {}, "PS4": {}, "NODE_OPTIONS": {}, "NODE_PATH": {},
	"PREV_NODE_OPTIONS": {}, "PREV_NODE_PATH": {}, "PREV_PYTHONPATH": {},
	"GIT_SSH": {}, "GIT_SSH_COMMAND": {}, "GIT_ASKPASS": {},
	"GIT_EXEC_PATH": {}, "GIT_TEMPLATE_DIR": {}, "SSH_ASKPASS": {},
}

var reservedTaskEnvPrefixes = []string{
	"BASH_", "QL", "FILE_", "DIR_", "LD_", "DYLD_", "PYTHON", "GIT_CONFIG_",
}

// IsAllowedTaskEnvName reports whether a name may be stored as task data.
func IsAllowedTaskEnvName(name string) bool {
	if name == "" || !isEnvIdentifier(name) {
		return false
	}
	upper := strings.ToUpper(name)
	if _, reserved := reservedTaskEnvNames[upper]; reserved {
		return false
	}
	for _, prefix := range reservedTaskEnvPrefixes {
		if strings.HasPrefix(upper, prefix) {
			return false
		}
	}
	return true
}

// IsEnvIdentifier matches /^[a-zA-Z_][0-9a-zA-Z_]*$/: the shape a shell
// variable name must have before the reserved-name rules apply.
func IsEnvIdentifier(name string) bool {
	return isEnvIdentifier(name)
}

// isEnvIdentifier matches /^[a-zA-Z_][0-9a-zA-Z_]*$/.
func isEnvIdentifier(name string) bool {
	for index, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9' && index > 0:
		default:
			return false
		}
	}
	return name != ""
}
