# 迁移说明：Node 版 → Go 版（单文件）

本文档覆盖从青龙 Node 版切换到 Go 单文件版的过程、逐项验证清单与回滚方法。
完整的行为差异见 `docs/compatibility.md`，安装与 FAQ 见 `README.txt`。

---

## 一、迁移前：备份（必须）

迁移全程只读你的 `data/` 目录，Go 版直接读写 Node 的数据库和配置，
**不改表结构、不要求导出重灌**。但任何迁移都建议先备份：

```
copy data  data.backup-<日期>          （Windows，整个目录复制）
# 或 tar -czf data.backup.tgz data      （Linux / macOS）
```

确认备份能打开后，再进行下一步。

---

## 二、迁移步骤

1. **停掉 Node 版**：`docker stop <容器>`，或结束 `node` 进程。
   确认 5700 端口已释放：`netstat -ano | findstr :5700`。

2. **放置单文件包**：新建一个目录，放入 `qinglong.exe`、`start.bat`、`README.txt`
   三个文件。目录**不要**放在网络映射盘（SQLite 在网络盘会锁表失败）。

3. **接管 data/**：把 Node 版的 `data/` 目录整体复制到 `qinglong.exe` 旁边。

4. **启动**：双击 `start.bat`（或命令行 `qinglong.exe`）。
   首次启动会：
   - 读出 `data/db/database.sqlite`、`data/db/keyv.sqlite`、`data/config/config.sh`；
   - 在 exe 旁解压出 `node_modules/`（ql API 运行时，约 8 MB / 1044 个文件）；
   - **不会覆盖**你已有的 `node_modules`（通常 1 GB），只补缺。

5. 浏览器打开 `http://localhost:5700`，用原账号密码登录。

---

## 三、逐项验证清单（迁移后）

- [ ] 登录成功（旧密码直接可用，说明 scrypt 与 keyv 兼容）。
- [ ] 任务列表完整，`crontab.list` 与迁移前一致。
- [ ] 点开一个任务的历史日志能读到（`data/log/<log_name>/`）。
- [ ] 环境变量页数量一致（`data/config/env.sh` 已由数据库重写）。
- [ ] 脚本列表完整，子目录结构保留。
- [ ] 订阅列表完整；手动「运行」一次能拉取并生成 crontab。
- [ ] 依赖页显示已装依赖；跑一个 `require('xxx')` 的任务能命中。
- [ ] 通知页配置一个 webhook，测试发送能实收。
- [ ] 仪表盘有数据（趋势/成功率来自 `CrontabStats`）。

任一项异常，先看 `data/syslog/` 当日日志，再对照 `docs/compatibility.md` §3.1。

---

## 四、回滚到 Node 版

Go 版只在 data 目录里新增/更新业务数据与 `.node-modules-revision`，
不删改 Node 需要的任何文件。回滚：

1. 停 Go 版（关窗口或结束 `qinglong.exe` 进程）。
2. 用第 1 步的备份 `data.backup-*` 覆盖回 `data/`（若迁移后没有新数据）。
3. 重新启动 Node 版容器/进程即可。

如果迁移后又产生了新数据需要保留，直接把 Go 版旁的 `data/` 交还 Node 版
（数据库结构未被 Go 改动过，`PRAGMA user_version` 之外 Node 不读的字段也原样保留）。

---

## 五、注意事项

- **端口**：默认 5700。冲突时改 `data/config/config.sh` 的 `BACK_PORT`，或设
  环境变量 `BACK_PORT`（环境变量优先）。
- **路径含空格/中文**：支持。`start.bat` 用 `cd /d "%~dp0"` 定位自身目录。
- **解释器**：面板本身免 Node/Git/Python；任务仍按脚本类型需要
  `node` / `ts-node` / `python3` / `bash`。ql API 由内置 node_modules 提供。
- **`git` / `pnpm` 可选**：缺失只影响「订阅拉取」和「依赖安装(Node)」，
  启动日志会有一条 WARN 说明。
- **两步验证**：迁移后 2FA 状态与恢复码沿用 Node 的数据，无需重新绑定。
