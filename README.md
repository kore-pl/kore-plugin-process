# kore-plugin-process

kore 的进程管理插件（生产级）：**真实读取 Linux `/proc`**，提供进程列表、单进程详情、
信号投递、进程树与优先级调整（renice）；kill 与 renice 共用同一份进程保护名单。
`process.list` 的 `cpu_pct` 用进程内 tick 快照做差分，**零后台 ticker**——只在
`list` 调用时顺带更新基线（G-19）。

## 协议方法（kore.plugin.v1）

| 方法 | 入参 | 结果 |
| --- | --- | --- |
| `kore.ping` | `{}` | `{status,uptime_s,goroutines,rss_kb}` |
| `kore.reload` | `{}` | `{ok:true,config_digest:""}`（无状态，空操作） |
| `kore.shutdown` | `{}` | `{ok:true}`，随后优雅退出 |
| `process.list` | `{sort?,limit?}` | `{schema,sampled_at,first_sample,cpu_note,sort,limit,count,total,masked_args,items[]}` |
| `process.detail` | `{pid}` | 单进程详情（见下） |
| `process.kill` | `{pid,confirm,name,signal?,force?}` | `{schema,pid,signal,name,killed,still_alive,waited_ms,note?}` |
| `process.renice` | `{pid,value,confirm,name?}` | `{schema,pid,name,previous,nice,changed}` |
| `process.tree` | `{root?}` | `{schema,roots,count,total,truncated,max_depth,max_nodes,nodes[]}` |

### process.list

- 扫描 `/proc/<pid>/`，条目字段：`pid / name(comm) / state / user(uid→name) / uid /
  nice / rss_kib / cpu_pct / cmdline / restricted / restricted_reason? /
  protected / protected_reason?`。
- `protected=true` 表示该进程在保护名单内（见「安全护栏」），前端据此加锁标并禁用
  终止 / 优先级操作；`protected_reason` 给出具体原因。
- `sort ∈ cpu|mem|pid|name`，缺省 `cpu`。`cpu` / `mem` 降序，`pid` / `name` 升序；
  `cpu` 同值时按 pid 降序（新进程优先），其它同值按 pid 升序，输出顺序确定。
- `limit` 缺省 `50`、范围 `1..500`；`total` 是截断前的扫描总数。
- `first_sample=true` 表示本次是插件启动后第一次采样：**`cpu_pct` 全为 0**，
  `cpu_note` 明确说明原因（无上次快照）；下一次 `list` 起才有差分值。
- 首次调用会更新基线；并发调用由采样器 mutex 串行化，不会互相污染快照。

### process.detail

`pid / ppid / name / state / user / uid / nice / cmdline / exe(readlink) / cwd(readlink) /
status{state,vm_rss_kib,vm_peak_kib,threads} / start_ticks / clk_tck /
started_at / elapsed_s / restricted / protected / protected_reason?`。

启动时间 = `/proc/stat` 的 `btime` + `starttime / CLK_TCK`，`CLK_TCK` 固定 100
（Linux USER_HZ，`CGO_ENABLED=0` 下不依赖 `sysconf`）；`btime` 不可读时只回
`start_ticks`，不编造墙钟时间。

### process.kill

四层护栏（全部在 `kill(2)` 之前完成）：

1. `confirm:true` 必填——终止进程不可自动重试，必须显式确认；
2. `name` 必填，且必须等于 `/proc/<pid>/stat` 的 comm——防 PID 复用误杀；不符返回
   `E_CONFLICT`；
3. `signal` 缺省 `TERM`，白名单 `TERM|KILL|HUP|INT`（大小写不敏感），其它值 `E_INVALID`；
   `KILL` 不可恢复，还要额外给 `force:true`；
4. 保护名单（见「安全护栏」）命中返回 `E_UNAUTHORIZED`，附具体原因。

- `killed=true` 表示**信号投递成功**；`still_alive` 表示 500ms 确认窗口后进程仍存活
  （僵尸 `Z` / 已死 `X` 视为不存活）。`note` 在仍存活时说明原因。
- 500ms 内每 50ms 读一次 `/proc/<pid>/stat` 的状态字符；探测本身失败时保守视为存活，
  不把一次 `EACCES` 伪造成"已杀"。
- 投递失败时 `EPERM` → `E_NEED_ROOT`（附 `sudo` 启动 kored 的提示），`ESRCH` → `E_NOTFOUND`。

### process.renice

- `value` 必填，范围 `0..19`（数值越大优先级越低）；`confirm:true` 必填；`name` 可选，
  提供时做与 kill 相同的 PID 复用复核。
- 与 kill 共用保护名单：kored / 插件自身 / 内核线程 / 其他 kore 组件一律
  `E_UNAUTHORIZED`。
- 非 root 只允许把数值**调大**（降低优先级）；调小（提高优先级）直接 `E_NEED_ROOT`，
  不触达执行面。执行面 `EPERM` 同样映射 `E_NEED_ROOT`，`ESRCH` → `E_NOTFOUND`。
- 结果 `{schema:"process.renice.v1",pid,name,previous,nice,changed}`：`previous` 是调整前
  的 nice，`changed=false` 表示新旧值相同（幂等调用）。

### process.tree

从一次 `/proc/*/stat` 扫描建 `ppid → children` 反查表，BFS 输出进程树（schema
`process.tree.v2`）：

- `root` 缺省 / `0`：输出全系统森林，`roots` 是各棵树根（ppid 为 0，或父进程不在本次
  扫描集内），按 pid 升序；`total` 是进树记录数（截断前）。
- `root` 为正整数：以该 pid 为根的子树，`roots=[pid]`；根不存在返回 `E_NOTFOUND`，
  负值为 `E_INVALID`。
- `depth` 以各根为 0；`children_count` 是该进程在本次扫描中的直接子进程数（截断前口径）。
- 深度 ≤ 10、节点 ≤ 500，命中任一上限时 `truncated=true`（不静默截断）；环路由 visited
  集合兜住，不会死循环或重复输出。

## CPU 采样机制（为什么是 0%）

`/proc/<pid>/stat` 第 14/15 字段是 `utime/stime`（单位 tick）：

```
cpu_pct = (ticks_now - ticks_prev) / CLK_TCK / 距上次 list 的秒数 × 100
```

- 单核归一：多核满载可超过 100%（与 `top` 的 Irix 模式一致），不是除以 CPU 核数。
- 新出现的进程没有上次 tick，`cpu_pct=0`；tick 回绕（进程 pid 复用）按 0 处理。
- 没有任何 `time.Ticker` / `time.AfterFunc` / 后台 goroutine；进程空闲时 CPU 为 0。

## 安全护栏

| 护栏 | 行为 |
| --- | --- |
| `pid < 1` | `E_INVALID`（非法入参） |
| PID 1 | `E_UNAUTHORIZED`（init 是用户态所有进程的祖先） |
| 插件自身 pid | `E_UNAUTHORIZED`（`KORE_PLUGIN_SOCK` 进程绝不自杀） |
| `KORE_PROTECTED_PIDS` | 逗号分隔正整数名单，命中即 `E_UNAUTHORIZED` |
| 插件父进程 | `E_UNAUTHORIZED`（生产环境即 `kored`） |
| kore 主进程 | `/proc/<pid>/comm == "kored"` → `E_UNAUTHORIZED` |
| kore 组件 | `comm` 以 `kore-` 前缀命名 → `E_UNAUTHORIZED` |
| 内核线程 | `cmdline` 为空且 `ppid ∈ {0,2}` → `E_UNAUTHORIZED`（exec 瞬态空 cmdline 不会误判） |
| kore 运行目录进程 | 可执行文件位于 `<state>/plugins/` 同根或 socket 同目录 → `E_UNAUTHORIZED` |
| 非 root | 只能操作同 uid 进程；`EPERM` → `E_NEED_ROOT`，`ESRCH` → `E_NOTFOUND` |
| 任意 `/proc` 路径 | 不支持：所有路径由固定根 + 数字 pid + 固定文件名拼接 |
| `/proc/kcore` | 永不读取 |

被拒绝的 kill / renice 不会调用 `kill(2)` / `setpriority(2)`（护栏在投递前完成）。
保护名单判定只依赖 `/proc` 字段与运行期注入的选项，不做网络或文件系统旁路探测。

## 非 root 与 restricted

非 root 只能看到 / 操作同 uid 的进程。`/proc/<pid>/{stat,status,cmdline,exe,cwd}`
读取失败或 `EPERM` 时，该进程在结果里标记 `restricted=true` 并给出
`restricted_reason`，**不报错整个请求**；扫描期间退出的进程（ENOENT）直接跳过。
`exe` / `cwd` 指向已删除文件时保留内核返回的 `(deleted)` 后缀。

## 命令行脱敏

`cmdline` 里可能有密码 / token。`KORE_PROCESS_MASK_ARGS` 默认 `true`：

- `--password=xxx` / `--token=xxx` / `--secret=xxx` / `--api-key=xxx` 等敏感键的值替换为 `***`；
- `--password secret` 这种空格分隔写法同样脱敏紧随其后的值；
- 非敏感参数（`--name=x`、`--user=root`）原样保留，不过度脱敏。

关闭：`KORE_PROCESS_MASK_ARGS=false`（同时接受 `0/no/off`）。非法取值启动即报错，
不静默退回默认。

## 平台与生命周期

- **Linux 真实现**；非 Linux 五个方法显式返回 `E_UNSUPPORTED`，不静默降级、不伪造数据。
- `lifecycle=lazy`：无后台 goroutine / 定时器，请求处理完即可被宿主回收。
- 能力声明 `proc:read` + `proc:write`；资源预算 6 MiB / 10% CPU / 16 pids。

## 测试（全真跑）

```sh
gofmt -l .
go vet ./...
go test -timeout 200s ./...
make build
make dist
```

- `internal/procfs`：真实 `/proc`（当前进程 detail、真实 sleep kill、真实 shell+sleep
  父子树、真实 renice 后回读 nice、空 cmdline 瞬态探测），加 fake 执行面覆盖排序
  （cpu/mem/pid/name）、limit 截断、首次采样全 0、保护名单九类矩阵（PID 1 / 自身 /
  env 名单 / 父进程 / kored / kore- 前缀 / 内核线程 / 状态树兄弟 / socket 同目录）、
  kill 四层护栏（confirm / name 复核 / force / 保护名单）、僵尸判定、深度/节点上限、
  全树森林与 PPID 成环、脱敏与 restricted。
- `tests/fault/`：四类故障注入（kill / timeout / disk / cgroup）覆盖五个方法，断言
  方法级错误码（`E_UPSTREAM` / `E_INTERNAL` / `E_UNSUPPORTED`）与无故障 sanity。
- `tests/e2e/`：真实插件二进制 + 假内核 UDS，全链路
  list（前 5 条）→ detail（nice/protected）→ tree 子树 + 全树 → kill 四层护栏矩阵
  （缺 confirm / 缺 name / 名称不符 / KILL 缺 force / PID 1 / 自身）→ kill TERM →
  二次 list 消失 → renice 降优先级与被拒调高 → cmdline 恰为
  `sleep 300 --password=secret` 的脱敏验证 → shutdown；日志输出 `E2E_*` 证据行。

## 构建与打包

`Makefile` 的 `dist` 显式列出 `info.json`、`bin/process`、`SHA256SUMS`，用
`--sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner` + `gzip -9n` 保证确定性。
二进制只内嵌静态 `manifest.json`（不含 `size_bytes`），`dist` 把真实 `.tar.gz`
字节数回填 `info.json` 不会改变二进制，因此不存在"改清单→改二进制→改包体"的自指
震荡；连续两次 `make dist` 的 sha256 逐字一致，源与包内 `size_bytes` 均等于实际包体。
构建基线：`CGO_ENABLED=0 GOAMD64=v1 -trimpath -buildvcs=false -ldflags="-s -w"`。

MIT License — pengtang1024
