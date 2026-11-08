// Package i18n holds the subset of the panel's translation table the auth
// milestone needs.
//
// Node keeps a single dictionary (back/shared/i18n.ts) and returns the key
// itself when a language has no entry. M2 mirrors that behaviour for the
// strings it emits; the remaining modules extend the table in their own
// milestones.
package i18n

import (
	"fmt"
	"strings"
	"sync/atomic"
)

var currentLang atomic.Value

func init() {
	currentLang.Store("zh")
}

var english = map[string]string{
	"请先初始化":        "Please initialize the panel first",
	"请先关闭两步验证":     "Please disable two-factor authentication first",
	"暂无权限":         "Access denied",
	"通知发送失败，请检查参数": "Notification failed, check parameters",
	"通知发送成功":       "Notification sent",
	"通知发送失败，请检查系统设置/通知配置":           "Notification failed, check system settings",
	"密码不能设置为admin":                  "Password cannot be admin",
	"更新成功":                          "Update successful",
	"未知错误":                          "Unknown error",
	"验证失败":                          "Verification failed",
	"Token 已失效":                     "Token expired",
	"请先登录":                          "Please login first",
	"错误的用户名密码，请重试":                  "Incorrect username or password, please try again",
	"该 IP 已被列入黑名单":                  "This IP address has been blocked",
	"已加入 IP 黑名单":                    "IP address added to the blacklist",
	"已移出 IP 黑名单":                    "IP address removed from the blacklist",
	"登录通知":                          "Login Notification",
	"仅支持 PNG、JPEG、GIF、WebP、AVIF 图片": "Only PNG, JPEG, GIF, WebP and AVIF images are supported",
	"你于":             "You at ",
	"在":              " in ",
	"端":              " client",
	"内网IP":           "Private IP",
	"登录失败":           "login failed",
	"登录成功":           "Login successful",
	"，ip地址":          ", IP: ",
	"失败次数过多，请%s秒后重试": "Too many failed attempts, please retry in %s seconds",
	"失败次数过多，请稍后重试":   "Too many failed attempts. Please try again later.",
	"青龙":             "QingLong",
	"日志设置为忽略":        "Logging is disabled",
	"运行中...":         "Running...",
	"日志不存在...":       "Log not found...",
	"实例不存在或已停止":      "Instance does not exist or has stopped",
	"实例已停止":          "Instance stopped",
	"参数错误":           "Invalid parameter",
	"接口已下线，请使用 /logs/detail 接口":          "This endpoint is offline, use /logs/detail",
	"资源已存在，请检查重复的名称或值":                   "Resource already exists, check duplicate names or values",
	"## 开始执行... %s\n":                    "## Starting... %s\n",
	"## 开始执行... %s\n\n":                  "## Starting... %s\n\n",
	"\n## 已停止 🛑... %s  耗时 %s 秒%s":        "\n## Stopped 🛑... %s  took %s seconds%s",
	"\n## 完成 ✅... %s  耗时 %s 秒%s":         "\n## Completed ✅... %s  took %s seconds%s",
	"\n## 失败 ❌(退出码 %s)... %s  耗时 %s 秒%s": "\n## Failed ❌(exit code %s)... %s  took %s seconds%s",
	"## 执行结束... %s  耗时 %s 秒":             "## Execution finished... %s  elapsed %s seconds",
	"client_id 或 client_seret 有误":        "Invalid client_id or client_secret",
	"trust proxy 配置格式无效":                 "Invalid trust proxy configuration",
	"代理层数不能超过 20":                        "Proxy hop count cannot exceed 20",
	"任务未找到":                              "Task not found",
	"任务#%s":                              "Task#%s",
	"依赖%s%s，结束时间 %s，耗时 %s 秒":             "Dependency %s%s, end time %s, elapsed %s seconds",
	"保存成功":                               "Saved successfully",
	"删除":                                 "Uninstall",
	"参数不正确":                              "Invalid parameter",
	"失败":                                 "Failed",
	"安装":                                 "Install",
	"开始%s依赖 %s，开始时间 %s\n\n":              "Start %s dependency %s, start time %s\n\n",
	"开始%s依赖 %s，开始时间 %s\n\n当前系统不支持\n\n依赖%s失败，结束时间 %s，耗时 %s 秒": "Start %s dependency %s, start time %s\n\nCurrent system not supported\n\nDependency %s failed, end time %s, elapsed %s seconds",
	"成功":            "Succeeded",
	"执行after命令...":  "Execute after command...",
	"执行before命令...": "Execute before command...",
	"接口已下线，请使用 /configs/detail 接口": "This endpoint has been retired. Use /configs/detail instead.",
	"接口已下线，请使用 /scripts/detail 接口": "This endpoint has been retired. Use /scripts/detail instead.",
	"文件无法访问": "File not accessible",
	"未分类":    "Uncategorized",
	"检查更新失败，请检查网络后重试":                                    "Failed to check for updates. Check your network and try again.",
	"检测到已经安装 %s\n\n%s\n\n跳过安装\n\n依赖%s成功，结束时间 %s，耗时 %s 秒": "Already installed %s\n\n%s\n\nSkipping install\n\nDependency %s succeeded, end time %s, elapsed %s seconds",
	"每条数据 name 或者 value 字段不能为空，参考导出文件格式":                 "Each entry must have name and value, see export format",
	"环境变量 QL_TRUST_PROXY 已生效，系统设置不可覆盖":                   "QL_TRUST_PROXY is active and cannot be overridden by system settings",
	"环境变量名称不可使用":                                         "Environment variable name is reserved or invalid",
	"系统配置不存在":                                            "System configuration does not exist",
	"订阅执行完成":                                             "Subscription completed",
	"设置时区失败":                                             "Failed to set timezone",
	"跳过订阅 %s 的无效 SSH 配置: %s":                             "Skipping invalid SSH configuration for subscription %s: %s",
	"配置更新失败":                                             "Failed to update configuration",
	"删除日志":                                               "Delete logs",
	"当前版本不支持在线升级":                                        "Online upgrade is not supported in this build",
	"当前系统不支持修改 Linux 镜像源":                                "Changing the Linux mirror is not supported on this platform",
	"当前版本不支持在线修改 Linux 镜像源":                              "Changing the Linux mirror online is not supported in this build",
	"未检测到 pnpm，请先安装 pnpm":                                "pnpm not found, please install pnpm first",
}

// SetLang switches the active language ("zh" or "en").
func SetLang(lang string) {
	if lang != "en" {
		lang = "zh"
	}
	currentLang.Store(lang)
}

// Lang returns the active language.
func Lang() string {
	value, _ := currentLang.Load().(string)
	if value == "" {
		return "zh"
	}
	return value
}

// T translates a key, falling back to the key itself like the Node version.
func T(key string) string {
	if Lang() == "en" {
		if translated, ok := english[key]; ok {
			return translated
		}
	}
	return key
}

// Tf is the %s substitution helper (tf in the Node code).
func Tf(key string, args ...any) string {
	message := T(key)
	for _, arg := range args {
		message = strings.Replace(message, "%s", fmt.Sprint(arg), 1)
	}
	return message
}
