青龙面板 2.24.0（Go 版 · 单文件）
================================

本目录只有三个文件，面板、前端、任务运行时全部在 qinglong.exe 里：

    qinglong.exe      面板本体（含 React 前端、ql API 所需 node_modules）
    qinglong-CLI.exe  命令行入口（task / list / doctor / version）
    start.bat         启动脚本（GBK 编码，双击即可）


一、安装
--------

1. 把整个目录放到一个**有写权限**的位置，例如 D:\qinglong。
   不要放在 C:\Program Files 下，除非用管理员身份运行。

2. 双击 start.bat。首次启动会在 exe 旁边自动创建：

       data\config\config.sh      面板配置（端口、日志级别等）
       data\db\database.sqlite    数据库
       data\scripts\              脚本目录
       data\log\                  任务日志
       data\syslog\               面板日志
       node_modules\              ql API 运行时（从 exe 解压，1044 个文件）
       .node-modules-revision     解压版本标记

3. 浏览器打开 http://localhost:5700 ，首次进入设置管理员账号和密码。

4. 需要换端口时改 data\config\config.sh：

       BACK_PORT="5800"

   也可以在启动前设环境变量 BACK_PORT=5800，环境变量优先。

命令行启动（不双击）也可以：

       cd /d D:\qinglong
       qinglong.exe

Ctrl+C 或关闭窗口会结束面板；面板退出时会一并结束正在运行的任务，不留残留进程。


二、运行环境
------------

面板本身**不需要 Node、不需要 Git、不需要 Python**，exe 开箱即用。

但任务是用户自己的脚本，仍然需要对应的解释器：

    .js / .mjs    需要 Node.js（PATH 里能找到 node）
    .ts           需要 ts-node
    .py           需要 python3 或 python
    .sh           需要 bash（Git for Windows 自带）
    .go           需要 go

ql API（脚本里的 require('ql')、QLAPI.*、$done）由面板内置的
node_modules 提供，只要装了 Node 就能用，不用 npm install。

git / pnpm 是**可选**的，只在两个页面用到：

    订阅拉取        需要 git
    依赖安装(Node)  需要 pnpm（没有 pnpm 时面板会提示安装命令）

两者缺失不会影响面板启动和其他功能，启动日志里会有一条 WARN 说明。


三、从 Node 版迁移
------------------

详细步骤见 MIGRATION.md，核心只有三步：

1. 停掉 Node 版（docker stop，或结束 node 进程）。
2. 备份 data 目录（整个复制一份即可）。
3. 把 data 目录放到 qinglong.exe 旁边，双击 start.bat。

Go 版直接读写 Node 版的数据库和配置文件，**不改表结构、不要求导出重灌**。
已有 node_modules 的用户目录（通常 1GB 左右）不会被覆盖：面板只补自己缺的
文件，已有的一个都不动。

迁移完成后建议逐项确认：任务列表、任务日志、环境变量、订阅、依赖。


四、FAQ
-------

Q: 路径里有空格或中文，能跑吗？
A: 能。start.bat 用 cd /d "%~dp0" 定位自身目录，面板内部所有路径都做了引号
   处理（这是 Go 版修掉的老问题之一）。例如 D:\我的 面板\qinglong 可以正常
   工作。唯一要求是**不要放在网络映射盘**上，SQLite 在网络盘上会锁表失败。

Q: 5700 端口被占用，起不来。
A: 先看是谁占用：

       netstat -ano | findstr :5700

   然后二选一：结束占用进程（taskkill /PID <pid> /T /F），或改端口：

       data\config\config.sh 里改 BACK_PORT="5800"

   改完重启面板，访问 http://localhost:5800 。

Q: 双击 start.bat 一闪而过。
A: 用 cmd 打开该目录，手动执行 start.bat，就能看到错误信息。常见原因：
   exe 被杀毒软件隔离、目录没有写权限、data\db\database.sqlite 被另一个
   进程占用（同时开了两个面板）。

Q: 面板起来了，但脚本里 QLAPI 是 undefined。
A: 说明 node_modules 没解压成功或 Node 找不到它。检查：

       dir node_modules\@grpc\grpc-js\package.json

   文件在但脚本仍报错时，确认 node 在 PATH 里（node -v 有输出）。启动日志
   （data\syslog）里会有一条 "node_modules extraction incomplete" 或
   "preload unavailable" 的 WARN，按提示处理即可。

Q: 任务到了时间却没跑，日志里也没有脚本输出？
A: 先看这个任务的日志开头。如果写着

       ## 开始执行... 2026-11-08 20:50:00

       任务随机延迟 54 秒，将于 2026-11-08 20:50:54 开始，配置文件参数
       RandomDelay 置空可取消延迟

   那就不是没跑，而是 config.sh 里的 RandomDelay 在起作用：任务触发后会在
   1 到该秒数之间随机等待，最长可能是几分钟，等完才真正执行脚本。这段文
   字与 Node 版逐字节一致。等待期间任务列表已显示“运行中”，点“停止”会立
   刻取消本次等待，不会几分钟后又自己跑起来。

       RandomDelay=""                     取消延迟
       RandomDelayFileExtensions="js py"  只延迟 js/py，其他文件准点跑
       RandomDelayIgnoredMinutes="0 30"   整点、半点不延迟

Q: 日志在哪？
A: 面板日志 data\syslog\（按天一个文件）；任务日志 data\log\<任务名>\。

Q: 怎么升级？
A: 停面板 → 备份 data 目录 → 用新的 qinglong.exe、qinglong-CLI.exe 覆盖旧文件
   → 启动。
   data 目录保持不动，schema 和数据都会自动沿用。

Q: 怎么回退到 Node 版？
A: 停 Go 版 → 用备份的 data 目录启动 Node 版即可。Go 版只会在 data 目录里
   新增 .node-modules-revision 和正常的业务数据，不删改 Node 需要的文件。


五、目录说明
------------

    data\config\config.sh      配置（端口、并发、通知等）
    data\config\crontab.list   任务清单（由面板生成，勿手改）
    data\db\database.sqlite    主库
    data\db\keyv.sqlite        会话/键值库
    data\scripts\              脚本
    data\log\                  任务日志
    data\syslog\               面板日志（排障先看这里）
    data\deps\                 依赖安装目录
    node_modules\              ql API 运行时（可删，重启会重新解压缺失文件）
