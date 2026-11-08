# 青龙后端 Go 重写 · M1–M9 逐轮指令

> 用法：
> 1. 新会话先粘【通用前缀】，再粘对应里程碑的代码块；
> 2. 如果一直用同一个会话，后续只需粘里程碑代码块（AI 有上下文）；
> 3. 每轮结束粘【收尾自检】那段，逼它对照验收标准逐条确认；
> 4. 它跑偏时用最后的【纠偏话术】拉回来。

---

## 通用前缀（每个新会话开头粘一次）

```
项目：把青龙面板 2.24.0 的 Node 后端用 Go 重写，产出单文件 exe，前端沿用现有 React 构建产物。

仓库：F:\WorkBuddy AI\qinglong\qinglong
资料：
- F:\WorkBuddy AI\qinglong\go-rewrite-prompt.md（总约束与验收）
- F:\WorkBuddy AI\qinglong\go-rewrite-plan.md（M0–M9 步骤）
- F:\WorkBuddy AI\qinglong\go-rewrite-api-inventory.md（156 条 API 契约）

铁律（任何里程碑都不能违反）：
1. 不改前端代码、不改 API 路径、不改响应字段；
2. 不改数据库 schema，直接读写现有 data/ 目录；
3. 不用 CGO，SQLite 用 modernc.org/sqlite；
4. 任务执行不用 sh -c 包一层；
5. 每个里程碑结束必须能 go build、能启动、对应页面可用，不留半成品。

当前进度：<在这里写，例如 M2 已完成，M3 未开始>
```

---

## M1 · 骨架

```
做 M1（骨架），只搭架子，不要实现登录/任务/调度等业务。

必做：
1. 初始化 Go module（module qinglong-go，go 1.22+），目录结构按 go-rewrite-plan.md 第 2 节。
2. internal/config：解析 data/config/config.sh，只认 KEY="value" 赋值行（跳过注释/空行，不 eval），环境变量优先；给出默认值。
3. internal/store：modernc.org/sqlite 连接 data/db/database.sqlite；只做 CREATE TABLE IF NOT EXISTS（先读真实库的 sqlite_master 拿表名和列名，不要凭 Sequelize 模型名猜）；用 PRAGMA user_version 记录迁移版本。
4. internal/httpapi：chi 路由，前缀 /api；中间件：CORS、50MB body 限制、统一错误输出 {code,message}、panic 恢复。
5. 静态托管：//go:embed web，SPA 回退（非 /api 且无扩展名 → index.html）；实现 GET /api/env.js（读 back/config/serverEnv.ts 看返回哪些字段）。
   同时按清单末尾「非路由端点」实现 `/api/static/*`（带 nosniff + CSP sandbox）与 `/open/*` 别名重写；
   按「中间件行为」实现大小写严格路由（case sensitive + strict routing）。
6. 日志：log/slog，按天滚动写到 data/syslog/。
7. GET /api/health 返回 {code:200,data:{status:"ok",...}}。

只读参考：back/app.ts、back/loaders/express.ts、back/loaders/httpApplication.ts、back/loaders/logger.ts、back/config/index.ts、back/config/serverEnv.ts
禁止：实现业务接口；改前端；引入 CGO。

交付：go build 通过；启动后 curl /api/health 返回 200；curl / 返回 index.html。
```

---

## M2 · 鉴权与用户

```
做 M2（鉴权与用户），按 go-rewrite-api-inventory.md 里「用户与鉴权」17 条实现。

必做：
1. PUT /api/user/init 初始化管理员（只能成功一次）。
2. POST /api/user/login：scrypt 校验（格式 scrypt$salt$hash，见 back/shared/password.ts）、JWT HS384、登录限流、登录日志。
3. 两步验证：/api/user/two-factor/*（TOTP 绑定、校验、恢复码）。
4. GET/PUT /api/user：资料、改密码、token 列表与失效。
5. keyv.sqlite 读写：authInfo / apps / lang，值格式是 {"value":...,"expires":null}，不要当裸值解析。
6. JWT 中间件：Authorization: Bearer，白名单与 back/config/index.ts 的 apiWhiteList 一致。

硬性验收：
- 用 Node 版创建的管理员账号密码，能在 Go 版直接登录（不能要求重置）；
- 错误响应结构与 Node 版一致（401 的 message、code）。

只读参考：back/api/user.ts、back/services/user.ts、back/shared/{auth,password,loginThrottle,store}.ts
```

---

## M3 · 任务 CRUD

```
做 M3（任务 CRUD），按清单里「任务（Crontab）」27 条实现，不要做调度触发。

必做：
1. /api/crons 全部 CRUD、启停、置顶、标签、视图（/crons/views）。
2. 字段对齐：name/command/schedule/labels/sub_id/extra_schedules/task_before/task_after/log_name/allow_multiple_instances/work_dir。
3. 生成 data/config/crontab.list：格式 `<cron> <command>`；禁用、@once/@boot 等特殊规则行首加 "# "；command 带 `real_time=... no_tee=true ID=... log_name=... task x.js` 前缀（见 back/services/cron.ts 的 makeCommand/setCrontab）。
4. 日志读取：GET /api/crons/:id/log（offset/limit/tail/t）、GET /api/logs 列表与下载。
5. 运行实例：GET /api/crons/:id/instances、停止接口。

硬性验收：
- 前端任务页能完整操作（增删改查、启停、标签、视图）；
- 用同一批任务，Go 版生成的 crontab.list 与 Node 版逐字节 diff 为空；
- 分页结构 {data:[], total} 与 Node 版一致；
- 清单「响应」列标注为「文件流 / 流式 / 直接写 res」的接口按流处理，标注「410 存根」的只返回 410，不要真读文件。

只读参考：back/api/cron.ts、back/services/cron.ts、back/data/cron.ts
禁止：自己改 crontab 格式；自己加字段。
```

---

## M4 · 调度器（已完成）

```
做 M4（调度器），把 M3 的轻量执行器换成真正的定时触发；执行细节仍复用现有 runner 接口，M5 再替换。

必做：
1. 进程内调度：5 段 cron（* , - /）、extra_schedules 全部参与；不要 gRPC、不要写系统 crontab。
2. @boot：启动时取无参 crontabs() 全量，过滤 !isDisabled && @boot，按列表顺序执行一次。
   @once：Node 没有自动触发，不要实现"创建即跑"。
3. 并发：cronConcurrency 未配置时用 max(NumCPU,4)；定时队列与手动队列分开。
4. 单实例：allow_multiple_instances=0 时，status=running 且 pid 存活则跳过；pid 已死先回收再执行。
5. 触发时状态流转：running + pid + log_path；结束时 idle + last_execution_time + exit_code。
6. 时区：读系统配置 timezone（默认 Asia/Shanghai），用 time.LoadLocation 计算触发点，补 DST 用例。
7. 任务 CRUD/启停后调用 scheduler.Reload() 全量重排。
8. 不要加随机延迟（RandomDelay 属于 runner，M5 做）。

验收：
- 1 分钟后触发的任务误差 < 2s；extra_schedules 两条规则都能触发；
- 重启后任务不丢、不重复触发；@boot 只跑一次；
- 单实例任务运行中再次到点被跳过；并发上限生效；
- 与 Node 对同一批 cron 表达式（含 */5、1-5、0 0 1 * *、7 表示周日）计算的下次触发时间逐条一致。
```

**落地要点**（详见 `docs/compatibility.md` 4.6）

- 引擎 `internal/cronexpr`：`Parse` + `Next`，墙钟迭代，DOM/DOW 按 OR 拆两支并去重，`7`=周日。
- 调度 `internal/scheduler`：每条规则一个 timer goroutine；cron/manual 两个 FIFO 队列；`Reload` 全量重排；`Start` 时跑 `@boot`。
- 执行 `internal/service/cron_run.go`：`RunScheduled`（idle→running→idle，含单实例 CAS）、`RunQueued`（手动/`@boot` 的 queued 快照）、`Shutdown`（杀进程树）。
- 停机顺序：HTTP → 调度器 → 运行中任务。

---

## M5 · 任务执行器（最重要）

```
做 M5（任务执行器），替换 M4 的占位函数，真正把脚本跑起来。这是整个重写最容易出错的部分，先给设计再写代码。

必做：
1. 命令解析：task x.js、now、conc、desi、-m <秒>、-l、-- 之后的参数原样传给脚本。
2. 解释器直启（不要经过 shell）：.js/.mjs→node，.ts→ts-node，.py→python3（Windows 回退 python），.sh→bash，.go→go。
3. 环境注入：QL_DIR、QL_DATA_DIR、ID、log_name、real_time、no_tee、work_dir、task_before/task_after、NODE_PATH+=data/deps、PYTHONPATH+=preload、data/config/env.sh 里的变量。
4. 预加载：Node 用 --require 注入（让脚本能用 ql API / $done / 通知），Python 用 sitecustomize.py；语义对齐 shell/preload/。
5. 日志：data/log/<log_name>/<YYYY-MM-DD-HH-mm-ss-SSS>.log；首行 `## 开始执行... <时间>`，末行 `## 完成 ✅... <时间>  耗时 N 秒` 或 `## 失败 ❌(退出码 N)...`。
6. 状态回写：Crontab 状态/pid/log_path/时间、RunningInstance、CrontabStat。
7. 停止与超时：杀整棵进程树（Windows Job Object 或 taskkill /T /F），退出码和耗时写回。

硬性验收：
- JS、Python、TS、Shell 四类脚本都能跑；
- 同一脚本在 Node 版和 Go 版各跑一次，日志正文一致（只差时间戳）；
- 手动停止后任务状态回到 idle，无残留进程。

只读参考：shell/task.sh、shell/otask.sh、shell/share.sh、shell/preload/*
先输出：解析规则表 + 环境变量清单 + 日志格式样例，确认无误后再写代码。
```

---

## M6 · 脚本 / 环境变量 / 日志

```
做 M6（脚本、环境变量、日志），按清单里「脚本」13 条、「环境变量」14 条、「日志」5 条实现。

必做：
1. 脚本：列表（含子目录）、内容读写、上传、下载、重命名、删除、历史版本。
2. 环境变量：CRUD、启停、排序、批量导入导出、名称合法性校验、同名处理。
3. 日志：分页、按任务筛选、下载、清理策略（logRemoveFrequency）、retention 相关接口。

硬性验收：
- 前端脚本页能新建/编辑/保存/删除脚本，保存后 data/scripts 下文件内容正确；
- 环境变量改动后，下一次任务执行能读到新值；
- 日志页能按任务和日期筛选。

只读参考：back/api/{script,env,log}.ts 及对应 service
```

---

## M7 · 订阅 / 依赖

```
做 M7（订阅与依赖），按清单里「订阅」12 条、「依赖管理」11 条实现。

必做：
1. 订阅：public-repo / private-repo / single-file 三种类型；拉取、更新、日志、定时规则。
2. git 调用：clone/pull/reset --hard，代理与凭据处理；没有 git 时返回明确错误（不要崩）。
3. 依赖：nodejs / python / linux 三类；安装、卸载、重装、日志流式输出、镜像源配置。
4. 安装命令的 shell 语义（npm/pnpm/pip）与超时；Windows 下没有 bash 时的降级路径。

硬性验收：
- 拉取一个公开仓库成功，脚本落到 data/repo；
- 安装一个 npm 包、一个 pip 包成功，依赖列表状态正确；
- 机器上没有 git 时，接口返回可读的错误而不是 500。

只读参考：back/api/{subscription,dependence}.ts、back/services/{subscription,dependence}.ts
```

---

## M8 · 系统设置 / OpenAPI / 仪表盘 / 通知

```
做 M8（系统设置、OpenAPI、仪表盘、通知），按清单里「系统设置」26 条、「OpenAPI」6 条、「仪表盘」10 条、「升级」3 条实现。

必做：
1. 系统设置：基础配置、通知渠道、日志清理、代理、客户端 IP、存储保留（含 /system/client-ip/* 和 /system/storage-retention/*）。
2. OpenAPI：应用 CRUD、token 生成与作用域、/open/* 鉴权（App Token，不是 JWT）。
3. 仪表盘：概览、趋势、Top 时间/次数、运行态、标签、系统信息。
4. 通知：渠道配置、测试发送、通知审计日志。
5. 升级相关 3 条：可以返回"仅容器版支持"的结构，但字段要与 Node 版一致，不能 404。

硬性验收：
- 系统设置页保存后重启仍生效；
- 用应用 token 能调 /open/crons 并返回数据；
- 仪表盘各卡片有数据、不报错。

只读参考：back/api/{system,open,dashboard,update}.ts
```

---

## M9 · 兼容测试与打包

```
做 M9（兼容与打包），不再新增功能，只做验证和收尾。

必做：
1. 用真实 data/（Node 版跑过、含任务/脚本/订阅/日志）启动 Go 版，逐页点检。
2. 契约 diff：写脚本对 156 条路由各发一次请求，Node 版与 Go 版各存 JSON，递归比较键名和类型。
3. 任务双跑：同一批 js/py/ts/sh 脚本两边各跑一次，比较日志正文、退出码、耗时量级。
4. Windows 专项：路径含空格/中文、任务停止、Ctrl+C、长路径。
5. 打包：go:embed 前端、单 exe、config.yaml、start.bat（GBK）、README。
6. 输出《迁移说明》：数据目录兼容性、已知差异、回滚到 Node 版的步骤。

硬性验收：
- 156 条中高频接口（任务/脚本/环境/日志/系统）全部通过；
- 任务双跑日志一致；
- 单 exe 在未装 Node 的 Windows 上能启动面板（JS 任务需 node，缺失时提示清晰）。

只读参考：go-rewrite-prompt.md 第四节验收标准
```

---

## 收尾自检（每轮结束粘这段）

```
本轮结束，请自检并输出：
1. 对照本里程碑的验收标准，逐条打勾（完成/未完成/有偏差）；
2. 未完成项的原因和下一步计划；
3. 本轮新增/修改的文件清单（路径 + 作用）；
4. 与 Node 版行为不一致的地方（如果有，说明为什么）；
5. 下一里程碑开始前需要我确认或提供的东西。
```

---

## 纠偏话术

**它想改前端或改 schema 时：**
```
停。铁律：不改前端、不改 API、不改数据库 schema。请把改动限制在 Go 后端内部，重新说明你的方案。
```

**它想一次做完多个里程碑时：**
```
只做当前里程碑（Mx），不要提前实现后面的功能。做完按验收标准自检，等我确认后再进下一步。
```

**它开始猜字段/表名时：**
```
不要猜。请先读 back/api/<文件>.ts 和真实数据库的 sqlite_master，把实际字段/表名列出来，再写代码。
```

**它想用 sh -c 省事时：**
```
任务执行不允许经过 shell。必须用 exec.Command 直接起 node/python/ts-node/bash，自己处理环境、日志、超时和进程树。
```

**它写的代码跑不起来时：**
```
先不要加新功能。请复现问题：给出启动命令、完整报错、你怀疑的原因，然后只修这个问题。
```
