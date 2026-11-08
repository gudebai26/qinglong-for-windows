# 青龙面板 2.24.0 API 契约清单

共 **156** 条路由，按模块分组。除标注外，响应均为 `{code, data}` / `{code, message}`。

列说明：**鉴权** `/open/*` 为应用 Token、`/api/*` 为 JWT；**响应** 标出非 JSON 包装的特殊接口；
**请求体** 标注 `multipart:字段名` 的是文件上传。

| 模块 | 文件 | 路由数 |
| --- | --- | --- |
| 任务（Crontab） | `back/api/cron.ts` | 27 |
| 系统设置 | `back/api/system.ts` | 26 |
| 用户与鉴权 | `back/api/user.ts` | 17 |
| 环境变量 | `back/api/env.ts` | 14 |
| 脚本 | `back/api/script.ts` | 13 |
| 订阅 | `back/api/subscription.ts` | 12 |
| 依赖管理 | `back/api/dependence.ts` | 11 |
| 仪表盘 | `back/api/dashboard.ts` | 10 |
| OpenAPI / 应用 | `back/api/open.ts` | 6 |
| 面板配置 | `back/api/config.ts` | 5 |
| 日志 | `back/api/log.ts` | 5 |
| 客户端 IP | `back/api/clientIp.ts` | 3 |
| 存储保留 | `back/api/retention.ts` | 3 |
| 升级 | `back/api/update.ts` | 3 |
| 健康检查 | `back/api/health.ts` | 1 |


## 任务（Crontab）（`back/api/cron.ts`，27 条）

| # | 方法 | 路径 | 鉴权 | 请求体字段 | 查询参数 | 路径参数 | 响应 | 服务 | 源码 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | GET | `/api/crons/views` | JWT |  |  |  | {code,data} | CronViewService | cron.ts:33 |
| 2 | POST | `/api/crons/views` | JWT | name, sorts, filters, filterRelation |  |  | {code,data} | CronViewService | cron.ts:46 |
| 3 | PUT | `/api/crons/views` | JWT | name, id, sorts, filters, filterRelation |  |  | {code,data} | CronViewService | cron.ts:67 |
| 4 | DELETE | `/api/crons/views` | JWT | [数组] |  |  | {code,data} | CronViewService | cron.ts:93 |
| 5 | PUT | `/api/crons/views/move` | JWT | fromIndex, toIndex, id |  |  | {code,data} | CronViewService | cron.ts:109 |
| 6 | PUT | `/api/crons/views/disable` | JWT | [数组] |  |  | {code,data} | CronViewService | cron.ts:129 |
| 7 | PUT | `/api/crons/views/enable` | JWT | [数组] |  |  | {code,data} | CronViewService | cron.ts:146 |
| 8 | GET | `/api/crons` | JWT |  |  |  | {code,data} | CronService | cron.ts:163 |
| 9 | GET | `/api/crons/detail` | JWT |  |  |  | {code,data} | CronService | cron.ts:175 |
| 10 | POST | `/api/crons` | JWT | name, command, schedule, labels, sub_id, extra_schedules, task_before, task_after, log_name, allow_multiple_instances, work_dir |  |  | {code,data} | CronService | cron.ts:190 |
| 11 | PUT | `/api/crons/run` | JWT | [数组] |  |  | {code,data} | CronService | cron.ts:207 |
| 12 | PUT | `/api/crons/stop` | JWT | [数组] |  |  | {code,data} | CronService | cron.ts:224 |
| 13 | DELETE | `/api/crons/labels` | JWT | ids, labels |  |  | {code,data} | CronService | cron.ts:241 |
| 14 | POST | `/api/crons/labels` | JWT | ids, labels |  |  | {code,data} | CronService | cron.ts:264 |
| 15 | PUT | `/api/crons/disable` | JWT | [数组] |  |  | {code,data} | CronService | cron.ts:284 |
| 16 | PUT | `/api/crons/enable` | JWT | [数组] |  |  | {code,data} | CronService | cron.ts:301 |
| 17 | GET | `/api/crons/:id/log` | JWT |  | offset, limit, tail, t | id | {code,data} | CronService | cron.ts:318 |
| 18 | PUT | `/api/crons` | JWT | id, name, command, schedule, labels, sub_id, extra_schedules, task_before, task_after, log_name, allow_multiple_instances, work_dir |  |  | {code,data} | CronService | cron.ts:359 |
| 19 | DELETE | `/api/crons` | JWT | [数组] |  |  | {code,data} | CronService | cron.ts:379 |
| 20 | PUT | `/api/crons/pin` | JWT | [数组] |  |  | {code,data} | CronService | cron.ts:396 |
| 21 | PUT | `/api/crons/unpin` | JWT | [数组] |  |  | {code,data} | CronService | cron.ts:413 |
| 22 | GET | `/api/crons/import` | JWT |  |  |  | {code,data} | CronService | cron.ts:430 |
| 23 | GET | `/api/crons/:id` | JWT |  |  | id | {code,data} | CronService | cron.ts:444 |
| 24 | PUT | `/api/crons/status` | JWT | ids, status, pid, log_path, last_running_time, last_execution_time, exit_code |  |  | {code,data} | CronService | cron.ts:463 |
| 25 | GET | `/api/crons/:id/instances` | JWT |  |  | id | {code,data} | — | cron.ts:491 |
| 26 | POST | `/api/crons/:id/instances/:instanceId/stop` | JWT |  |  | id, instanceId | {code,data} | CronService | cron.ts:514 |
| 27 | GET | `/api/crons/:id/logs` | JWT |  |  | id | {code,data} | CronService | cron.ts:533 |

## 系统设置（`back/api/system.ts`，26 条）

| # | 方法 | 路径 | 鉴权 | 请求体字段 | 查询参数 | 路径参数 | 响应 | 服务 | 源码 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | GET | `/api/system` | 公开 |  |  |  | {code,data} | UserService | system.ts:32 |
| 2 | GET | `/api/system/config` | JWT |  |  |  | {code,data} | SystemService | system.ts:58 |
| 3 | PUT | `/api/system/config/log-remove-frequency` | JWT | logRemoveFrequency |  |  | {code,data} | SystemService | system.ts:72 |
| 4 | PUT | `/api/system/config/cron-concurrency` | JWT | cronConcurrency |  |  | {code,data} | SystemService | system.ts:90 |
| 5 | PUT | `/api/system/config/dependence-proxy` | JWT | dependenceProxy |  |  | {code,data} | SystemService | system.ts:108 |
| 6 | PUT | `/api/system/config/node-mirror` | JWT | nodeMirror |  |  | 流式 | SystemService | system.ts:126 |
| 7 | PUT | `/api/system/config/python-mirror` | JWT | pythonMirror |  |  | {code,data} | SystemService | system.ts:144 |
| 8 | PUT | `/api/system/config/linux-mirror` | JWT | linuxMirror |  |  | 流式 | SystemService | system.ts:162 |
| 9 | PUT | `/api/system/update-check` | JWT |  |  |  | {code,data} | SystemService | system.ts:180 |
| 10 | PUT | `/api/system/update` | JWT |  |  |  | {code,data} | SystemService | system.ts:194 |
| 11 | GET | `/api/system/update-status` | JWT |  |  |  | {code,data} | SystemService | system.ts:208 |
| 12 | PUT | `/api/system/reload` | JWT | type |  |  | {code,data} | SystemService | system.ts:219 |
| 13 | PUT | `/api/system/notify` | JWT | title, content |  |  | {code,data} | SystemService | system.ts:238 |
| 14 | PUT | `/api/system/command-run` | JWT | command |  |  | 流式（含 QL-Task-Pid / QL-Task-Log 响应头） | SystemService | system.ts:258 |
| 15 | PUT | `/api/system/command-stop` | JWT | command, pid |  |  | {code,data} | SystemService | system.ts:316 |
| 16 | PUT | `/api/system/data/export` | JWT | type |  |  | 二进制流（zip） | SystemService | system.ts:335 |
| 17 | PUT | `/api/system/data/import` | JWT | multipart:data |  |  | {code,data} | SystemService | system.ts:352 |
| 18 | GET | `/api/system/log` | JWT |  | startTime, endTime, limit, t |  | 直接写 res | SystemService | system.ts:366 |
| 19 | GET | `/api/system/notify-log` | JWT |  |  |  | {code,data} | SystemService | system.ts:397 |
| 20 | DELETE | `/api/system/log` | JWT |  |  |  | {code} | SystemService | system.ts:410 |
| 21 | PUT | `/api/system/auth/reset` | JWT | retries, twoFactorActivated, password, username |  |  | {code,data} | UserService | system.ts:423 |
| 22 | PUT | `/api/system/config/timezone` | JWT | timezone |  |  | {code,data} | SystemService | system.ts:447 |
| 23 | PUT | `/api/system/config/lang` | JWT | lang |  |  | {code,data} | SystemService | system.ts:465 |
| 24 | PUT | `/api/system/config/panel-title` | JWT | panelTitle |  |  | {code,data} | SystemService | system.ts:483 |
| 25 | PUT | `/api/system/config/global-ssh-key` | JWT | globalSshKey |  |  | {code,data} | SystemService | system.ts:501 |
| 26 | PUT | `/api/system/config/dependence-clean` | JWT | type |  |  | {code,data} | SystemService | system.ts:519 |

## 用户与鉴权（`back/api/user.ts`，17 条）

| # | 方法 | 路径 | 鉴权 | 请求体字段 | 查询参数 | 路径参数 | 响应 | 服务 | 源码 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | POST | `/api/user/login` | 公开 | username, password |  |  | {code,data} | UserService | user.ts:48 |
| 2 | POST | `/api/user/logout` | JWT |  |  |  | {code} | UserService | user.ts:72 |
| 3 | PUT | `/api/user` | JWT | username, password |  |  | {code,data} | UserService | user.ts:87 |
| 4 | GET | `/api/user` | JWT |  |  |  | {code,data} | UserService | user.ts:109 |
| 5 | GET | `/api/user/two-factor/init` | JWT |  |  |  | {code,data} | UserService | user.ts:128 |
| 6 | PUT | `/api/user/two-factor/active` | JWT | code |  |  | {code,data} | UserService | user.ts:142 |
| 7 | PUT | `/api/user/two-factor/deactivate` | JWT |  |  |  | {code,data} | UserService | user.ts:161 |
| 8 | PUT | `/api/user/two-factor/login` | 公开 | code, username, password |  |  | {code,data} | UserService | user.ts:175 |
| 9 | GET | `/api/user/login-log` | JWT |  |  |  | {code,data} | UserService | user.ts:197 |
| 10 | GET | `/api/user/ip-blacklist` | JWT |  |  |  | {code,data} | UserService | user.ts:211 |
| 11 | PUT | `/api/user/ip-blacklist` | JWT | ip |  |  | {code,data} | UserService | user.ts:224 |
| 12 | DELETE | `/api/user/ip-blacklist` | JWT | ip |  |  | {code,data} | UserService | user.ts:244 |
| 13 | GET | `/api/user/notification` | JWT |  |  |  | {code,data} | UserService | user.ts:264 |
| 14 | PUT | `/api/user/notification` | JWT |  |  |  | {code,data} | UserService | user.ts:278 |
| 15 | PUT | `/api/user/init` | 公开 | username, password |  |  | {code,data} | UserService | user.ts:292 |
| 16 | PUT | `/api/user/notification/init` | 公开 |  |  |  | {code,data} | UserService | user.ts:313 |
| 17 | PUT | `/api/user/avatar` | JWT | multipart:avatar |  |  | {code,data} | UserService | user.ts:327 |

## 环境变量（`back/api/env.ts`，14 条）

| # | 方法 | 路径 | 鉴权 | 请求体字段 | 查询参数 | 路径参数 | 响应 | 服务 | 源码 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | GET | `/api/envs` | JWT |  |  |  | {code,data} | EnvService | env.ts:22 |
| 2 | POST | `/api/envs` | JWT | [数组] |  |  | {code,data} | EnvService | env.ts:34 |
| 3 | PUT | `/api/envs` | JWT | value, name, remarks, id, labels |  |  | {code,data} | EnvService | env.ts:63 |
| 4 | DELETE | `/api/envs` | JWT | [数组] |  |  | {code,data} | EnvService | env.ts:86 |
| 5 | PUT | `/api/envs/:id/move` | JWT | fromIndex, toIndex |  | id | {code,data} | EnvService | env.ts:103 |
| 6 | PUT | `/api/envs/disable` | JWT | [数组] |  |  | {code,data} | EnvService | env.ts:125 |
| 7 | PUT | `/api/envs/enable` | JWT | [数组] |  |  | {code,data} | EnvService | env.ts:142 |
| 8 | PUT | `/api/envs/name` | JWT | ids, name |  |  | {code,data} | EnvService | env.ts:159 |
| 9 | GET | `/api/envs/:id` | JWT |  |  | id | {code,data} | EnvService | env.ts:179 |
| 10 | PUT | `/api/envs/pin` | JWT | [数组] |  |  | {code,data} | EnvService | env.ts:198 |
| 11 | PUT | `/api/envs/unpin` | JWT | [数组] |  |  | {code,data} | EnvService | env.ts:215 |
| 12 | POST | `/api/envs/labels` | JWT | ids, labels |  |  | {code,data} | EnvService | env.ts:232 |
| 13 | DELETE | `/api/envs/labels` | JWT | ids, labels |  |  | {code,data} | EnvService | env.ts:251 |
| 14 | POST | `/api/envs/upload` | JWT | multipart:env |  |  | {code,data} | EnvService | env.ts:270 |

## 脚本（`back/api/script.ts`，13 条）

| # | 方法 | 路径 | 鉴权 | 请求体字段 | 查询参数 | 路径参数 | 响应 | 服务 | 源码 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | GET | `/api/scripts` | JWT |  | path |  | {code,data} | — | script.ts:79 |
| 2 | GET | `/api/scripts/detail` | JWT |  | path, file |  | {code,data} | ScriptService | script.ts:134 |
| 3 | GET | `/api/scripts/history` | JWT |  | filename, path |  | {code,data} | ScriptHistoryService | script.ts:173 |
| 4 | GET | `/api/scripts/history/detail` | JWT |  | id, filename, path |  | {code,data} | ScriptHistoryService | script.ts:189 |
| 5 | PUT | `/api/scripts/history/restore` | JWT | id, expectedHash, filename, path |  |  | {code,data} | ScriptHistoryService | script.ts:211 |
| 6 | GET | `/api/scripts/:file` | JWT |  |  |  | 410 存根 | — | script.ts:232 |
| 7 | POST | `/api/scripts` | JWT | filename, path, content, skipHistory, expectedHash, originFilename, directory, file, multipart:file |  |  | {code} | ScriptHistoryService | script.ts:239 |
| 8 | PUT | `/api/scripts` | JWT | filename, path, content, skipHistory, expectedHash |  |  | {code,data} | ScriptService + ScriptHistoryService | script.ts:384 |
| 9 | DELETE | `/api/scripts` | JWT | filename, path, type |  |  | {code} | ScriptService | script.ts:425 |
| 10 | POST | `/api/scripts/download` | JWT | filename, path |  |  | 文件流 | ScriptService | script.ts:459 |
| 11 | PUT | `/api/scripts/run` | JWT | filename, content, path |  |  | {code,data} | ScriptService | script.ts:495 |
| 12 | PUT | `/api/scripts/stop` | JWT | filename, path, pid |  |  | {code,data} | ScriptService | script.ts:527 |
| 13 | PUT | `/api/scripts/rename` | JWT | filename, path, newFilename |  |  | {code} | — | script.ts:570 |

## 订阅（`back/api/subscription.ts`，12 条）

| # | 方法 | 路径 | 鉴权 | 请求体字段 | 查询参数 | 路径参数 | 响应 | 服务 | 源码 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | GET | `/api/subscriptions` | JWT |  |  |  | {code,data} | SubscriptionService | subscription.ts:12 |
| 2 | POST | `/api/subscriptions` | JWT | type, schedule, interval_schedule, value, name, url, whitelist, blacklist, branch, dependences, pull_type, pull_option, extensions, sub_before, sub_after, schedule_type, alias, proxy, autoAddCron, autoDelCron |  |  | {code,data} | SubscriptionService | subscription.ts:27 |
| 3 | PUT | `/api/subscriptions/run` | JWT | [数组] |  |  | {code,data} | SubscriptionService | subscription.ts:74 |
| 4 | PUT | `/api/subscriptions/stop` | JWT | [数组] |  |  | {code,data} | SubscriptionService | subscription.ts:91 |
| 5 | PUT | `/api/subscriptions/disable` | JWT | [数组] |  |  | {code,data} | SubscriptionService | subscription.ts:108 |
| 6 | PUT | `/api/subscriptions/enable` | JWT | [数组] |  |  | {code,data} | SubscriptionService | subscription.ts:125 |
| 7 | GET | `/api/subscriptions/:id/log` | JWT |  | offset, limit, tail, t | id | {code,data} | SubscriptionService | subscription.ts:142 |
| 8 | PUT | `/api/subscriptions` | JWT | type, schedule, interval_schedule, name, url, whitelist, blacklist, branch, dependences, pull_type, pull_option, schedule_type, extensions, sub_before, sub_after, alias, proxy, autoAddCron, autoDelCron, id |  |  | {code,data} | SubscriptionService | subscription.ts:175 |
| 9 | DELETE | `/api/subscriptions` | JWT | [数组] | force, t |  | {code,data} | SubscriptionService | subscription.ts:217 |
| 10 | GET | `/api/subscriptions/:id` | JWT |  |  | id | {code,data} | SubscriptionService | subscription.ts:238 |
| 11 | PUT | `/api/subscriptions/status` | JWT | ids, status, pid, log_path |  |  | {code,data} | SubscriptionService | subscription.ts:257 |
| 12 | GET | `/api/subscriptions/:id/logs` | JWT |  |  | id | {code,data} | SubscriptionService | subscription.ts:283 |

## 依赖管理（`back/api/dependence.ts`，11 条）

| # | 方法 | 路径 | 鉴权 | 请求体字段 | 查询参数 | 路径参数 | 响应 | 服务 | 源码 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | GET | `/api/dependencies` | JWT |  | searchValue, type, status |  | {code,data} | DependenceService | dependence.ts:11 |
| 2 | POST | `/api/dependencies/refresh` | JWT | type |  |  | {code,data} | DependenceService | dependence.ts:33 |
| 3 | GET | `/api/dependencies/refresh/active` | JWT |  |  |  | {code,data} | DependenceService | dependence.ts:57 |
| 4 | GET | `/api/dependencies/refresh/:id` | JWT |  |  | id | {code,data} | DependenceService | dependence.ts:73 |
| 5 | POST | `/api/dependencies` | JWT | [数组] |  |  | {code,data} | DependenceService | dependence.ts:96 |
| 6 | PUT | `/api/dependencies` | JWT | name, id, type, remark |  |  | {code,data} | DependenceService | dependence.ts:118 |
| 7 | DELETE | `/api/dependencies` | JWT | [数组] |  |  | {code,data} | DependenceService | dependence.ts:139 |
| 8 | DELETE | `/api/dependencies/force` | JWT | [数组] |  |  | {code,data} | DependenceService | dependence.ts:155 |
| 9 | GET | `/api/dependencies/:id` | JWT |  |  | id | {code,data} | DependenceService | dependence.ts:171 |
| 10 | PUT | `/api/dependencies/reinstall` | JWT | [数组] |  |  | {code,data} | DependenceService | dependence.ts:189 |
| 11 | PUT | `/api/dependencies/cancel` | JWT | [数组] |  |  | {code} | DependenceService | dependence.ts:205 |

## 仪表盘（`back/api/dashboard.ts`，10 条）

| # | 方法 | 路径 | 鉴权 | 请求体字段 | 查询参数 | 路径参数 | 响应 | 服务 | 源码 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | POST | `/api/dashboard/record` | JWT |  |  |  | {code} | — | dashboard.ts:20 |
| 2 | GET | `/api/dashboard/overview` | JWT |  |  |  | {code,data} | — | dashboard.ts:63 |
| 3 | GET | `/api/dashboard/successes` | JWT |  |  |  | {code,data} | — | dashboard.ts:111 |
| 4 | GET | `/api/dashboard/failures` | JWT |  |  |  | {code,data} | — | dashboard.ts:111 |
| 5 | GET | `/api/dashboard/trend` | JWT |  |  |  | {code,data} | — | dashboard.ts:150 |
| 6 | GET | `/api/dashboard/top-time` | JWT |  |  |  | {code,data} | — | dashboard.ts:196 |
| 7 | GET | `/api/dashboard/top-count` | JWT |  |  |  | {code,data} | — | dashboard.ts:238 |
| 8 | GET | `/api/dashboard/runtime` | JWT |  |  |  | {code,data} | — | dashboard.ts:284 |
| 9 | GET | `/api/dashboard/labels` | JWT |  |  |  | {code,data} | — | dashboard.ts:361 |
| 10 | GET | `/api/dashboard/system` | JWT |  |  |  | {code,data} | — | dashboard.ts:413 |

## OpenAPI / 应用（`back/api/open.ts`，6 条）

| # | 方法 | 路径 | 鉴权 | 请求体字段 | 查询参数 | 路径参数 | 响应 | 服务 | 源码 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | GET | `/api/apps` | JWT（/open 为 App Token） |  |  |  | {code,data} | OpenService | open.ts:11 |
| 2 | POST | `/api/apps` | JWT（/open 为 App Token） | name, scopes |  |  | {code,data} | OpenService | open.ts:25 |
| 3 | PUT | `/api/apps` | JWT（/open 为 App Token） | name, scopes, id |  |  | {code,data} | OpenService | open.ts:45 |
| 4 | DELETE | `/api/apps` | JWT（/open 为 App Token） | [数组] |  |  | {code,data} | OpenService | open.ts:66 |
| 5 | PUT | `/api/apps/:id/reset-secret` | JWT（/open 为 App Token） |  |  | id | {code,data} | OpenService | open.ts:83 |
| 6 | GET | `/api/auth/token` | JWT（/open 为 App Token） |  | client_id, client_secret |  | {code,data} | OpenService | open.ts:102 |

## 面板配置（`back/api/config.ts`，5 条）

| # | 方法 | 路径 | 鉴权 | 请求体字段 | 查询参数 | 路径参数 | 响应 | 服务 | 源码 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | GET | `/api/configs/samples` | JWT |  |  |  | {code,data} | — | config.ts:17 |
| 2 | GET | `/api/configs/files` | JWT |  |  |  | {code,data} | — | config.ts:31 |
| 3 | GET | `/api/configs/detail` | JWT |  |  |  | {code,data} | ConfigService | config.ts:51 |
| 4 | POST | `/api/configs/save` | JWT | name, content |  |  | {code,data} | — | config.ts:63 |
| 5 | GET | `/api/configs/:file` | JWT |  |  |  | 410 存根 | — | config.ts:94 |

## 日志（`back/api/log.ts`，5 条）

| # | 方法 | 路径 | 鉴权 | 请求体字段 | 查询参数 | 路径参数 | 响应 | 服务 | 源码 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | GET | `/api/logs` | JWT |  |  |  | {code,data} | — | log.ts:21 |
| 2 | GET | `/api/logs/detail` | JWT |  | path, file, offset, limit, tail, t |  | {code,data} | LogService | log.ts:35 |
| 3 | GET | `/api/logs/:file` | JWT |  |  |  | 410 存根 | — | log.ts:89 |
| 4 | DELETE | `/api/logs` | JWT | filename, path, type |  |  | {code} | LogService | log.ts:99 |
| 5 | POST | `/api/logs/download` | JWT | filename, path |  |  | 文件流 | LogService | log.ts:130 |

## 客户端 IP（`back/api/clientIp.ts`，3 条）

| # | 方法 | 路径 | 鉴权 | 请求体字段 | 查询参数 | 路径参数 | 响应 | 服务 | 源码 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | GET | `/api/system/client-ip/config` | JWT |  |  |  | {code,data} | — | clientIp.ts:15 |
| 2 | PUT | `/api/system/client-ip/config` | JWT | trustProxy |  |  | {code,data} | — | clientIp.ts:26 |
| 3 | GET | `/api/system/client-ip/diagnose` | JWT |  |  |  | {code,data} | — | clientIp.ts:46 |

## 存储保留（`back/api/retention.ts`，3 条）

| # | 方法 | 路径 | 鉴权 | 请求体字段 | 查询参数 | 路径参数 | 响应 | 服务 | 源码 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | PUT | `/api/system/storage-retention/config` | JWT | runningInstanceRetentionDays, cronStatRetentionDays |  |  | {code,data} | RetentionService | retention.ts:32 |
| 2 | POST | `/api/system/storage-retention/preview` | JWT | dependenceCacheTypes, compactDatabase, runningInstanceRetentionDays, cronStatRetentionDays |  |  | {code,data} | RetentionService | retention.ts:46 |
| 3 | POST | `/api/system/storage-retention/cleanup` | JWT | confirmation, dependenceCacheTypes, compactDatabase, runningInstanceRetentionDays, cronStatRetentionDays |  |  | {code,data} | RetentionService | retention.ts:60 |

## 升级（`back/api/update.ts`，3 条）

| # | 方法 | 路径 | 鉴权 | 请求体字段 | 查询参数 | 路径参数 | 响应 | 服务 | 源码 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | PUT | `/api/update/reload` | JWT |  |  |  | {code,data} | SystemService | update.ts:10 |
| 2 | PUT | `/api/update/system` | JWT |  |  |  | {code,data} | SystemService | update.ts:24 |
| 3 | PUT | `/api/update/data` | JWT |  |  |  | {code,data} | SystemService | update.ts:38 |

## 健康检查（`back/api/health.ts`，1 条）

| # | 方法 | 路径 | 鉴权 | 请求体字段 | 查询参数 | 路径参数 | 响应 | 服务 | 源码 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | GET | `/api/health` | 公开 |  |  |  | {code,data} | HealthService | health.ts:10 |

---

## 非路由端点（不在上表计数内，但必须实现）

| 端点 | 位置 | 说明 |
| --- | --- | --- |
| `GET /api/env.js` | `back/loaders/express.ts:51` | 前端 index.html 直接引用，返回运行时变量 |
| `GET /api/ws`（SockJS） | `back/loaders/sock.ts:12` | 实时日志/状态推送，非 REST |
| `GET /api/static/*` | `back/loaders/express.ts:52-60` | 上传文件静态托管，带 `nosniff` + CSP `sandbox` |
| `GET *`（SPA 回退） | `back/loaders/express.ts:218` | 非 `/api/*` 返回 index.html；`/api/*` 未命中返回 `{code:404}` |
| `/open/*` 全量别名 | `back/loaders/express.ts:215` | `rewrite('/open/*','/api/$1')`，上表 156 条在 `/open` 下同样可达 |

## 必须对齐的中间件行为

| 行为 | 位置 | Go 版要求 |
| --- | --- | --- |
| `case sensitive routing` + `strict routing` + `protectedPathCase` | `express.ts:36-43` | 拒绝路径大小写变体，否则鉴权可被绕过 |
| 全局删除 `req.query.t` | `httpApplication.ts:20-23` | `t` 只是缓存击穿参数，业务不读 |
| 登录 / `auth/token` 限流 | `user.ts:48`、`open.ts:104` | 15 分钟 100 次 |
| 密码错误计数与锁定 | `back/shared/loginThrottle.ts` | 与 Node 版一致 |

## 已知结构细节（自动抽取无法完全表达，必须按源码实现）

| 项 | 说明 |
| --- | --- |
| `POST/PUT /api/subscriptions` | `value` 是 `interval_schedule: {type, value}` 的**嵌套**字段，不是顶层字段 |
| `PUT /api/crons` | body 为 `...commonCronSchema` + `id`，共 12 个字段，`command`/`schedule` 必填 |
| `extra_schedules` | 对象数组，每项含 `schedule`，Go 侧要建结构体而不是 `[]string` |
| 3 条 410 存根 | `/scripts/:file`、`/logs/:file`、`/configs/:file`，固定返回 410，不要真读文件 |
| 非 JSON 响应 | `logs/download`、`scripts/download`（文件流）；`system/data/export`（zip）；`system/log`（直接写 res）；`system/command-run`、`config/node-mirror`、`config/linux-mirror`（流式） |
| `system/config/python-mirror` | 普通 JSON，与 node/linux 两个镜像接口**不一致** |
| multipart 字段 | `envs/upload→env`、`scripts→file`、`system/data/import→data`、`user/avatar→avatar` |
| 路径参数类型 | 13 条路由带 `params` 校验（uuid / number），错误响应结构随类型不同 |
| `POST /api/dashboard/record` | 无 celebrate，手写校验 `ref_id` 必填，另有 `code`、`elapsed`；`shell/share.sh` 回写统计走这里，Go 执行器要自己调用 |
| `GET /api/system/log` 响应头 | 除正文外还设置 `Content-Length`、`X-QL-Log-Total`（总字节数）、`X-QL-Log-Truncated`（`total > contentLength` 时为 `true`），分页/截断逻辑必须对齐 |
| `DELETE /api/system/log` | 返回裸 `{code:200}`，**没有 data**，Go 侧不要写成 `{code,data}` |

## Go struct 片段（嵌套结构，不要靠猜）

```go
type IntervalSchedule struct {
    Type  string `json:"type"`
    Value int    `json:"value"`
}

type ExtraSchedule struct {
    Schedule string `json:"schedule"`
}

type CronViewFilter struct {
    Property  string `json:"property"`
    Operation string `json:"operation"`
    Value     string `json:"value"`
}

type CronViewSort struct {
    Type  string `json:"type"` // ASC / DESC
    Value string `json:"value"`
}

type CronView struct {
    Name           string           `json:"name"`
    ID             int64            `json:"id"`
    Position       int              `json:"position"`
    IsDisabled     int              `json:"isDisabled"`
    Filters        []CronViewFilter `json:"filters"`
    Sorts          []CronViewSort   `json:"sorts"`
    FilterRelation string           `json:"filterRelation"` // and / or
    Type           int              `json:"type"`
}
```

其余约定：`labels`、`scopes` 是 `[]string`；9 条路由的 body 是数组（见「请求体」列的 `[数组]`）；
`pull_option` 是自由 object，按源码字段透传，不要强类型。

## 附录 · M3 任务 CRUD 补充（2026-11-07）

- `crontab.list` 的行序不是按 `id`，而是无参 `crontabs()` 的默认排序：
  `isPinned DESC → isDisabled ASC → status ASC → createdAt DESC`。
  `setCrontab()` 调用的就是无参 `crontabs()`，所以文件行序必须照此生成。
- `crontabs()` 在 `page=0,size=0` 时返回全量（不分页）；`setCrontab()` 依赖这个行为，
  Go 侧不要默认套 20 条分页。
- `GET /api/crons/:id/log` 与 `GET /api/logs/detail` 的响应是
  `{code,data,logStatus,offset,nextOffset,total,truncated}`（`logStatus` 在 detail 里
  仅当存在运行实例时出现），不是纯 `{code,data}`。
- `GET /api/crons/:id/instances` 使用 `raw:true`，`createdAt/updatedAt` 返回数据库原文
  `YYYY-MM-DD HH:mm:ss.SSS +00:00`，不要转 ISO。
- `extra_schedules` 是对象数组（`[{schedule:"..."}]`），不是 `[]string`。
