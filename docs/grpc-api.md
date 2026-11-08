# M6.5 · gRPC ql API

> 任务脚本里的 `QLAPI.*` 走的就是这条链路：`shell/preload/client.js` → gRPC →
> `internal/grpcapi` → `internal/service`。没有它，`QLAPI` 只是内存里的一个空壳，
> 任何一次调用都会 `ECONNREFUSED`。

## 1. 代码生成

| 文件 | 作用 |
| --- | --- |
| `proto/api.proto` | `back/protos/api.proto` 的原样副本（字段号/tag 一律不改） |
| `buf.yaml` | `version: v2` + `modules: [{path: proto}]` |
| `buf.gen.yaml` | `protoc-gen-go` 与 `protoc-gen-go-grpc`，`out: gen`，`paths=source_relative`，`Mapi.proto=qinglong-go/internal/grpcapi` |
| `internal/grpcapi/api.pb.go`、`api_grpc.pb.go` | 生成产物（`buf generate` 后从 `gen/` 移入） |

重新生成：

```bash
buf generate && mv gen/api.pb.go gen/api_grpc.pb.go internal/grpcapi/ && rmdir gen
```

`buf.yaml`/`buf.gen.yaml` 留在仓库里，升级 proto 时只改 `proto/api.proto` 再跑一次即可。
生成器版本：buf 1.47.2、protoc-gen-go 1.36.5、protoc-gen-go-grpc 1.5.1。

## 2. 运行时依赖（Node 侧）

`client.js` 第一行就 `require('@grpc/grpc-js')`，失败会 `process.exit(1)` —— 任务会直接
死掉而不是降级。所以 `node_modules/` 随包提供（合计约 8 MB）：

```
@grpc/grpc-js  @grpc/proto-loader  protobufjs  @protobufjs/*  yargs  yargs-parser
long  lodash.camelcase  @js-sdsl/ordered-map  undici  nodemailer
cliui  escalade  get-caller-file  require-directory  string-width  strip-ansi
ansi-regex  ansi-styles  color-convert  color-name  emoji-regex  is-fullwidth-code-point
wrap-ansi  y18n
```

- `undici` 是 `__ql_notify__.js` 的顶层依赖，缺了 `QLAPI.notify()` 会抛
  `Cannot find module 'undici'`；`nodemailer` 只在 email 渠道用到（懒加载）。
- 位置：`<QL_DIR>/node_modules`。Node 从 `shell/preload/client.js` 逐级向上查找，
  天然命中；`runner` 另外把 `<QL_DIR>/node_modules` 加进 `NODE_PATH`，脚本换
  `work_dir` 也能 `require` 到同一批包。
- 已删掉运行时用不到的文件（`@grpc/grpc-js/src`、`protobufjs/dist`、`*.js.map`），
  否则会多出 3.7 MB。

`runner/preload_probe.go` 的探测决定是否注入 `NODE_OPTIONS`：探测通过才加
`-r sitecustomize.js`，失败只打一条 WARN，任务照跑（没有 QLAPI），不会因此失败。

预加载脚本带版本号落盘（`shell/preload/.preload-revision`）：版本变化时 `Materialize`
会重写这批钩子，否则老 `data/` 目录里的旧文件永远不会被替换；`env.js`/`env.py` 由环境
变量页生成，任何情况下都不覆盖。

## 3. 服务端

| 项 | 值 |
| --- | --- |
| 端口 | `GRPC_PORT`，默认 `5500`（`config.sh` 或环境变量均可） |
| 绑定 | `BIND_HOST_GRPC`，默认 `127.0.0.1`（Node 默认 `::`，Go 收紧到回环） |
| 证书目录 | `<QL_DATA_DIR>/config/grpc` |
| 客户端校验 | `tls.RequireAndVerifyClientCert`（与 Node `createSsl(ca, [{cert,key}], true)` 等价） |
| 停机 | `GracefulStop()`，5 s 超时后 `Stop()` |

证书缺失（或解析失败、或只有一半）时按 `grpcCerts.ts` 的规格重新生成：

- RSA 2048，有效期 10 年（3650 天）
- CA：`CN=qinglong-ca, O=qinglong, C=CN`
- 服务端：`CN=grpc-server`，SAN `DNS:localhost, IP:127.0.0.1, IP:::1`
- 客户端：`CN=grpc-client`，`extendedKeyUsage=clientAuth`
- 私钥用 PKCS#1（`RSA PRIVATE KEY`），与 `openssl genrsa` 一致
- 逐个文件「临时文件 + rename」落盘，避免客户端读到半截证书

生成失败不阻塞面板启动：回环地址下降级为明文并 WARN（此时 `client.js` 也会
`createInsecure()`，两边仍然能通）；**非回环绑定**则拒绝启动 gRPC，避免把无鉴权的
任务 API 暴露到网络上。

## 4. 20 个 RPC 的两种错误模式

这一节是移植时最容易写错的地方 —— Node 里两种风格混用是**故意的**：

**A. 错误写在 response body 里**（`callback(null, {code:500,...})`）

| RPC | 失败时 |
| --- | --- |
| `GetEnvs` | `{code:500, data:[], message}`；`data` 必须是 `[]` 不能是 `nil` |
| `GetCrons` | `{code:500, data:[], message}`；同上 |
| `CreateEnv` | 走 gRPC error（见 B）——它虽然返回 `EnvsResponse`，但异常是 `callback(e)` |

**B. 用 gRPC error**（`callback(e)`）：其余 17 个，客户端侧表现为 Promise reject。

**C. 参数缺失 → `{code:400, message:'xxx parameter is required'}`（不是 gRPC error）**

`GetCronById(id)`、`GetCronDetail(log_path)`、`RunCrons/EnableCrons/DisableCrons/DeleteCrons(ids)`、`MoveEnv(id)`、`UpdateEnv(env.id)`、`GetEnvById(id)`、`DeleteEnvs/DisableEnvs/EnableEnvs/UpdateEnvNames(ids)`。

**D. `GetCronById` 查不到 → `{code:404, data:undefined, message}`**（同样不是 gRPC error）。

### 数据归一（`normalizeCronData`）

`GetCrons` / `GetCronById` / `GetCronDetail` / `CreateCron` / `UpdateCron` 统一输出：

- `labels`：`null → []`
- `extra_schedules`：`null → []`
- `sub_id` / `pid` / `task_before` / `task_after`：未设置（0 或空串）时**不下发**，
  而不是发 `0` / `""`
- `GetCrons` 等价 `page=0&size=0`：全量、不分页

### 其它语义

- `GetEnvs`：`remarks` 为 `null` 时输出 `""`（`CreateEnv`/`UpdateEnv` 保持原值）
- `UpdateCron`：proto3 optional 未传即 `nil`，**跳过**不写；`labels`/`extra_schedules`
  是 repeated，请求里永远是数组（哪怕空），所以和 Node 一样会被覆盖
- `RecordNotifyLog`：`status = success ? 0 : 1`（**反的**），`source` 固定 `script`
- `SystemNotify`：走 `service.NotifyService`，返回 `{code, message}`，并且**总是**写一条
  `source=system` 的审计
- `RunCrons`：只入队，立即返回 `{code:200}`，不等任务跑完

## 5. 自测

```bash
# 面板已启动
node scripts/grpc-smoke.js --keep --verbose
```

脚本走 mTLS 直连 `localhost:5500`，覆盖 20 个 RPC 的返回码、归一规则、参数校验，
并用 `node:sqlite` 直接核对 `Crontabs` / `Auths(notifyLog)` 的落库结果；`RunCrons`
之后会等 `data/log/grpc_probe/*.log` 出现 `GRPC_PROBE done`，确认任务真的跑了。

任务脚本：`scripts/grpc-probe-task.js`（复制到 `data/scripts/grpc_probe.js` 使用）。

## 6. 已知限制

- 只注册了 `com.ql.api.Api`。Node 另外还注册了 `CronService`（`addCron/delCron/
  setConcurrency`）与 `HealthService`，那两个服务是 Node 多进程调度器内部用的，
  Go 版调度器在进程内，不需要。
- `SystemNotify` 的渠道覆盖：gotify / goCqHttpBot / serverChan / pushDeer / chat /
  bark / telegramBot / dingtalkBot / weWorkBot / iGot / pushPlus / pushMe / ntfy /
  feishu(lark) / webhook。`email`、`weWorkApp`、`aibotk`、`wePlusBot`、`chronocat`、
  `wxPusher*`、`wpush` 留给 M8（未实现时 WARN 并返回失败，不会假装成功）。
- `QLAPI.notify()`（JS 多渠道路径）读的是任务环境里的 `PUSH_KEY`/`BARK_PUSH`/`WEBHOOK_*`
  等变量。这些变量来自 `data/config/config.sh`：`runner.importConfig` 会把 config.sh 与
  `shell/preload/lang_env.sh` 解析进任务环境（`QL_DIR`/`PATH`/`NODE_OPTIONS` 等保留变量除外），
  所以通知配置写在 config.sh 里即可生效；面板「系统设置 → 通知」写的是 `Auths.notification`，
  只影响 `systemNotify`，Node 也是这两套。
