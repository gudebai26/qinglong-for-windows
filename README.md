# 青龙面板 2.24.0 for Windows（Go 重写版）

**非官方项目**，与青龙官方无关，不代表原作者立场。

**[⬇ 下载最新版](https://github.com/gudebai26/qinglong-for-windows/releases/latest)** · 22 MB，解压即用

免安装。面板本体是单个 56 MB 的 exe——前端、ql API 运行时全部内嵌，不需要安装 Node.js 就能启动；已有的青龙 `data/` 目录可以直接接管，任务、日志、环境变量、订阅全部沿用。

另附一个可选的命令行工具（4 MB），不开浏览器时跑脚本用：

```
qinglong.exe        56 MB    面板本体，单文件自包含
qinglong-CLI.exe     4 MB    命令行工具（可选，删掉不影响面板）
start.bat                    启动脚本（等价于直接双击 exe）
```

> **"单文件"指面板本体**：只把 `qinglong.exe` 拷到一个空目录也能启动，其余两个文件都不是必需的。首次运行会在 exe 旁边生成 `data/`、`node_modules/` 等运行时目录——任何有状态的应用都躲不开这一步。

---

## 目录

- [这是什么](#这是什么)
- [特点](#特点)
- [缺点与限制](#缺点与限制)
- [快速开始](#快速开始)
- [从 Node 版迁移](#从-node-版迁移)
- [命令行工具](#命令行工具)
- [运行环境要求](#运行环境要求)
- [技术实现](#技术实现)
- [兼容性](#兼容性)
- [已知差异](#已知差异)
- [从源码构建](#从源码构建)
- [常见问题](#常见问题)
- [许可证与致谢](#许可证与致谢)

---

## 这是什么

青龙面板是一个定时任务管理平台，原本是 Node.js 应用，以 Docker 镜像分发。本项目把它重写成了 Go 程序，目标是让 Windows 用户解压就能用，不用装 Node、不用配 PM2、不用维护多进程。

**保留了**：前端界面、HTTP API（156 条）、JWT 鉴权、SQLite 表结构、`crontab.list` 格式、任务执行语义、`ql` API（脚本里的 `QLAPI.*`）。

**替换了**：Node 后端进程 → Go 单进程；PM2 → 无（进程内调度）；`shell/task.sh` → Go 原生执行器。

---

## 特点

### 部署

| | |
| --- | --- |
| **面板单文件** | `qinglong.exe` 一个文件装下前端、后端、ql API 运行时。整个压缩包 22 MB，解压 56 MB |
| **CLI 可选** | 命令行工具是独立 exe，不需要就删掉，面板照常运行 |
| **免 Node 启动** | 面板本身不需要 Node.js / Python / Git。只有跑任务才需要对应解释器 |
| **零配置** | 首次启动自动建库、签发 gRPC 证书、解压运行时，约 3 秒就绪 |
| **直接接管数据** | 指向 Node 版的 `data/` 就能用，不改表结构、不要求导出重灌 |
| **不覆盖已有依赖** | 迁移时只补自己缺的文件，用户已有的 1 GB `node_modules` 一个不动 |

### 兼容性

| | |
| --- | --- |
| **API 逐字段对齐** | 156 条路由，310 组 A/B 对比（正常 + 错误分支），响应体 0 差异 |
| **双向数据互通** | Node 建的账号能在 Go 登录，Go 写的数据 Node 也能读 |
| **`crontab.list` 逐字节一致** | 含置顶、禁用、`@once`、`@boot`、`extra_schedules` |
| **任务日志格式一致** | `## 开始执行...` / `## 完成 ✅...`，与上游逐字符相同 |
| **22 条有意差异** | 全部记录在 `docs/compatibility.md`，每条注明「保留/待定」 |

### 执行器

| | |
| --- | --- |
| **不经过 shell** | JS/Python 任务直接 `exec.Command(node, ...)`，Windows 上没装 Git 也能跑 |
| **四类脚本** | `.js` `.mjs` `.ts` `.py` `.pyc` `.sh` `.go`，按扩展名自动选解释器 |
| **完整任务语义** | `now` / `conc` / `desi` / `-m 超时` / `-l` / `--`，环境变量前缀、`task_before/after` 钩子、随机延迟 |
| **进程树清理** | 停止任务时杀掉整棵进程树，不留孤儿 |
| **ql API 可用** | 内置 `@grpc/grpc-js` 等依赖，脚本里的 `QLAPI.getEnvs()`、`$done` 开箱即用 |

### 运维

| | |
| --- | --- |
| **启动日志清晰** | 六步流程 + 明确的「启动成功」和访问地址 |
| **内置诊断** | `qinglong-CLI.exe doctor` 一次列出 Node / Python / Git / pnpm / bash 的状态 |
| **自动修环境** | 缺 pnpm 时自动 `npm i -g pnpm`；Git 装在非标准路径时查注册表 |
| **友好降级** | 缺 Git 只是钩子跳过，不影响 JS/Python 任务；缺 pnpm 只在依赖页提示 |
| **中文编码正确** | 自动切 UTF-8 代码页，退出恢复；Python 任务注入 `PYTHONIOENCODING` |

---

## 缺点与限制

这一节是重点，请先读完再决定是否使用。

### 硬性限制

| 限制 | 说明 |
| --- | --- |
| **仅支持 Windows x64** | 没有 Linux / macOS 构建。代码用了 `syscall` 和注册表，移植需要重写 `internal/platform` |
| **不支持在线升级** | `PUT /api/update/system` 返回「不支持」。升级 = 下载新 exe 覆盖，`data/` 保留 |
| **面板退出即停止** | 没有守护进程。点「重启面板」会直接退出进程，需要重新双击 `start.bat` |
| **不内置解释器** | Node / Python / Git 需要自行安装。内置会让包从 22 MB 涨到 110 MB，得不偿失 |

### 功能缺口

| 缺口 | 影响 |
| --- | --- |
| **订阅需要 Git** | `git clone` 拉仓库，没装 Git 时订阅页明确报错 |
| **`.sh` 任务需要 bash** | bash 来自 Git for Windows，其他扩展名不受影响 |
| **`.ts` 任务需要 ts-node** | 需要 `npm i -g ts-node` |
| **`.pyc` 版本绑定** | 3.13 编译的 `.pyc` 不能在 3.12 跑，跨版本请用 `.py` |
| **依赖安装需要 pnpm** | 首次会自动装，但需要能访问 npm registry |
| **没有 gRPC 的 Cron / Health 服务** | 上游的调度器内部服务，单进程架构不需要 |

### 行为差异

| 差异 | 说明 |
| --- | --- |
| **静态资源 404 而非回退首页** | Node 的 `app.get('*')` 对 `/foo.js` 也返回 `index.html`；这里返回 404，避免把 HTML 当 JS |
| **`/api/static/` 缺文件返回 404** | Node 会继续走到 JWT 中间件返回 401 |
| **命令行执行不写数据库** | `qinglong-CLI.exe task x.js` 只写日志，与 Node 版不带 `ID=` 时一致 |
| **`command-stop` 无 pid 返回 400** | Node 返回 200 空转 |
| **通知渠道无内置测试** | 配置时测试发送成功才落库，与 Node 一致；但失败的渠道不会重试 |

完整清单见 [`docs/compatibility.md`](docs/compatibility.md)。

### 工程上的取舍

- **没有单元测试覆盖全部 API**：`go test` 覆盖 config / store / httpapi / runner / service 的关键路径，契约正确性靠 A/B 脚本（`scripts/m9-*.py`）保证。
- **没有 CI**：回归脚本需要在有 Node 版数据的环境手动跑。
- **单进程无横向扩展**：所有任务在一个进程里调度，任务量很大时不如分布式方案。
- **前端未改动**：`web/` 是上游构建产物，Go 版不改前端，所以 UI 上的已知问题都继承下来了。

---

## 快速开始

### 1. 准备

```
D:\qinglong\
├── qinglong.exe
├── qinglong-CLI.exe
└── start.bat
```

放在**有写权限**的目录，不要放 `C:\Program Files`（除非用管理员运行）。路径不要含空格。

### 2. 启动

双击 `start.bat`。首次启动会创建：

```
data\config\config.sh      面板配置
data\db\database.sqlite    数据库
data\scripts\              脚本目录
data\log\                  任务日志
data\syslog\               面板日志
node_modules\              ql API 运行时（1044 个文件，约 3 秒解压）
```

浏览器打开 <http://localhost:5700>，首次进入设置管理员账号。

### 3. 装解释器（按需）

```powershell
# 跑 JS / TS 任务
winget install OpenJS.NodeJS.LTS

# 跑 Python 任务
winget install Python.Python.3.13

# 跑 .sh 任务、用订阅、需要 bash
winget install Git.Git
```

装完重启面板，`qinglong-CLI.exe doctor` 确认全部 `[OK]`。

---

## 从 Node 版迁移

```powershell
# 1. 停掉 Node 版
docker stop qinglong

# 2. 备份（重要）
Copy-Item -Recurse D:\qinglong\data D:\qinglong\data.bak

# 3. 把 data 目录放到 qinglong.exe 旁边
# 4. 双击 start.bat
```

Go 版直接读写 Node 版的数据库和配置文件，**不改表结构、不要求导出重灌**。

迁移后逐项确认：任务列表、任务日志、环境变量、订阅、依赖。

回退同样简单：停 Go 版，用备份的 `data/` 启动 Node 版即可。

---

## 命令行工具

```cmd
qinglong-CLI.exe task demo.js                 执行脚本
qinglong-CLI.exe task -m 60 demo.py 参数1      60 秒超时
qinglong-CLI.exe task "子目录/a.js"            子目录脚本
qinglong-CLI.exe list                         列出 data\scripts 下的脚本
qinglong-CLI.exe doctor                       检查运行环境
qinglong-CLI.exe version                      显示版本
```

输出同时写终端和 `data\log\<脚本名>\<时间戳>.log`，格式与面板点「运行」一致。

命令行执行**不写数据库**（不更新任务状态），适合调试；正式运行请用面板或 `PUT /api/crons/run`。

---

## 运行环境要求

| 用途 | 需要 |
| --- | --- |
| 启动面板、管理任务、看日志 | **无** |
| 脚本里用 `QLAPI.*` | **无**（已内置） |
| `.js` / `.mjs` 任务 | Node.js |
| `.ts` 任务 | Node.js + ts-node |
| `.py` / `.pyc` 任务 | Python 3 |
| `.sh` 任务 | bash（Git for Windows） |
| 订阅拉取 | Git |
| 安装 npm 依赖 | pnpm（缺失时自动装） |
| 安装 pip 依赖 | pip（随 Python） |

缺失的工具不会阻止面板启动，只在对应功能上给出明确错误。

---

## 技术实现

```
cmd/qinglong/           入口、启动流程、优雅退出
cmd/qinglong-cli/       命令行工具
internal/config/        config.sh 解析（不 eval）、路径、端口
internal/store/         modernc.org/sqlite（纯 Go，无 CGO）
internal/httpapi/       chi 路由、中间件链、静态托管、SockJS
internal/service/       业务逻辑（用户/任务/脚本/环境/订阅/依赖/系统/通知）
internal/runner/        任务执行器（分词、解释器、日志、超时、钩子）
internal/scheduler/     进程内 cron 调度
internal/grpcapi/       ql API 的 gRPC 服务（mTLS 自签证书）
internal/preload/       preload 钩子的嵌入与落地
internal/nodemodules/   ql API 运行时的嵌入与解压
```

**规模**：144 个 Go 文件、约 42000 行。

**技术选型**：

- `net/http` + `chi` — 路由，不引入框架
- `modernc.org/sqlite` — 纯 Go SQLite，`CGO_ENABLED=0` 静态编译
- `sockjs-go` — 前端用 `sockjs-client`，必须协议兼容
- `golang.org/x/crypto` — scrypt 密码哈希
- `embed` — 前端、preload 钩子、proto、ql API 运行时全部打进 exe

**没有使用**：ORM（直接 SQL）、依赖注入容器（显式构造）、日志框架（`log/slog`）。

---

## 兼容性

### API

156 条路由，覆盖上游全部接口。契约测试的做法：

```
同一份 data/ 副本 → Node 跑一遍 → Go 跑一遍 → 归一化 token/时间戳/id → 逐字段 diff
```

结果：**310 组比较，0 差异**。

### 数据

- `database.sqlite`：表结构、列名、索引完全一致（`sqlite_master` diff 为空）
- `keyv.sqlite`：保留 `{"value":...,"expires":null}` 包装格式
- `config.sh`：只解析 `KEY="value"` 赋值，不 `eval`
- `crontab.list`：行序按 `isPinned DESC → isDisabled ASC → status ASC → createdAt DESC`

### 任务执行

| 项 | 与上游 |
| --- | --- |
| 日志首尾格式 | 逐字符一致 |
| 超时退出码 | 124（超时）/ 125（配置非法） |
| 停止退出码 | 143 |
| 随机延迟 | 仅单参数 + 非 `real_time` + 非 `no_delay` 时生效 |
| 钩子变量回灌 | `env -0` 落盘后并入子进程环境 |

---

## 已知差异

完整的 22 条记录在 [`docs/compatibility.md`](docs/compatibility.md)，每条都标注了「保留 / 待定」和理由。几个用户可能感知到的：

1. **静态资源未命中返回 404**，不返回首页（避免把 HTML 当 JS）
2. **`/api/static/` 缺文件 404**（Node 是 401）
3. **命令行 `task` 不写数据库**（与 Node 不带 `ID=` 时一致）
4. **`skipHistory` 语义**：仅 413 分支跳过归档，普通文件路径只增加 `expectedHash` 强校验
5. **Python 注入 `PYTHONIOENCODING=utf-8`**（Node 版没有，Windows 上会崩 emoji）
6. **`pip` 用 `<python3> -m pip`**，不硬编码 `pip3`（多版本共存时更可靠）

---

## 从源码构建

### 依赖

- Go 1.22+
- Node.js 20+（仅构建前端需要）
- pnpm

### 步骤

```bash
# 1. 克隆上游前端
git clone https://github.com/whyour/qinglong.git upstream
cd upstream && pnpm install --ignore-scripts

# 2. 构建前端 → static/dist
pnpm build:front

# 3. 复制到本仓库
cp -r static/dist ../web

# 4. 准备 ql API 运行时（约 1044 个文件）
#    需要 @grpc/grpc-js、@grpc/proto-loader、protobufjs、undici、nodemailer 等
cp -r node_modules ../node_modules

# 5. 编译
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o qinglong.exe ./cmd/qinglong
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o qinglong-CLI.exe ./cmd/qinglong-cli
```

### 重新生成 gRPC 代码

```bash
go install github.com/bufbuild/buf/cmd/buf@v1.47.2
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.5
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1
buf generate
```

---

## 常见问题

**Q: 为什么不内置 Node / Python / Git？**

内置后压缩包会从 22 MB 涨到 110 MB，解压 250 MB。而这些运行时本来就是青龙用户的常规前置，且需要独立升级（Python 版本、Git 更新）。如果你需要完全离线部署，可以自行把便携版放进 `runtime/` 目录，程序会优先使用它们。

**Q: 面板能跑，但任务报「找不到 node」。**

`qinglong-CLI.exe doctor` 看 node 那行。没装就装，装了但没进 PATH 就重开终端。程序只在启动时解析一次 PATH。

**Q: 日志里的图标显示成问号。**

传统 cmd 字体没有 emoji 字形。程序检测到 `WT_SESSION` 环境变量（Windows Terminal 会设置）时保留图标，否则自动降级为 `>` 和 `OK`。也可以用 `QL_LOG_ASCII=1` 强制 ASCII。

**Q: 点「重启面板」后窗口关了，怎么再启动？**

这是预期行为——没有守护进程。重新双击 `start.bat`。

**Q: 能同时跑 Node 版和 Go 版吗？**

不能，会抢同一个端口和同一个 SQLite 文件。迁移前先停 Node 版。

**Q: 数据能回退到 Node 版吗？**

能。Go 版只新增 `.node-modules-revision` 和正常业务数据，不改 Node 需要的任何文件。

---

## 许可证与致谢

本项目基于 [whyour/qinglong](https://github.com/whyour/qinglong)（Apache License 2.0）修改，遵循同一许可证。

- 前端构建产物（`web/`）、gRPC 协议定义（`back/protos/api.proto`）、preload 钩子（`shell/preload/`）来自上游
- 后端为 Go 重写
- 详细的修改说明见 [`NOTICE`](NOTICE)

**本项目与青龙官方无关，不代表原作者立场。**

---

## 状态

功能完整，可作为日常使用。已知问题见 [Issues](../../issues)。

欢迎提 PR，但请注意：**API 契约不允许改动**——前端一行不改是这个项目的前提。
