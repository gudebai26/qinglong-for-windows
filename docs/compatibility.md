# 兼容性清单（M9 收尾基线）

> 从 M1 开始维护：每确认一处与 Node 版的行为差异就追加一行，M9 据此产出《迁移说明》。
> 判定依据一律以 `back/` 源码为准，不凭印象。

## 1. 公开端点（M2 起不得加鉴权）

| 端点 | 依据 |
| --- | --- |
| `GET /api/env.js` | `express.ts:51`，挂在 `expressjwt` 之前 |
| `GET /api/static/*` | `express.ts:52-60`，`express.static` 在 `expressjwt`（`:79`）之前 → **公开**，上传文件靠它加载 |
| `GET /api/health` | `config.apiWhiteList` |
| `GET /api/system` | `config.apiWhiteList` |
| `POST /api/user/login`、`PUT /api/user/init`、`PUT /api/user/two-factor/login`、`PUT /api/user/notification/init` | `config.apiWhiteList` |
| `/open/auth/token`、`/open/user/login`、`/open/user/two-factor/login`、`/open/system`、`/open/user/init`、`/open/user/notification/init` | `config.apiWhiteList` |

`/api/static/*` 是最容易搞错的一条：它不是"还没做鉴权"，而是**设计上就公开**。M2 接 JWT 时若把它包进鉴权链，前端头像/上传文件会直接 401，属于回归。

## 2. 已确认与 Node 一致（回归项）

- `protectedPathCase`：`/api`、`/open` 下大小写变体返回 `400 {"code":400,"message":"Invalid path format"}`。
- `case sensitive routing` + `strict routing`：`/api/health/` 不匹配 `/api/health`（但鉴权在路由之前，未带 token 时先得到 401）。
- `/open/*` → `/api/*` 别名重写，两条路径命中同一 handler；重写发生在鉴权之后（`express.ts:215`），所以 `/open` 的 App Token 分支能看到原始前缀。
- CORS 预检：204，`Access-Control-Allow-Origin: *`，方法 `GET,HEAD,PUT,PATCH,POST,DELETE`。
- 全局删除 `req.query.t`。
- 数据库：`sqlite_master` 与 Node 建库逐行 diff 为空；`PRAGMA user_version` 仅 Go 使用（M1 置 1），Node 启动后保持 1。

## 2.1 响应契约（M2 起必须对齐，容易写错）

**HTTP 状态码不等于 body 里的 `code`**，四种情况要分开处理：

| 场景 | HTTP 状态 | body 结构 | 例子 |
| --- | --- | --- | --- |
| 成功 | 200 | `{code:200, data}` | 任务列表、登录成功 |
| 成功但无 data | 200 | `{code:200}` 或 `{code:200, message}` | `DELETE /api/system/log`、`PUT /api/user/init` |
| 业务错误 | **200** | `{code:4xx/5xx, message}` | 密码错误 400、限流 410、2FA 420、已初始化 450 |
| 参数校验（celebrate） | **400** | `{statusCode:400, error:"Bad Request", message, validation}` | 缺 `username`、`ip` 非法 |
| 鉴权失败 | **401** | `{code:401, message}` | 见下方三条文案 |
| `/api/*` 未命中 | **404** | `{code:404, message:"Not Found"}` | `GET /api/not-exist`（带 token） |
| 方法不匹配且非 GET/HEAD | **404** | Express 默认 HTML `Cannot <METHOD> <path>` | `POST /api/system` |
| 未捕获异常 | **500** | `{code:500, message}` | service 抛错 |

> 依据：`back/api/user.ts:63`（`res.send(data)`，业务错误也是 200）、`node_modules/celebrate/lib/celebrate.js:204-211`（`res.status(400).send({statusCode,error,message})`）、`back/loaders/express.ts:239-254`（401）与 `:277-290`（500）。

**这条如果写反，前端不会报错但所有业务提示都会失效**：前端拦截器按 `res.data.code` 判断，Go 若把业务错误写成 HTTP 400，axios 会走 catch 分支，提示文案与重试逻辑都会变。

鉴权失败的文案分三种，已实测确认（不要统一成一句）：

| 请求 | HTTP | message |
| --- | --- | --- |
| `/api/*` 缺 `Authorization` | 401 | `No authorization token was found`（express-jwt 原文，英文） |
| `/api/*` 头格式错误 | 401 | `Format is Authorization: Bearer [token]` |
| `/api/*` 签名错误/过期/算法不符 | 401 | `invalid signature` / `jwt expired` / `invalid algorithm` / `jwt malformed` / `invalid token` |
| `/open/*` 缺 token | 401 | `请先登录`（自定义中间件，走 i18n） |
| 签名有效但 token 不在 authInfo | 401 | `Token 已失效`（`/api` 与 `/open` 相同） |
| 黑名单 IP 登录 | 200 | `{code:403,message:"该 IP 已被列入黑名单"}` |

另外：`/api/system`、`/api/health` 在白名单里是**按路径匹配**，所以它们的任意方法都免 JWT（Node 的真实行为，不要按「GET 才免」实现）。但白名单只在**没有 token** 时放行：带了失效 token 访问 `/api/user/login` 仍会 401 `Token 已失效`。

## 3. 有意差异（M9 必须逐条复核）

| # | 差异 | Node 行为 | Go 行为 | 原因与影响 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 1 | 带扩展名的未命中静态路径 | `app.get('*')` 返回 `index.html` 200 | 返回 `404 text/plain` | 避免把 HTML 当 JS/CSS 返回、掩盖构建缺文件；只影响不存在的资源 URL | **保留**，M9 复核 |
| 2 | 响应压缩 | `compression()` 对文本 gzip | 已实现（M9），见 §3.1 A1 | 纯性能项，不影响契约 | 已补齐 |
| 3 | `/api/ws`（SockJS） | 实时日志/状态推送 | 已实现（M8） | 传输层 sockjs-go，话题与消息结构与 Node 一致；close code 用数字，Node 用字符串 | 已补齐 |
| 4 | `health.services.grpc` | gRPC worker 就绪状态 | 固定 `true`（调度器在进程内） | 字段结构不变，仅取值占位 | 保留 |
| 5 | 访问日志 | 仅记录慢请求（>1s） | 2xx→debug、4xx→warn、5xx→error、>1s→warn | 非契约，避免 syslog 被 200 刷满 | 已定 |
| 6 | `X-Powered-By` | `Express` | 不发送 | 安全头，无功能影响 | 保留 |
| 7 | `env.js` 显式空值 | `pick()` 按 key 是否存在判断，`.env` 里写 `QlBaseUrl=` 会输出空赋值 | 值为空即省略该行 | 空 `QlBaseUrl` 等价于未启用子路径，前端行为一致；仅逐字节比对时可见 | 保留，M9 复核 |
| 8 | 公网 IP 归属地 | ip2region 查询真实归属地 | 仅内网/回环返回「内网IP」，公网返回 `""` | 需要 11 MB 的 ip2region.xdb，未嵌入；影响 `lastaddr` 与登录日志展示，不影响鉴权 | 待定 |
| 9 | 通知渠道发送 | `testNotify` 按 type 调用真实渠道 | 已实现（M9），见 §3.1 A14 | 通知页保存先测试发送，成功才落库 | 已补齐 |
| 10 | `/api/static/<不存在文件>` | `express.static` 未命中 → 继续走鉴权 → 401 | 404 | M1 已定：静态目录按公开处理，避免前端把 404 当登录失效。**若前端存在「401 即跳登录」的逻辑，需复核缺失头像/上传文件时的表现**（Node 会误判为登录失效，Go 不会） | 保留，M9 复核 |
| 11 | token 全清时关闭 WS | 同步 `conn.close('401')` | 每秒重校验，1 秒内 `close(401)` | 前端不读关闭码，只走重连；延迟 ≤1s | 已补齐（M8） |
| 12 | 非法 JSON body | body-parser 原文（`Unexpected token ... in JSON at position ...`） | 固定 `Unexpected token in JSON` | 只有手工构造坏请求可见，前端始终发合法 JSON | 保留 |
| 13 | 限流响应头大小写 | `X-RateLimit-*` | `X-Ratelimit-*`（Go 规范化） | HTTP 头名不区分大小写，值完全一致 | 保留 |
| 14 | 新建脚本时的空 `path`（Windows） | `path.join(scriptPath, scriptPath)` → 403「暂无权限」 | 解析为 scripts 根目录 → 200 | Node 的 `path` 处理在 Windows 上把默认值再 join 一次，导致**脚本页在根目录无法新建/上传**；Go 按 Linux/Docker 下的实际行为实现（见 4.8.4） | 保留 |
| 15 | 脚本版本历史写入（Windows） | `preparedWrite` 以只读句柄 `fsync` → `EPERM` → 保存返回 500 | 正常写入并归档 | Node 在 Windows 上**完全无法保存带历史的脚本**（只读句柄 FlushFileBuffers 需要写权限）；Go 用可写句柄 + `Sync` | 保留 |
| 16 | 脚本树排序（非 ASCII 文件名） | `localeCompare` 走 ICU，中文按系统 locale 拼音排序 | ASCII 完全一致；非 ASCII 回退码点序 | 复刻 ICU 拼音需要整张排序表；只影响中文文件名在树中的先后 | 保留 |
| 17 | 下载响应头 | 额外 `ETag`/`Last-Modified`/`Accept-Ranges` | 只发 `Content-Type`/`Content-Disposition`/`Content-Length` | 浏览器另存不受影响；`Content-Disposition` 与 MIME 表已逐条对齐 | 保留 |


## 3.1 M9 收口 · M1–M8 有意差异汇总（每条标保留/待定）

> M9 用同一份 `data/` 跑完 Node↔Go 全量 A/B（`scripts/m9-contract-diff.py`，
> 156 条路由 × 正常+错误分支，310 组比较）。除下表外 **0 差异**；表中的都是
> 「有意保留」或「受本机环境限制」的项，均已在源码注释与迁移说明中留痕。

| # | 差异点 | 结论 | 判定 |
| --- | --- | --- | --- |
| A1 | 响应压缩 | Go 已补 `compression()` 等价中间件：JSON/文本/前端资源 gzip；`/api/ws`、`application/octet-stream` 流式/二进制与下载不压缩 | 保留（对齐 Node） |
| A2 | SockJS 原始 websocket 传输 | 之前 `/api/ws/.../websocket` 因中间件未转发 `http.Hijacker` 而 500；已修，`m8-ws-raw.js` 4/4、`m8-ws.js` 7/7 | 已补齐 |
| A3 | `health.services.grpc` | 调度器在进程内，恒 `true`（无独立 gRPC worker 就绪态） | 保留 |
| A4 | 公网 IP 归属地 | Node 用 ip2region(11MB) 查真实归属；Go 内网/回环返回「内网IP」，公网返回空。影响 `lastaddr` 展示，不影响鉴权 | 待定（未嵌入 xdb） |
| A5 | 升级/重载停机 | `PUT /api/system/reload`、`/api/update/reload|data`：Node 起 `ql reload` 由 supervisor 重启，Go 直接退出进程（单文件部署需外部守护，`start.bat` 未做循环拉起） | 待定（是否加守护循环） |
| A6 | 升级接口 | `update/system` 等：Node 起下载任务，Go 按 M8 决策保留「仅容器版可升级」空实现 | 保留 |
| A7 | 依赖安装（Node on Windows） | Node 把 `dependence-proxy.sh` 路径未加引号交给 bash，反斜杠被吞、安装必然失败；Go 正常安装。日志/状态值因此不同 | 保留（不复制缺陷） |
| A8 | 脚本版本历史（Windows） | Node 只读句柄 `fsync` → EPERM，保存/归档必 500；Go 可写句柄正常归档 | 保留（不复制缺陷） |
| A9 | 新建脚本空 `path`（Windows） | Node 根目录新建 403（join 两次）；Go 解析为根目录 200 | 保留（按 Docker 语义） |
| A10 | 脚本停止 / 订阅停止 / 命令停止 | Node `killTask` 起外部 `kill`，本机 `spawn EPERM`；Go 直接杀进程树 | 保留（本机环境） |
| A11 | `linux-mirror` / `command-run` | Node 脚本依赖 perl 且 linux-mirror 在本机不回包；Go 直接写配置/直启解释器 | 保留（本机环境） |
| A12 | `data/import` | Node 用 `tar` 子进程解包并回显命令结果；Go 进程内解包返回空串（效果一致） | 保留 |
| A13 | 订阅日志正文 | 依赖 git 输出与脚本语言，两进程必然不同；响应结构一致（M7 已单独验收） | 保留 |
| A14 | 通知配置保存 | Node `testNotify` 成功才落库；Go 已补齐（`NotifyService.UpdateConfig`），webhook 实收 | 已补齐 |
| A15 | pnpm 全局依赖可 require | `pnpm add -g` 后 NODE_PATH 每次任务重建，跨 pnpm 版本的多层 hash 目录均覆盖 | 已补齐 |
| A16 | 仪表盘聚合 / 日志目录列表 | 内容依赖本机任务执行结果（Node 侧脚本依赖缺失），仅结构可对照 | 保留（按 shape 比较） |
| A17 | 日志下载 404 响应体 | Node 走 Express 错误信封，Go 走 `ServeFile` 纯文本 404；状态码一致 | 保留 |


## 4. M2 鉴权与用户（已完成，17 条）

### 4.1 三条契约的落点

| 契约 | 实现位置 | 说明 |
| --- | --- | --- |
| JWT 只作用于 `/api/*` 且不在白名单 | `internal/httpapi/auth.go` | `/open/*` 走 App Token，非 `/api`、`/open` 前缀（含 `/api`、`/open` 本身）不校验；`/api/env.js`、`/api/static/*` 在鉴权链之前 |
| 签发 HS384、`data` 随机 50~100、20d/60d、写入 `authInfo.tokens[platform]`（unshift，上限 10）+ `authInfo.token` | `internal/auth/jwt.go`、`internal/service/user.go` | 上限取 `config.maxTokensPerPlatform`（10）；`JWT_EXPIRES_IN` 非空时覆盖 20d/60d，与 Node 一致 |
| 签名 + token 在 authInfo 双校验；platform 由 UA 判定 | `internal/auth/authinfo.go`、`internal/auth/platform.go` | UA 判定与 `getPlatform()` 逐分支对齐（windows/macos/x11/android/ios/openharmony/mobile） |

### 4.2 验收记录（2026-11-07）

- **契约 diff**：`scripts/contract-diff/run-diff.sh` 用同一份 Node 初始化的 `data/` 副本，对 Node 与 Go 各跑 145 步（含 17 条接口的正反例、2FA 全流程、退避 410、100 次限流 429），**状态码与响应体 0 差异**（token/时间戳/secret 归一化后逐字段比较）。
- **双向互通**：Node 初始化 `nodeadmin` → Go 登录成功；Go 初始化 `goadmin` → Node 登录成功，且 Node 接受 Go 签发的 JWT。
- **登出即失效**：`POST /api/user/logout` 后同一 token 立即 401 `Token 已失效`；改密码 / 开关 2FA 同样清空 `token` 与 `tokens`。
- **keyv 格式**：`{"value":{...},"expires":null}`，键名 `keyv:authInfo`；token 元素字段顺序 `value,timestamp,ip,address,platform`（无 `expiration`）。
- **密码**：Node 生成的 `scrypt$<32hex>$<128hex>` 在 Go 可直接校验（`internal/auth/password_test.go` 固化了一个 Node 向量）。

### 4.3 实现要点（避免后续里程碑踩坑）

- 鉴权链是**手写串联**的，不是 `chi.Use`：因为要插在公开端点与业务路由之间，且 chi 不允许路由后再 `Use`。
- 鉴权在**路由之前**，所以未知 `/api/*`：无 token → 401，有 token → 404 JSON；`/api/health/`（尾斜杠）无 token 也是 401。
- 业务错误一律 HTTP 200（见 2.1），`Reply.MessageSet` 用来输出 `{"code":420,"message":""}` 这种空文案。
- 参数校验按 celebrate 结构手写（`internal/httpapi/validate.go`），字段顺序、`keys`、`message` 拼接方式与 Joi 对齐。
- 两套限流并存：`RateLimiter`（express-rate-limit，100/15min → 429 纯文本 + `X-RateLimit-*`）与 `LoginThrottle`（连续失败退避 → 410 + `data` 秒数）。
- **登录通知是 fire-and-forget**：`notifyLoginAsync` 用 `context.WithoutCancel` + goroutine，登录响应不等通知、通知失败也不改登录结果（对齐 `user.ts:171` 未 await 的 `notify(...)`）。
- 登录日志保留 100 条（按 `info.timestamp` 倒序裁剪），通知审计保留 200 条（按 id），与 Node 的裁剪时机一致。

### 4.4 已复核的两处“看起来不一致”的点

**限流响应头是 `X-RateLimit-*`，不是 `RateLimit-*`。** 实测（`curl -D -`，第 101 次登录）：

```
X-RateLimit-Limit: 100
X-RateLimit-Remaining: 0
X-RateLimit-Reset: 1794028586
Retry-After: 895
Content-Type: text/html; charset=utf-8
```

原因在 `node_modules/express-rate-limit/dist/index.cjs`：`standardHeaders` 默认 `false`（`:599`），`legacyHeaders` 默认 `true`（`:608`），而 `back/api/user.ts` 只传了 `windowMs`/`max`，没有开 `standardHeaders`。所以 `RateLimit-Limit`/`RateLimit-Policy`/`RateLimit-Remaining`/`RateLimit-Reset` **不会出现**；Go 侧照 `X-RateLimit-*` 实现才是对齐。将来若 Node 打开 `standardHeaders: 'draft-7'`，Go 需同步加头。

**429 文案**：`Too many requests, please try again later.`（42 字节，无换行，`text/html; charset=utf-8`），已与 Node 逐字节一致。

## 4.5 M3 任务 CRUD（已完成，27 + 5 条）

### 4.5.1 覆盖范围

- `back/api/cron.ts` 的 27 条全部实现：列表/详情/创建/更新/删除、启停、置顶、标签、视图 7 条、日志、导入、状态回写、实例列表/停止。
- `back/api/log.ts` 的 5 条：`/api/logs` 列表、`/api/logs/detail`、`/api/logs/:file`（410 存根）、删除、下载。
- 启动 `Bootstrap` 复刻 `back/loaders/initData.ts` 的任务部分：确保系统视图「全部任务」、所有任务 `status=1`、残留 `RunningInstances` 置为 `stopped`。
- 为了让任务页在 M8 前可打开，补了 3 个最小系统接口与 1 个订阅桩（见 4.5.4）。

### 4.5.2 验收记录（2026-11-07）

- **crontab.list 逐字节**：同一份 Node 数据副本，两边各跑 24 步场景（创建含 `extra_schedules`/`task_before` 引号换行/`work_dir`/`log_name`、更新、置顶/取消、加删标签、禁用/启用、删除、视图增改移删），最终文件 `diff -u` **为空**。覆盖 `# ` 前缀的四种来源：`isDisabled=1`、`@once`、`@boot`、`isNodeCron`（DOM+DOW 同时非 `*`、`extra_schedules`、含 `7` 的 DOW、DOM 步进）。
- **契约 diff**：24 步正常响应 + 18 个错误用例（缺字段、非法 schedule、`extra_schedules` 字符串数组、越权 log_name、重复任务 409、未知 id 500、非法数组 400、日志 query 校验、410 存根），归一化 `timestamp/createdAt/updatedAt/time/createTime` 后**状态码与响应体 0 差异**。
- **分页**：`page=1&size=3`、`page=2&size=3`、`page=0&size=0`（全量）、`searchValue`、`filters={"status":[...]}`、`queryString`（视图过滤 + 排序）逐字段一致。
- **日志 6 字段**：`{code,data,logStatus,offset,nextOffset,total,truncated}` 在 `tail=true`、`offset+limit`、UTF-8 截断三种情况下与 Node 完全一致；`/api/logs/detail` 同样，且 `logStatus` 仅在存在运行实例时出现。
- **前端**：无头 Edge 打开 `/crontab`，任务表 10 行、状态、下次运行时间、视图切换均渲染，无 JS 异常；日志弹窗走 REST 轮询可读到分块内容。
- **实例/停止**：`PUT /api/crons/status` 创建实例、`GET /api/crons/:id/instances` 返回 `raw:true` 时间格式、`POST .../instances/:id/stop` 杀进程并写 `status=2/exit_code=143`、`PUT /api/crons/stop` 批量停止，均已实测。

### 4.5.3 实现要点（后续里程碑必须沿用）

- **行序**：`crontab.list` 的行序 = 无参 `crontabs()` 的默认排序 `isPinned DESC → isDisabled ASC → status ASC → createdAt DESC`，不是 id；`page=0/size=0` 必须返回全量，不能套默认分页。
- **前缀**：`real_time=false no_tee=true ID=<id> [log_name=...] [task_before='...'] [task_after='...'] [work_dir='...'] `，命令未以 `task ` / `ql ` 开头时补 `task `；`task_before/after` 的 `'` 转义为 `'\''`，换行（`;? *\n`）压成 `;`；`work_dir` 只转义引号。
- **写文件后**：`Crontabs.saved=1` 且所有行 `updatedAt` 被 Sequelize 批量更新刷新；创建/更新接口返回的是刷新前的对象，所以 `saved=false`。
- **`log_name`**：创建时 id 尚未生成，`getUniqPath` 不带 `_id`；更新时带 `_id`。`/dev/null` 原样保留；相对名在日志目录内则保留，否则回退到 `getUniqPath`。
- **视图位置**：`initPosition=4.5e15`，新建取当前最小位置的一半；`move` 取相邻位置中点，越界时整体重排（步长 1e10）；`position` 用 16 位有效数字。
- **日志分块**：默认 256KB、最大 1MB、最小 4 字节；`tail`/无 offset 从末尾读；跳过 UTF-8 续字节并回退不完整码点；`truncated = offset>0 || nextOffset<total`。
- **实例时间**：`GET /api/crons/:id/instances` 走 `raw:true`，`createdAt/updatedAt` 返回数据库原文（`YYYY-MM-DD HH:mm:ss.SSS +00:00`），不要转 ISO。
- **错误契约**：业务错误 HTTP 200 + `{code,message}`；celebrate 校验 HTTP 400 + `{statusCode,error,message,validation}`；唯一约束 HTTP 409；`getDb` 未命中 HTTP 500。数组 body 校验失败的 `keys` 是 `[""]`，`extra_schedules` 元素错误是 `extra_schedules.<i>`。
- **手动运行**：M3 只做轻量执行器（`shell -c` + 日志文件 + `RunningInstances` 状态机），命令解析、预加载、`task_before/after` 语义留给 M5；M5 替换 `runSingle` 时保留状态机与实例字段。

### 4.5.4 M3 有意差异 / 临时桩

| # | 项 | Node | Go（M3） | 计划 |
| --- | --- | --- | --- | --- |
| 1 | `GET /api/subscriptions` | 完整订阅列表 | 固定 `{code:200,data:[]}` | M7 替换；任务页刷新会无条件读它，404 会导致列表不渲染 |
| 2 | `GET /api/system`、`/api/system/config`、`PUT /api/system/config/lang` | 完整系统设置 | 最小实现（`isInitialized/version`、Auths 行、写 `info.lang`） | M8 补齐 |
| 3 | `GET /api/system/update-status` | 真实升级任务状态 | 固定 `idle` | M8 替换 |
| 4 | `/api/dashboard/*`、`/api/ws/*` | 仪表盘 / SockJS 实时推送 | 404 / 401 | M8；任务页日志走 REST 轮询不受影响 |
| 5 | `GET /api/crons/:id/logs` 的 `time` | `birthtimeMs` | `mtime` 毫秒 | Windows 上两者通常相同；仅排序/展示用 |
| 6 | 系统 crontab | `crontab <file>`（system 模式） | 不调用 | M4 单进程调度，不需要系统 crond |
| 7 | `cronClient.readiness.ensureReady()` | 每次写操作前 gRPC 健康检查 | 无 | Go 单进程版没有 gRPC 层 |
| 8 | 手动运行执行器 | `shell/task.sh` + 预加载 + 环境注入 | `shell -c` 直接执行 `makeCommand` | **M5 已替换**（见 4.7） |

## 4.6 M4 调度器（已完成）

### 4.6.1 覆盖范围

- **进程内调度**：不启 gRPC、不写系统 crontab；`crontab.list` 仍按 M3 规则生成（供外部工具与回滚使用），但不安装到 crond。
- **表达式**：5 段与 6 段（带秒）、`*` `,` `-` `/`、`?`（仅日期字段）、`#N`、`JAN-DEC`/`SUN-SAT`、`@yearly…@minutely` 别名；`7` 与 `0` 都表示周日。
- **日期语义**：DOM 与 DOW 同时受限时是 OR（Vixie 语义）。等价于 Node 的“拆两条 pattern + `accepts()` 去重”，连 `dayCount` 与固定 `monthDays`（2 月按 29 天）的比较也照搬。
- **extra_schedules**：每条独立注册、独立触发，与主规则并行；同秒都命中时按两条触发处理（单实例任务第二条会被跳过）。
- **`@boot`**：启动时取无参 `crontabs()` 全量，过滤 `!isDisabled && @boot`，按列表顺序走 `run(ids)`：一次 `MarkQueued`（同一个 token）+ 手动队列逐条执行。`Reload` 不会重跑。
- **`@once`**：不自动触发，也不删除——与 Node 一致（早期计划文档写的“执行后删除”是错的）。
- **并发**：定时队列与手动队列**分开**；上限取 `systemConfig.info.cronConcurrency`，未配置时 `max(NumCPU,4)`。
- **单实例**：`allow_multiple_instances=0` 时，`status=running` 且 pid 存活则跳过；pid 已死先把行回收成 `idle/pid=NULL`、残留实例置 `stopped`，再执行。
- **状态机**：定时触发 `idle → running`（写 `pid`/`log_path`/`last_execution_time`），结束回 `idle`（`last_execution_time`=开始时间、`last_running_time`=耗时秒、`exit_code` 写 `RunningInstances`），语义对齐 `shell/api.sh:update_cron`。
- **时区**：取 `systemConfig.info.timezone`（默认 `Asia/Shanghai`），`time.LoadLocation` 计算触发点；二进制内嵌 `time/tzdata`，Windows 无 zoneinfo 也能用。
- **变更重排**：create / update / delete / enable / disable 后调用 `scheduler.Reload()` 全量重排。
- **停机**：先停调度器（定时器 + 队列），再结束运行中任务的进程树并等待状态回写，避免“面板停了任务还在跑”。

### 4.6.2 验收记录（2026-11-07，真实 `data/` + Windows）

| 项 | 结果 |
| --- | --- |
| 1 分钟后触发 | 13:52:36 创建 `53 13 * * *`，日志文件名 `2026-11-07-13-53-00-000.log`，误差 < 1s |
| extra_schedules | 主 `*/5` + 附加 `*/7`，90s 内 56 次触发、56 个独立日志文件 |
| 单实例 | `*/2` + 5s 任务：24s 内启动 5 次，其余 7 次日志 `skip active single-instance task` |
| 并发（默认） | NumCPU=8 → 峰值 8（4 个 `* * * * * *` × 3s 任务） |
| 并发（配置） | `cronConcurrency=2` → 峰值 2 |
| `@boot` | 两次进程启动各 1 次；`Reload` 后不再触发 |
| 重启 | 任务保留；13:53 的分钟任务没有重复执行 |
| cron 一致性 | 66 组（表达式 × 起始时刻）与 Node 逐条一致；另用 node-cron 引擎交叉验证 7 条 |
| DST | 春令 02:30 → 03:30（与 cron-parser 一致）；秋令 01:30 只触发一次；上海无 DST |

### 4.6.3 实现要点

- 引擎在 `internal/cronexpr`：`Parse` 出字段位图 + `dayCount` + `nthDay`，`Next` 在**墙钟空间**迭代（carrier 用 UTC 承载日历字段），最后用 `time.Date` 落到面板时区。
- 三种日期模式：`modeDayOfMonth`（DOW 覆盖全周，忽略 DOW）、`modeDayOfWeek`（DOM 覆盖全月，忽略 DOM）、`modeEither`（两条分支 OR + 去重）。
- 队列：FIFO + 信号量，`Submit` 不阻塞触发协程；`Close` 丢弃未开始的作业。
- 每条规则一个 goroutine + `time.Timer`；触发后按当前时间重算下一次。
- 日志文件名用 `O_EXCL` 抢占，同毫秒冲突自动加 `-1`/`-2` 后缀——两条规则同秒触发时不会共用一个日志。
- 定时触发的命令是 `MakeCommand(row, false)`（不带 `no_delay`），手动运行仍是 `no_delay=true` + `real_time=true`，把 `RandomDelay` 完整留给 M5。

### 4.6.4 M4 有意差异

| # | 项 | Node | Go（M4） | 说明 |
| --- | --- | --- | --- | --- |
| 1 | 秋令重复小时 | cron-parser 对固定时刻只跑一次，但每分钟规则会在重复小时再跑一遍 | 墙钟匹配，重复小时只跑一遍 | 满足“不重复”；代价是每分钟规则该小时少跑一遍 |
| 2 | 迟到执行 | node-cron `missedExecutionTolerance=5000`：5s 内补跑，超过丢弃 | 定时器晚到就执行一次，然后按当前时间重排 | 不补跑多个错过的点；睡眠/挂起后不会雪崩 |
| 3 | `cronConcurrency` 运行时变更 | `setConcurrency` gRPC 立即生效 | `Reload`/重启时重读 | M8 保存系统设置后需调用一次 `Reload` |
| 4 | 系统 crontab | system 模式把可移植规则交给 crond | 全部进程内调度 | 按 M4 指令；`crontab.list` 仍生成 |
| 5 | 定时触发日志命名 | `task.sh` 自己按 `real_log_path` 写 | M3 执行器按毫秒命名 + `O_EXCL` 去重 | **M5 保留该命名**（runner 按 `real_log_path` 写，不重新推导） |

## 4.7 M5 任务执行器（已完成）

### 4.7.1 覆盖范围

`internal/runner` 替换 M4 的“只起进程”占位实现（`shell -c` 执行 `makeCommand`），调度与状态机不动。

- **命令解析**（`parse.go`）：`task <file>`、`now`、`conc`、`desi`、`-m <秒>`、`-l`、`--` 分隔脚本参数；命令首词 `task`/`ql`（或 `*/task.sh`）自动剥离；`NAME=value` 前缀按 shell 赋值解析；引号（单/双/转义/相邻拼接）自写分词，**不 eval**。
- **解释器直启**：`.js/.mjs→node`、`.ts→ts-node-transpile-only|ts-node`、`.py/.pyc→python3|python`、`.sh→bash`、`.go→go run`；`run_else` 形态（`task node x.js`）直接 exec 首词。全程 `exec.Command`，不经过 shell。
- **环境**：`QL_DIR`、`QL_DATA_DIR`、`ID`、`log_name`、`real_time`、`no_tee`、`real_log_path`、`work_dir`、`task_before/after`、`envParam/numParam`，外加 `share.sh` 的 `dir_*`/`file_*`/`cmd_task` 全套；`NODE_PATH += data/deps(:/node_modules) + pnpm 全局`；`PYTHONPATH = preload:config[:prev]`；`PYTHONUNBUFFERED=1`、`QL_LANG`、`env.sh` 变量。
- **预加载复用**：`shell/preload` 原样 embed（`sitecustomize.js/py`、`client.*`、`__ql_notify__.*`、`esm-loader.mjs`、`notificationAudit.js`、`env.*`），首启落地到 `<root>/shell/preload`；js/ts/mjs 走 `NODE_OPTIONS=-r <preload>`，py 走 `PYTHONPATH`。同时落地 `back/protos/api.proto`（见 4.7.3）。
- **日志**：`handle_log_path` 三级优先（`real_log_path` > `log_name` > 由脚本路径推导，含 `_<ID>` 后缀），三态重定向（默认 tee / `no_tee` 只写文件 / `real_time` 只写 stdout 通道 / `/dev/null` 丢弃）；首尾格式逐字符对齐（`## 开始执行... <ts>\n\n`、`\n## 完成 ✅... <ts>  耗时 N 秒　　　　　\n`，失败/停止各一版）。
- **超时**：`CommandTimeoutTime` 支持 `s/m/h/d`、小数、指数；到期 SIGTERM 进程组 → 0.5s → SIGKILL；退出码 124；非法值打印 `Invalid task timeout: <v>` 并退出 125。
- **随机延迟**：仅“单参数 + `real_time!=true` + `no_delay!=true` + `RandomDelay>0`”生效；`RandomDelayFileExtensions` / `RandomDelayIgnoredMinutes` 用 `${VAR-default}` 语义（键存在但为空表示“不过滤/不忽略”，只有键缺失才用默认 `js` / `0 30`）。
- **延迟期间的状态与停止**：任务在触发瞬间就写成 running（pid 为空）+ log_path + last_execution_time，与 Node `handle_task_start` 一致；`Stop` 在延迟期间立即把行置回 idle，runner 的等待是可中断的（100ms 轮询 stopped），不会在几分钟后“诈尸”启动。
- **钩子**：js/py/ts 由预加载执行 `task_before`；sh 及其他由 runner 用 bash `source task_before.sh` + `eval`，并把钩子进程的 `env -0` 回灌进任务环境（等价于 shell 里同进程 source）；`task_after` 对所有类型都执行（与 `otask.sh` 一致）；无 bash 时跳过并 WARN。
- **状态**：沿用 M4 状态机（running/pid/log_path → idle/last_execution_time/last_running_time），实例行写 `exit_code`；新增 `CrontabStats` 日聚合（`run_count/success_count/fail_count/total_time/max_time`，对齐 `POST /open/dashboard/record`）。
- **停止**：`runner.Stop(id)` 标记 stopped → SIGTERM 进程组 → 硬杀兜底；尾注 `## 已停止 🛑`，退出码 143，实例状态 `stopped`（不是 `error`）。

### 4.7.2 验收记录（2026-11-07，Windows + 真实 Node 版对照）

同一份 `data/`、同一批脚本，Node `shell/task.sh` 与 Go runner 各跑一次，日志去掉时间戳/耗时/绝对路径后 `diff -u`：

| 用例 | 结果 |
| --- | --- |
| `task body.js` / `body.py` / `body.sh` / `body.ts` | **8/8 逐字节一致** |
| `task body.js -- --flag value`（`--` 透传） | 一致 |
| `task body.js now` | 一致 |
| `task_before='export HOOK=...'`（js，走预加载） | 一致 |
| `task_before` + `task_after`（sh，走 bash） | 一致 |
| `-m 1` 超时 | 退出码 124，尾注 `## 失败 ❌(退出码 124)` |
| `-m abc` 非法超时 | 退出码 125，日志含 `Invalid task timeout: abc` |
| `RandomDelay=2`（定时路径） | 出现“任务随机延迟 N 秒…”且实际等待；手动路径不延迟 |
| 停止 | `## 已停止 🛑`、实例 `stopped/143`、进程树无残留、任务回 `idle` |
| 面板 E2E（HTTP 建任务→运行→停止） | 4 类脚本 + 超时 + 非法超时 + 停止共 28 项断言全绿 |

Go 侧测试：`internal/runner`（解析/超时/日志路径/延迟规则/环境 + 真实进程执行）与 `internal/service`（状态机、统计、停止）共 20 余个用例；`.tmp/compare.sh` 可重跑 Node↔Go 日志对比。

### 4.7.3 实现要点（后续里程碑必须沿用）

- **命令首词**：`crontab.list` 的行首是 `task`（或 `ql`），shell 会把它当命令解析；Go 没有这层，所以 `ParseCommand` 先剥掉首词，其余参数才等价于 `task.sh` 的 `"$@"`。
- **分词而非 eval**：`task_before='a;b'` 这类赋值必须原样成为环境变量值，任何 `sh -c` 都会把它变成代码执行。分词器只处理空格、单引号、双引号、反斜杠与相邻拼接。
- **`real_time` 语义**：Node 里它表示“不重定向，输出到 stdout”，由面板捕获后写日志；Go 侧 runner 始终写调用方给的 sink（面板传的就是日志文件），观感与落盘结果一致。
- **日志文件归属**：`openTaskLog` 仍用 `O_EXCL` 抢名（同毫秒两条规则不共用文件），runner 以 append 打开同一个路径，绝不截断。
- **预加载探测**：`NODE_OPTIONS` 只在 `node -e "require(<preload>)"` 干净退出时注入（预加载在 `require` 阶段就会 `process.exit(1)`，缺依赖会杀死每个任务）；Python 侧同理，探测失败只 WARN 一次，任务照跑。
- **`QL_SHELL`**：预加载用 `process.env.QL_SHELL || cmd.exe` 执行钩子；Windows 上 cmd.exe 没有 `source`，所以 runner 把解析到的 bash 写进 `QL_SHELL`（Unix 下等价于默认值）。
- **`back/protos/api.proto`**：`client.js` 在 require 阶段 `loadSync(protoPath)`，缺文件直接 `exit(1)`；Go 面板把同一份 schema 落到 `<root>/back/protos/api.proto`（缺失时才写）。
- **`NODE_PATH` 分隔符**：Node 的 shell 版本用 `:` 拼 `NODE_PATH`，Windows 上盘符里的冒号会把路径切碎；Go 用 `os.PathListSeparator`，并额外加入 `data/deps/node_modules`，让 `npm i --prefix data/deps` 的布局也能解析。
- **钩子变量回灌**：`task_before` 里 `export` 的变量必须对任务可见。Go 在钩子 bash 末尾执行 `env -0` 落到临时文件，解析后并入子进程环境；探测用的钩子文件是空文件，避免启动时执行用户代码。
- **conc/desi**：js/py 只传 `envParam`/`numParam` 交给预加载拆分（与 Node 一致），其他类型直接把选中的账号值写进环境变量；并发子进程各写独立日志文件，结束后按序拼进任务日志再删除。
- **统计落库**：`CrontabStats` 以 `(ref_id, date)` 唯一键 upsert，`total_time/max_time` 单位毫秒，`exit_code==0` 记成功。

### 4.7.4 M5 有意差异

| # | 项 | Node | Go（M5） | 说明 |
| --- | --- | --- | --- | --- |
| 1 | `.sh` 无参时的 `$1` | `. "$@"` 让脚本看到自己的文件名 | 同样把文件名作为第一个参数传入 | 为对齐日志刻意保留该可观察行为 |
| 2 | `.go` 脚本 | 不支持（按 `run_else` 直接执行文件） | `go run <file>` | 计划文档要求 `.go→go` |
| 3 | `-l` | 解析后无实际作用 | 解析后仅 debug 日志记录路径 | 与 Node 行为一致 |
| 4 | `conc/desi` 账号变量为空 | 静默跑 0 个子进程 | 日志提示“环境变量 X 没有可运行的账号”后退出 0 | 只在配置错误时可见 |
| 5 | 随机延迟期间的状态 | `handle_task_start` 先写 running/pid/log_path，再 sleep | 同样先置 running（pid 为 NULL）、写好 log_path 与 last_execution_time，延迟结束再补 pid | 延迟可达数分钟，若仍显示 idle 会被误判为“任务没跑”；`Stop` 在延迟期间也会立即生效 |
| 6 | Python 预加载探测失败 | Windows 上 `PYTHONPATH` 用 `:` 拼接，预加载从未被 import | 用 `;` 正确注入；平台跑不起来时（如缺 `/tmp`）跳过并 WARN | 任务日志因此比 Node 多一条 WARN 之外的差异为零 |
| 7 | `ql` API 调用 | 走 gRPC:5500 | 预加载可加载，但 Go 侧未实现 gRPC 服务，调用会连接失败 | 与 M4“不启 gRPC”一致，M8/后续按需补 HTTP 桥 |

## 4.8 M6 脚本 / 环境变量（已完成，13 + 14 条）

### 4.8.1 覆盖范围

- **脚本 13 条**（`back/api/script.ts`）：`GET /api/scripts`（无 `path` 递归、有 `path` 单层）、`/scripts/detail`、`/scripts/history`、`/scripts/history/detail`、`/scripts/history/restore`、`GET /scripts/:file`（410 存根）、`POST/PUT/DELETE /scripts`、`/scripts/download`、`/scripts/run`、`/scripts/stop`、`/scripts/rename`。
- **环境变量 14 条**（`back/api/env.ts`）：列表、批量创建、更新、批量删除、`/:id/move`、`disable`/`enable`、`/name` 批量改名、`GET /:id`、`pin`/`unpin`、`POST/DELETE /labels`、`/upload`。
- **历史目录**：`data/script-history/<sha256(相对路径)>.br`，**不落在 `data/scripts`**（否则会被脚本树列出，且改名/删除后历史丢失）。`PUT /api/scripts` 同时写脚本与历史（`ScriptService` + `ScriptHistoryService`）。

### 4.8.2 验收记录（2026-11-07，Windows + 真实 Node 版对照）

`python .tmp/m6/m6diff.py`：

| 项 | 结果 |
| --- | --- |
| 脚本/环境变量请求序列（同一份 data/，Node 与 Go 各跑一遍） | **58 步、0 差异**（状态码、body 逐键、`content-type`/`content-disposition`/`content-length`） |
| `data/scripts` 目录树 | 路径、大小、SHA-256 全等 |
| `shell/preload/env.sh`、`env.js`、`env.py` | 逐字节相同（含 `'` 与 `\` 的转义） |
| 历史归档 | 解压后 `JSON.parse → JSON.stringify` 原样，重新用 zlib(q4/lgwin22) 压缩**字节相同** |
| Node 读 Go 写的归档 | `history` 列表与 `history/detail` 与 Go 返回完全一致（格式双向兼容） |
| 路径穿越 / 黑名单 | `../`、`../../config/crontab.list`、`auth.json`、`env.sh`、`crontab.list`、`token.json`、`config/grpc/*` 全部 403（与 Node 一致） |

`python .tmp/m6/e2e.py`（Go 单跑，28 项断言全绿）：

- 脚本页主流程：根目录新建 → 详情 → 保存两次（历史 2 版）→ 历史详情 → 恢复 → 删除；历史文件在 `data/script-history`，`data/scripts` 里没有 `.br`。
- `PUT /scripts/run` 返回 pid、建出 `data/log/<name>.swap` 目录；`/stop` 后进程结束、`.swap` 调试副本被删除。
- **环境变量生效链路**：新建 `M6_TOKEN=first` → 手动跑 `task envcheck.js` → 日志出现 `M6_TOKEN=first`；改成 `second` → 再跑 → 日志出现 `M6_TOKEN=second`。

Go 侧测试：`internal/service/script_test.go`（路径解析/黑名单/穿越/历史生命周期/乐观锁/超限/归档格式/根目录新建/树过滤）、`internal/service/env_test.go`（保留名、`toPrecision(16)`、位置与排序、重复 name 合并、停用跳过、三文件转义）。

### 4.8.3 实现要点（M7+ 沿用）

- **路径安全**：`internal/service/fsaccess.go` 逐条移植 `back/shared/fileAccess.ts` —— `resolvePath`（Node `path.resolve` 语义，`/x` 重挂当前盘）、`pathRelative`/`isWithinPath`（Windows 大小写不敏感）、黑名单按路径段小写匹配（含子孙）、`realpath` 后二次校验、**悬空软链接按存在处理并拒绝**、含 `\0` 直接拒绝。`isPathAllowed`/`existingWritableRealPath` 对应 `script.ts` 的两个包装（写根 `[data/config, data/scripts]`，只有 `data/config` 套 `blackFileList`）。
- **历史格式**：`{"format":1,"path":"<相对路径>","versions":[{id,createdAt,source,hash,content}],"head":{hash,createdAt,source}}`，键序固定；`source ∈ baseline|save|restore|external`；只存**改动前**的内容，`head` 仅在其 hash 仍等于文件时用于标注下一个快照。
- **乐观锁**：`restore` 与 `skipHistory` 必须带 64 位 hex `expectedHash`，不匹配 → 409「文件已变化…」；`content === current` 直接返回 `{changed:false}`（不写盘、不记历史）。
- **skipHistory 的真实语义**（容易误解，M9 不要当 bug 改）：它是 **413 逃生阀，不是「跳过归档」**。
  - 413 分支（当前文件 >1 MiB、含 `\0` 或非 UTF-8）：`!skipHistory` → 抛 `HistoryUnavailableError`（413 + `currentHash`）；带了 `skipHistory` → 直接写文件、**不归档**、返回 `historyRecorded:false`。
  - 普通文件分支：`skipHistory` **只增加 `expectedHash` 强校验**（不匹配 409），随后照常归档并返回 `historyRecorded:true`。
  - 依据 `back/shared/scriptHistory.ts:230-268`；前端 `saveWithHistory.ts` 只在 413 弹窗后才带该字段，所以用户可见行为仍是「不记历史」。
- **同目录的 `lang_env.sh`**：`shell/preload/lang_env.sh` 由 `back/api/system.ts:655` 写入（语言切换），M6 不碰；**M8 做语言切换时必须一起写**，否则 `share.sh:81` 的 `. "$dir_preload/lang_env.sh"` 读到的还是旧语言。
- **原子写**：`preparedWrite` 先写同目录 `.ql-save-<uuid>.tmp`（`O_EXCL` + `Sync`）再 `rename`，保留原 mode；归档先发布、脚本后提交，提交失败回滚归档。配额：20 版本 / 单文件 1 MiB / 归档 2 MiB / 解压 8 MiB / 总量 32 MiB / 1000 文件。
- **Brotli 参数**：quality 4、lgwin 22、generic 模式 —— 与 Node `zlib.brotliCompress` **字节一致**（已用 `andybalholm/brotli` 实测），所以两版可以互读对方的归档。
- **`set_envs`**：按 `COALESCE(isPinned,0) DESC, position DESC, createdAt ASC` 取 `status=0 且 name 非空` 的行，同名用 `&` 合并，写 `shell/preload/env.sh|env.js|env.py`（`share.sh` 的 `file_env=$dir_preload/env.sh`，**不是** `data/config/env.sh`）。保留名（`PATH`/`ID`/`QL*`/`NODE_*`…）跳过并 WARN。
- **位置算法**：新行 `position = 上一行 position - 1e10`（首行 `4.5e15`）；`move` 取邻居中值；`toPrecision(16)` 后 `trunc`；越界或与边缘距离 <100 时按当前顺序整体重排为 `4.5e15 - 1e10*i`。
- **名称唯一性**：数据库唯一约束是 `UNIQUE(value, name)`，同名不同值是合法的（多账号）；重复插入返回 409「资源已存在，请检查重复的名称或值」。
- **multipart**：脚本上传按 multer `diskStorage` 落 `<root>/.tmp/<uuid>` 再拷贝（不留半成品）；env 导入按 `memoryStorage` 5 MiB 上限读入内存。
- **下载**：`Content-Disposition: attachment; filename="…"`（非 ASCII 追加 `filename*=UTF-8''…`），MIME 表按 `mime-types` 实测值硬编码（`.js→application/javascript; charset=UTF-8`、`.ts→video/mp2t`、`.py→application/octet-stream`…）。
- **调试运行**：`real_time=true task <相对路径> now`，`.swap` 副本保留扩展名（`x.swap.js`），日志目录因此是 `data/log/x.swap`（正是 `stop` 三秒后清理的路径）；`real_time` 不写日志文件，输出待 M8 的 `/api/ws` 送到前端。

### 4.8.4 M6 有意差异

| # | 项 | Node | Go | 说明 |
| --- | --- | --- | --- | --- |
| 1 | 空 `path` 新建/上传（Windows） | 403 | 200（落到 scripts 根） | Node `path.join(scriptPath, scriptPath)` 的 Windows 缺陷；Go 按 Linux 行为实现，否则前端脚本页在根目录无法新建 |
| 2 | 保存脚本（Windows） | 500 `EPERM: fsync` | 正常 | Node `preparedWrite` 用只读句柄 `fsync`；Go 用可写句柄 |
| 3 | 归档跨进程锁 | `proper-lockfile` | 进程内按文件互斥 | 面板单实例；Go 不与 Node 同时写同一 data/ |
| 4 | 脚本树非 ASCII 排序 | ICU 拼音（随系统 locale） | 码点序 | 仅影响中文文件名先后 |
| 5 | 下载响应头 | 含 `ETag`/`Last-Modified` | 不含 | 另存行为一致 |

## 4.9 M6.5 · gRPC ql API（`internal/grpcapi`）

实现细节见 `docs/grpc-api.md`。与 Node 的对照结论：

### 4.9.1 已逐条对齐

- proto 原样（`proto/api.proto` 与 `back/protos/api.proto` 字节相同），字段号不变。
- 两种错误模式分开：`GetEnvs`/`GetCrons` 用 body 里的 `code:500`，其余 17 个用 gRPC error。
- `GetEnvs`/`GetCrons` 的 `data` 空时是 `[]`，不是 `nil`（proto 序列化后为长度 0 的 repeated）。
- 参数缺失 `{code:400, message:'xxx parameter is required'}`；`GetCronById` 未命中 `{code:404, data:undefined}`。
- `normalizeCronData`：`labels`/`extra_schedules` 补 `[]`，`sub_id`/`pid`/`task_before`/`task_after` 未设置时不下发。
- `UpdateCron` 跳过 `nil` 指针字段；`labels`/`extra_schedules` 作为 repeated 永远存在，因此和 Node 一样会被空数组覆盖。
- `RecordNotifyLog`：`status = success ? 0 : 1`、`source=script`、审计保留 200 条。
- `RunCrons` 只入队（`scheduler.RunManual`），立即 `{code:200}`。

### 4.9.2 有意差异

| # | 项 | Node | Go | 说明 |
| --- | --- | --- | --- | --- |
| 1 | `GetCronDetail` 缺 `log_path` 的文案 | `log_path is required` | `log_path parameter is required` | 按 M6.5 任务书统一成 `xxx parameter is required`；如需逐字对齐改 `api.go` 一处常量 |
| 2 | `DeleteCrons` 空 `ids` | 200（`remove([])` 空转） | 400 `ids parameter is required` | 任务书要求与 `Enable/Disable/Run` 一致；对合法调用无影响 |
| 3 | 默认绑定地址 | `BIND_HOST_GRPC=::`（所有网卡） | `127.0.0.1` | 收紧暴露面；需要远程调用时显式设置 |
| 4 | 证书生成失败 | 抛错，gRPC 不启动 | 回环降级明文 + WARN；非回环拒绝启动 | `client.js` 在证书缺失时本来就 `createInsecure()`，两边仍能通 |
| 5 | 证书校验 | 只检查 5 个文件是否存在 | 额外解析一次，损坏即重新生成 | 避免「文件在但内容坏」导致脚本端 `createSsl` 抛错后回退明文、连不上 mTLS 端口 |
| 6 | 额外注册的服务 | `Api` + `Cron` + `Health` | 只注册 `Api` | 另两个是 Node 多进程调度器内部用的，Go 调度器在进程内 |

runner 侧另有两处有意差异（`config.sh` 的保留变量、赋值超集），见 4.9.3。

### 4.9.3 一并修掉的 M5 缺口

**（1）`task_before` 路径含空格**（`sitecustomize.js` / `sitecustomize.py`）

- 现象：`source ${file_task_before} ${fileName}` 未加引号，`QL_DIR=F:/WorkBuddy AI/...`
  时 bash 只拿到 `F:/WorkBuddy`，`source` 失败，`task_before.sh` 导出的变量全部丢失。
- 修法：JS 改为 `source "${shellPath(...)}" "${shellPath(fileName)}"`。
- Python 需要多改一处：原代码把整条命令再套一层 `bash -c "..."`，只给路径加双引号会被
  外层引号提前截断，所以改成 **argv 直传**（`subprocess.check_output([shell, "-c", command])`，
  `shell` 取 `QL_SHELL`，否则 `bash`），路径与 `file_name` 统一走 `shlex.quote`，
  `eval` 的内层引号改为 `'\''` 转义，`python3 -c` 里的 `\"` 去掉一层转义。
- 验证：`task_before.sh` 写 `export HOOK_TOKEN="from-hook"`，JS 与 Python 任务日志都输出
  `HOOK_TOKEN=from-hook`，日志不再有 traceback。

**（2）Python 预加载硬编码 `/tmp`**（比表面严重：整条钩子失效）

- 现象：`temp_file = f"/tmp/env_{pid}.json"`。Windows 上 `/tmp` 是「当前盘符的 \tmp」，
  目录不存在就抛 `FileNotFoundError`；而 `except` 包住了整个 `run()`，于是
  **`source task_before.sh`、`import task_before`、`env.py` 回灌、`QLAPI` 全部一起失效**，
  不是「多一段 traceback」。
- 修法：`temp_file = os.path.join(tempfile.gettempdir(), f"ql-env-{pid}.json").replace(os.sep, "/")`。
  正斜杠是必须的 —— 路径在 `python_cmd` 里被双引号包着，反斜杠会在 shell 层被吃掉；
  前缀从 `env_` 改成 `ql-env-`，与 JS 侧 `ql-env-${pid}.json` 对齐，避免撞名。
- 验证（**不建任何 `/tmp` 目录**）：Python 任务日志输出 `HOOK_TOKEN=from-hook`，
  `QLAPI.getEnvs()` 返回 `code=200`，环境变量页的 `PY_CHECK=visible` 也出现在 `os.environ` 里。

**（3）任务环境不导入 `config.sh`**

- 现象：Node `shell/share.sh:80` 的 `import_config` 会 `. "$file_config_user"`，Go 的
  `runner.baseEnv` 只加载 `shell/preload/env.sh`，于是 `PUSH_KEY`/`WEBHOOK_*` 等
  `QLAPI.notify()` 依赖的配置在 Go 任务里读不到。
- 修法：`runner.importConfig` 复用 `config.ParseConfigSh`（只解析 `KEY="value"`，不 eval），
  顺序为 **基础环境 → `data/config/config.sh` → `shell/preload/lang_env.sh`**。
- **空值不注入**：`PUSH_KEY=` 这种空赋值直接跳过。Node 只是改一个未 export 的 shell 变量，
  子进程仍能看到继承来的值；若注入 `""` 会把父进程继承的 `PUSH_KEY=abc` 覆盖掉，通知就废了。
- 保留变量（与 Node 的有意差异）：`QL_DIR`、`QL_DATA_DIR`、`PATH`、`NODE_OPTIONS`、
  `PYTHONPATH`、`QL_SHELL` 不接受 `config.sh` 覆盖，命中时 WARN 一次，避免一句
  `PATH=` 让所有任务起不来。
- 与 Node 的另一处差异（有意保留的超集）：Node 只是 `source` 文件，**没有 `export` 的赋值
  不会传给子进程**；Go 把所有非空赋值都注入任务环境。社区里大量用户写 `PUSH_KEY=xxx`
  不带 `export`，Node 那个行为是坑不是契约；超集只会让原本跑不了的脚本能跑。
- 验证：`config.sh` 写入 `PUSH_KEY` / `WEBHOOK_URL` / `PATH="/broken/path"` /
  `QL_DIR="/broken/dir"`，JS 与 Python 任务日志均输出 `PUSH_KEY=scttestkey`、
  `PATH_IS_BROKEN=no`、`QL_DIR` 仍为面板路径；`QLAPI.notify()` 打到本地 webhook 成功，
  审计表新增 `source=script status=0 notifyType=webhook`。空值规则由
  `TestImportConfigSkipsEmptyValues` 覆盖（继承值保留、空赋值不新建变量）。

**（4）启动时重建环境变量三文件**（`initData.ts:226` 的对应实现）

- `EnvService.Bootstrap` 在启动时调用一次 `set_envs`，把 `shell/preload/env.{sh,js,py}`
  按数据库重写。`env.sh`/`env.js`/`env.py` 完全由 `Envs` 表派生，所以升级后不会留下
  旧版本写坏的副本；这也回答了「Materialize 要不要管 env.sh」—— `Materialize` 没有 DB
  访问，也没有 `env.sh` 的嵌入副本，重建只能发生在服务层。
- 验证：把 `env.sh` 改成 `export STALE='1'` 再重启，启动后文件被重写为空（库里没有启用的
  变量）；`TestBootstrapRewritesStaleEnvironmentFiles` 覆盖三文件同时重建。

**（5）预加载脚本带版本号落盘**

`shell/preload/.preload-revision` 记录已写入的钩子版本，`Materialize` 只在版本变化时
重写这批钩子，否则老 `data/` 目录里的旧文件永远不会被替换（Node 是随包覆盖）。
`env.js`/`env.py` 属环境变量页生成，`Materialize` 任何情况下都不覆盖它们。

**（6）`QLAPI.notify()` 需要 `undici`**（不是缺陷，是依赖清单）

`shell/preload/__ql_notify__.js:2` 顶层 `require('undici')`，缺包时第一次调用就抛
`Cannot find module 'undici'`。`node_modules/` 已随包提供；`runner/preload_probe.go` 只探
`client.js`（不探通知），所以缺 `undici` 时 QLAPI 其余方法仍可用，只有 `notify()` 报错 ——
这是有意的降级边界。

## 4.10 M7 订阅 / 依赖（已完成，12 + 11 条）

### 4.10.1 覆盖范围

- 订阅 12 条（`back/api/subscription.ts`）+ 依赖 11 条（`back/api/dependence.ts`）全部实现；
  `internal/httpapi/stub.go` 里的 `GET /api/subscriptions → []` 桩已删除。
- 订阅同步不再 exec `ql repo`，而是在面板进程内按 `shell/update.sh` 的语义重写：
  `get_uniq_path` / `git clone -q --depth=1 [-b]` / `gen_list_repo` / `diff_cron` /
  `add_cron` / `del_cron` / `update_raw` 逐条对齐。
- 依赖安装照 `back/services/dependence.ts` 的串行队列与
  `back/config/util.ts:779 getInstallCommand` 的命令表。
- doctor（`internal/platform/toolchain.go`）探测 git/pnpm/npm/node/python/pip/bash，启动时
  逐条落日志，缺 git/pnpm 记 WARN。

### 4.10.2 验收记录（2026-11-07，Windows + 真实 Node 版对照）

1. **脚本目录逐文件一致**：同一本地仓库（`file:///C:/ql-m7/repo-src`，含 js/py/ts/子目录/
   黑名单目录/依赖文件），Go 与 `bash shell/update.sh repo ...` 拉取后
   `data/scripts/ql-m7_repo-src` `diff -r` 为空；整棵 `data/scripts` 也一致
   （含 `sendNotify.js`、`notify.py`）。脚本：`.tmp/m7-acceptance.sh`。
2. **crontab.list 逐字节一致**：`autoAddCron=true` 时新增 4 个任务
   （a.js / b.py / c.ts / d.js，`task <uniq>/<相对路径>`），`cmp` 通过。
   `autoDelCron=true` 时仓库删文件后重跑，任务行与脚本文件一起消失，其余行不变。
   - 注：本机 Git Bash 缺 `jq`/`perl`，`add_cron_api` 无法自行调通面板，所以 Node 侧的
     crontab 参考是把**同一批 name/schedule/command 行**经面板 API 落库生成的；name 与
     schedule 由 update.sh 的提取规则手工推得（`cron:` 注释 → `DefaultCronRule`），
     提取逻辑本身由 `TestCronMetadataPrefersCronAnnotation` 覆盖。
3. **依赖全流程**：`is-number`（pnpm）与 `tabulate`（pip）各装一个——状态
   `queued(6) → installing(0) → installed(1)`，日志保存 pnpm/pip 全文，卸载
   `removing(3) → removed(4)` 后行被删除、包真的消失；任务脚本
   `require('is-number')` 与 `import tabulate` 均成功。
4. **git 缺失**：`PUT /api/subscriptions/run` 返回 HTTP 200 +
   `{"code":400,"message":"未检测到 git，无法拉取仓库订阅，请先安装 git"}`，
   不再出现超时或空列表；doctor 同时记 WARN。
5. **契约 diff**：`.tmp/m7-contract.py` 31 步 0 差异（订阅 18 步 + 依赖 13 步，含校验错误
   与 404）。
6. **重启恢复**：面板重启后按 DB 重新注册订阅；`interval_schedule{seconds,2}` 用例在重启
   后继续每 2 秒触发。

### 4.10.3 实现要点（M8+ 沿用）

- **命名规则是硬契约**：`uniqPath` 的 author 取法（`${url%/*}` → `${x##*/}` → `${x##*:}` →
  `${x##*.}`）和 repo 的 `${repoTmp%.*}` 都不能"优化"，否则老订阅会多出一份目录。
  尾部斜杠的 URL 会得到 `qinglong_qinglong`（owner 被解析成 repo 名），这是 shell 的既有
  行为，已用 bash 逐条核对。
- **文件选择顺序**用 `os.File.Readdirnames` 的目录顺序（与 `find` 一致），不用字典序；
  add_cron 的创建顺序决定 `crontab.list` 的行序（`createdAt DESC`）。
- **默认扩展名**：`extensions` → `RepoFileExtensions` → `js py ts`。Node 的兜底是 `js py`，
  配了 `RepoFileExtensions` 时两边完全一致。
- **`DefaultCronRule` 为空时两边都随机**（分钟/小时），逐字节一致的前提是配置了它或脚本
  自带 cron 注释。
- **订阅日志**：`data/log/<alias>/<YYYY-MM-DD-HH-mm-ss>.log`；头部
  `## 开始执行... <ts>\n`，每个输出块前置 `\n`，尾部
  `\n## 执行结束... <ts>  耗时 N 秒` + 5 个全角空格；`GET /:id/log` 同时返回 `data` 与
  `content`（Node `res.send({code,data,...result})` 的产物），键序照抄。
- **依赖队列**：单 worker FIFO；`cancel` 把 id 移出队列后该任务不再执行；卸载成功（或
  force）后删行；`log` 列是字符串数组，写入串行化（对应 `updateLogLimit`）。
- **`pnpm root -g` 在 pnpm 11+ 返回版本目录**（`<global>/v11`），包实际在
  `<global>/v11/<hash>/node_modules`；NODE_PATH 两个都追加，pnpm 10 及更早的扁平布局同样
  覆盖。doctor 探测 pnpm 时同时记录 `pnpm root -g` 失败原因。
- **订阅运行中 `pid` 保持 NULL**：同步在面板进程内，`stop` 用 context 取消并杀 git/curl
  子进程，不按 pid kill（避免误杀面板自身）。
- **私有仓库 SSH**：`data/ssh.d/<alias>{,.config}` + `~/.ssh/config` 的 `Include` 行，
  与 `back/services/sshKey.ts` 一致；别名含 `/` 时用 `~sub_<sha256>` 命名空间。

### 4.10.4 M7 有意差异

| # | 项 | Node | Go | 说明 |
| --- | --- | --- | --- | --- |
| 1 | 订阅执行方式 | `bash -c "SUB_ID=… ql repo …"` | 进程内 Go | 差异只在日志正文；脚本目录与 crontab.list 逐字节一致 |
| 2 | pip 可执行 | 硬编码 `pip3` | `<python3> -m pip` | Windows 上 `pip3` 常属于另一个解释器，装完任务 `import` 不到；venv 下两边都是 `<venv>/bin/python3 -m pip` |
| 3 | 依赖安装的 shell | Windows 默认 cmd.exe | 有 bash 时优先 bash | 命令串是 POSIX 写法（单引号、`source`），cmd.exe 必然失败；Node 在 Windows 上装 pip 依赖同样报"文件名、目录名或卷标语法不正确" |
| 4 | linux 依赖在 Windows | 建行成功，worker 报"当前系统不支持" | 相同 | `getInstallCommand` 不再因平台抛错（否则 create 直接 500） |
| 5 | `run` 时缺 git | 200，随后任务静默失败 | 200 + `code:400` 明确报错 | 任务书要求"明确错误而不是超时或空列表" |
| 6 | `PUT /status` 的 `pid:"0"` | `parseInt||''` 把 `''` 写进数字列 | 写 0 | 该端点只被 shell 脚本调用，正常路径不会传 0 |
| 7 | 通知与推送 | socket.io + 通知服务 | 只走通知服务（M6.5 同一套） | websocket 属 M8 |


## 4.11 M8 系统设置 / OpenAPI / 仪表盘 / WS（已完成）

### 4.11.1 覆盖范围

| 模块 | 条数 | 实现位置 |
| --- | --- | --- |
| 系统设置 `system.ts` | 26 | `internal/service/{system,system_log,upgrade}.go`、`internal/httpapi/system.go` |
| 存储保留 `retention.ts` | 3 | `internal/service/retention.go`、`internal/store/dashboard.go` |
| 客户端 IP `clientIp.ts` | 3 | `internal/service/clientip.go` |
| 面板配置 `config.ts` | 5 | `internal/service/configs.go`、`internal/httpapi/configs.go` |
| 仪表盘 `dashboard.ts` | 10 | `internal/service/dashboard.go`、`internal/store/dashboard.go` |
| OpenAPI `open.ts` | 6 | `internal/service/open.go`、`internal/store/apps.go`、`internal/httpapi/open.go` |
| 升级 `update.ts` | 3 | `internal/httpapi/configs.go`（`registerUpdateRoutes`） |
| SockJS `/api/ws` | — | `internal/service/sock.go`、`internal/httpapi/ws.go` |

至此 156 条清单全部有真实 handler，`internal/httpapi/stub.go` 只剩一行说明。

### 4.11.2 验收记录（2026-11-07，Windows + 真实 Node 版对照）

- `.tmp/m8-acceptance.py`：54 项，0 失败（系统设置、应用 CRUD、scope、仪表盘、保留、客户端 IP、配置页、升级、系统日志、导出）。
- `.tmp/m8-ws.js`：7 项，0 失败（query token 连接、echo、坏 token → 404、脚本调试广播、登出后 401 关闭）。
- `.tmp/m8-diff.py`：19 条只读端点，Node 与 Go 同数据源，0 差异。
- `.tmp/m8-diff2.py`：37 条写端点与错误分支，0 差异。
- 数据接管：Go 写过的 `data/`（Apps 表 + `keyv:apps` + `systemConfig.info`）直接启动 Node 版，登录、应用列表、仪表盘统计全部正常。
- `node scripts/grpc-smoke.js` 54/54；`go test ./...` 全绿。

### 4.11.3 实现要点（M9 沿用）

- **WS 不走 JWT 中间件**：Node 把 sockjs 装在 http server 上，Express 完全看不到 `/api/ws`。Go 在 `cors` 之后、鉴权链之前插入 `wsHandler`，token 从 query 取，`prefix = baseUrl + "/api/ws"`。
- **每秒重校验**：`wsRegistry` 复用一份 `authInfo` 快照，按 (token, platform) 缓存判定结果，失效即 `Close(401)`；最后一个连接断开时停掉 ticker。
- **广播点**：依赖安装/卸载在 `setStatus`（状态）+ `appendLog`（正文），脚本调试在 runner 的 sink，订阅结束在 `finish`，镜像更新在 `StartNodeMirror` 的管道。前端只订阅这 5 个 topic，其余 type 预留给 CLI。
- **`command-run` 的响应头**：runner 一开始就把日志头写进 sink，会提前触发 `WriteHeader`，导致 `QL-Task-Pid`/`QL-Task-Log` 丢失。用 `deferredWriter` 缓冲到 handler 写完头再放行。
- **设置类接口的响应体不是整行配置**：Node 多数是 `res.send(result)`，`result.info` 就是请求体；只有 `global-ssh-key` 返回完整 Auths 行。`present` 参数用来区分「字段缺失」与「显式 null」。
- **Joi 会做类型转换**：`number()` 接受 `"5"`，`boolean()` 接受 `"true"`/`0/1`；校验器必须跟着转换，否则前端某些写法会被拒。
- **保留策略删的是真数据**：`runningInstanceWhere` = `status<>0 AND (finished_at<cutoff OR (finished_at IS NULL AND started_at<cutoff))`；`cronStatWhere` = `date < today-days`；清理后按需 `VACUUM`。
- **登录语言**：启动时以 `systemConfig.info.lang` 为准（Node `initData` 的顺序），任务日志头/尾走 `shell/lang/en.sh` 的文案（`## Starting...`），不是 `i18n.ts` 的文案。

### 4.11.4 M8 有意差异

| # | 项 | Node | Go | 说明 |
| --- | --- | --- | --- | --- |
| 1 | `PUT /api/system/update`、`PUT /api/update/system` | 调 `panelUpgrade.cjs` 下载/应用升级包 | `{code:400, message:"当前版本不支持在线升级"}` | 单文件发布没有 npm 包可替换；升级由外层启动器负责 |
| 2 | `PUT /api/system/config/linux-mirror` | Windows 上**不返回响应**（请求挂起）；Linux 上改 apt 源 | 非 Linux 返回 `{code:400}`；Linux 返回不支持 | Node 在 Windows 的挂起是缺陷，Go 给出明确错误 |
| 3 | `PUT /api/system/reload` | spawn `ql reload` 后 `process.exit(0)` | 优雅关闭进程，由启动器重启 | 语义一致，只是没有 `ql` CLI 这一跳 |
| 4 | `PUT /api/system/command-stop`（只给 command） | `ps -eo pid,command` 查 pid | 返回 `{code:400,message:"任务未找到"}` | Windows 没有 `ps`；前端总是用 `QL-Task-Pid` 回传 pid |
| 5 | `GET /api/system/log` | 不设 `Content-Type` | `application/octet-stream` | Go 会嗅探成 `text/plain`，显式声明避免前端把日志当文本渲染 |
| 6 | `GET /api/system/notify-log` | `order id DESC limit 200` | 相同（倒序遍历 AuthRowsByType） | 无差异，仅记录 |

## 5. M9 待补

- 156 条路由的契约 diff（Node vs Go 各存一份 JSON，递归比较键名与类型）；M2 的 17 条已可用 `scripts/contract-diff/run-diff.sh` 复用，其余模块按同一模式扩展 `probe.py`。
- 任务双跑：js/py/ts/sh 四类脚本日志正文、退出码、耗时对比。
- 数据目录兼容说明与回滚到 Node 版的步骤（保留 `SchemaMigrations`、`user_version` 不冲突）。
