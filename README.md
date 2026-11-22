# kore-plugin-process

kore 的进程管理插件（生产级）：**真实读取 Linux `/proc`**，提供进程列表、单进程详情、
信号投递与进程树。`process.list` 的 `cpu_pct` 用进程内 tick 快照做差分，**零后台
ticker**——只在 `list` 调用时顺带更新基线（G-19）。

## 协议方法（kore.plugin.v1）

| 方法 | 入参 | 结果 |
| --- | --- | --- |
| `kore.ping` | `{}` | `{status,uptime_s,goroutines,rss_kb}` |
| `kore.reload` | `{}` | `{ok:true,config_digest:""}`（无状态，空操作） |
| `kore.shutdown` | `{}` | `{ok:true}`，随后优雅退出 |
| `process.list` | `{sort?,limit?}` | `{schema,sampled_at,first_sample,cpu_note,sort,limit,count,total,masked_args,items[]}` |
| `process.detail` | `{pid}` | 单进程详情（见下） |
| `process.kill` | `{pid,signal?}` | `{schema,pid,signal,comm,killed,still_alive,waited_ms,note?}` |
| `process.tree` | `{pid}` | `{schema,root,count,truncated,max_depth,max_nodes,nodes[]}` |

### process.list

- 扫描 `/proc/<pid>/`，条目字段：`pid / name(comm) / state / user(uid→name) / uid /
  rss_kib / cpu_pct / cmdline / restricted / restricted_reason?`。
- `sort ∈ cpu|mem|pid|name`，缺省 `cpu`。`cpu` / `mem` 降序，`pid` / `name` 升序；
  `cpu` 同值时按 pid 降序（新进程优先），其它同值按 pid 升序，输出顺序确定。
- `limit` 缺省 `50`、范围 `1..500`；`total` 是截断前的扫描总数。
- `first_sample=true` 表示本次是插件启动后第一次采样：**`cpu_pct` 全为 0**，
  `cpu_note` 明确说明原因（无上次快照）；下一次 `list` 起才有差分值。
- 首次调用会更新基线；并发调用由采样器 mutex 串行化，不会互相污染快照。

### process.detail

`pid / ppid / name / state / user / uid / cmdline / exe(readlink) / cwd(readlink) /
status{state,vm_rss_kib,vm_peak_kib,threads} / start_ticks / clk_tck /
started_at / elapsed_s / restricted`。

启动时间 = `/proc/stat` 的 `btime` + `starttime / CLK_TCK`，`CLK_TCK` 固定 100
（Linux USER_HZ，`CGO_ENABLED=0` 下不依赖 `sysconf`）；`btime` 不可读时只回
`start_ticks`，不编造墙钟时间。

### process.kill

- `signal` 缺省 `TERM`，白名单 `TERM|KILL|HUP|INT`（大小写不敏感），其它值 `E_INVALID`。
- `killed=true` 表示**信号投递成功**；`still_alive` 表示 500ms 确认窗口后进程仍存活
  （僵尸 `Z` / 已死 `X` 视为不存活）。`note` 在仍存活时说明原因。
- 500ms 内每 50ms 读一次 `/proc/<pid>/stat` 的状态字符；探测本身失败时保守视为存活，
  不把一次 `EACCES` 伪造成"已杀"。

### process.tree

从一次 `/proc/*/stat` 扫描建 `ppid → children` 反查表，BFS 输出以 `pid` 为根的子树；
`depth` 以根为 0。深度 ≤ 10、节点 ≤ 500，命中任一上限时 `truncated=true`（不静默截断）。
根进程不存在返回 `E_NOTFOUND`；环路由 visited 集合兜住。

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
| `pid ≤ 1` | `E_INVALID`（init / 内核线程受保护） |
| 插件自身 pid | `E_UNAUTHORIZED`（`KORE_PLUGIN_SOCK` 进程绝不自杀） |
| kore 主进程 | `/proc/<pid>/comm == "kored"` → `E_UNAUTHORIZED` |
| `KORE_PROTECTED_PIDS` | 逗号分隔正整数名单，命中即 `E_UNAUTHORIZED` |
| 非 root | 只能向同 uid 进程发信号；`EPERM` → `E_UNAUTHORIZED`，`ESRCH` → `E_NOTFOUND` |
| 任意 `/proc` 路径 | 不支持：所有路径由固定根 + 数字 pid + 固定文件名拼接 |
| `/proc/kcore` | 永不读取 |

被拒绝的 kill 不会调用 `kill(2)`（护栏在投递前完成）。

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

- **Linux 真实现**；非 Linux 四个方法显式返回 `E_UNSUPPORTED`，不静默降级、不伪造数据。
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

- `internal/procfs`：真实 `/proc`（当前进程 detail、真实 sleep kill、真实 shell+sleep 父子树），
  加 fake 执行面覆盖排序（cpu/mem/pid/name）、limit 截断、首次采样全 0、闭环
  `pid≤1`/自身/`kored`/保护名单拒绝、僵尸判定、深度/节点上限、脱敏与 restricted。
- `tests/fault/`：四类故障注入（kill / timeout / disk / cgroup）覆盖四个方法，断言
  方法级错误码（`E_UPSTREAM` / `E_INTERNAL` / `E_UNSUPPORTED`）与无故障 sanity。
- `tests/e2e/`：真实插件二进制 + 假内核 UDS，全链路
  list（前 5 条）→ detail → tree → kill → 二次 list 消失 → `kill{pid:1}` 被拒 →
  kill 自身被拒 → cmdline 恰为 `sleep 300 --password=secret` 的脱敏验证 → shutdown；
  日志输出 `E2E_*` 证据行。

## 构建与打包

`Makefile` 的 `dist` 显式列出 `info.json`、`bin/process`、`SHA256SUMS`，用
`--sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner` + `gzip -9n` 保证确定性。
二进制只内嵌静态 `manifest.json`（不含 `size_bytes`），`dist` 把真实 `.tar.gz`
字节数回填 `info.json` 不会改变二进制，因此不存在"改清单→改二进制→改包体"的自指
震荡；连续两次 `make dist` 的 sha256 逐字一致，源与包内 `size_bytes` 均等于实际包体。
构建基线：`CGO_ENABLED=0 GOAMD64=v1 -trimpath -buildvcs=false -ldflags="-s -w"`。

MIT License — pengtang1024
