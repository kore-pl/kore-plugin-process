# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.3.0] - 2026-11-23

### Added

- `process.history`：系统级资源趋势（CPU / 内存 / load1），**60 点环形缓冲**；插件内零 ticker
  （G-19）——只在方法被调用时按时间戳采样，距上次不足 30s 直接复用上一点；state 0600 原子写，
  进程重启后从磁盘续读。
- `process.trend {pid, window?}`：单进程 CPU/RSS 趋势，用 `/proc/<pid>/stat` 的 utime+stime 差分；
  上次 tick 基线持久化在 state，**两次调用间隔 <2s 返回 `insufficient_data` + hint**，不伪造数据；
  pid 复用（tick 回绕）重置基线。
- `process.watchdog.suggest {pid|name}`：只生成 systemd unit 建议文本（`Restart=always` +
  `ExecStart`）与 `systemctl --user` / 系统级二选一说明，由用户执行；返回体明确
  「**kore 不做进程守护，守护归 systemd**」——不写 unit、不启服务、不起守护循环。
- 方法级 capability：三者均为 `system:read`（纯读+建议）；插件级补 `system:read` 以保持
  「方法级 ⊆ 插件级」。

### Tests

- `history_test.go`：环形追加/30s 复用/60 点截断、state 0600 与重启续读、trend 首次与间隔不足
  `insufficient_data`、CPU 差分（15%/20%）、watchdog 片段幂等与边界声明、pid/name 二选一。

## [0.2.2] - 2026-11-23

### Added

- `process.ports`（只读，`proc:read`）：排查「谁占了 8080」。读 `/proc/net/tcp{,6}` 的
  LISTEN（st=0A）条目，再用 `/proc/<pid>/fd` 符号链接按 `socket:[inode]` 反查归属，返回
  `listeners[]`（pid / name / protocol / address / port / inode）与 `fds[]`（socket 单独归类、
  敏感路径标记、`anon_inode:` 归 other）。入参 `{pid?}`：缺省/0 扫描全部进程（进程数 ≤2000、
  单进程 fd ≤512，命中上限 `truncated=true`）；只读 procfs、不联网、不起 ticker。
- `internal/procfs/ports_test.go`：tcp/tcp6 LISTEN 解析（含畸形行跳过）、fd 反查与三类归类、
  已建立连接不算监听、不存在 pid（`E_NOTFOUND`）与负 pid（`E_INVALID`）、全进程扫描跳过无 fd 进程。

## [0.2.1] - 2026-11-23

### Added

- `info.json` 新增 `method_capabilities`（5 条）：`process.list` / `process.detail` /
  `process.tree` → `proc:read`，`process.kill` / `process.renice` → `proc:write`。
  未声明时内核回退插件级 `capabilities` 取最高要求（含 `proc:write` → admin），只读方法
  也被按写调用审计；声明后只读方法不再进写审计，写方法仍按 admin 级判定。
- `method_capabilities_test.go`：「声明 vs 实现」双向一致性门禁——读 `info.json` 的方法
  清单断言每条都命中 `methods.go` 的 `businessVerbs` 且 `Runtime.Call` 有分支，反向断言
  每个业务方法都被声明；锁定只读/写分类、6 项特权能力白名单、方法级 ⊆ 插件级、
  `manifest.json` 身份/版本/能力同步。

### Changed

- `methods.go` 的业务动作清单抽为 `businessVerbs` 切片（原 switch 等价改写，行为不变），
  让 `info.json` 声明、方法分发与一致性测试共用单一事实来源。

## [0.2.0] - 2026-11-23

### Added

- 进程保护名单（`internal/procfs/protect.go`）：kill / renice 共用同一份策略，PID 1、
  插件自身、`KORE_PROTECTED_PIDS`、插件父进程（kored）、`comm=kored`、`comm` 带 `kore-`
  前缀、内核线程（cmdline 为空且 ppid ∈ {0,2}）、可执行文件位于 `<state>/plugins/` 同根
  或 socket 同目录的进程一律拒绝；判定只依赖 `/proc` 字段与运行期注入选项，拒绝原因
  逐条回给前端（`protected` / `protected_reason`）。
- `process.renice`：nice 取值 `0..19`，`confirm:true` 必填、`name` 可选复核 PID 复用；
  非 root 仅允许调大（降低优先级），调小或执行面 `EPERM` 返回 `E_NEED_ROOT`；结果含
  `previous` / `nice` / `changed`。
- list / detail 增加 `nice` 与 `protected` / `protected_reason` 字段。

### Changed

- `process.kill` 升级 schema v2：四层护栏（`confirm`、`name` 与 `/proc` comm 复核防 PID
  复用、SIGKILL 额外要 `force`、保护名单）；返回字段 `comm` → `name`；投递 `EPERM` 由
  `E_UNAUTHORIZED` 改为 `E_NEED_ROOT`，名称不符返回 `E_CONFLICT`。
- `process.tree` 升级 schema v2：`root` 缺省/0 输出全系统森林（`roots[]`），新增 `total`
  与节点 `children_count`，节点不再返回 `state`；子树模式入参 `pid` → `root`；全树与
  成环输入都不会死循环或重复输出。
- exec 后 `/proc/<pid>/cmdline` 可能短暂为空：保护名单以 ppid 判内核线程，空 cmdline 的
  用户进程不会被误判（`raceprobe_test.go` 守护该瞬态）。

### Tests

- `internal/procfs`：保护名单九类矩阵、kill 四层护栏、renice 真实 sleeper 回读 nice、
  全树森林与 PPID 成环、真实 kill / tree / 空 cmdline 瞬态。
- `tests/e2e`：list（含 protected）→ detail（含 nice）→ tree 子树+全树 → kill 七条拒绝
  路径 + TERM → 二次 list 消失 → renice 降优先级与被拒调高 → 脱敏 → shutdown。
- `tests/fault`：四类故障注入覆盖五个方法（新增 renice）。

## [0.1.1] - 2026-11-23

- build: 位级可复现打包（stage chmod + tar --mode 归一、size_bytes 收敛），二进制改嵌 manifest.json，消除同版本 sha 漂移

## [0.1.0] - 2026-11-22

### Added

- 进程管理插件：`process.list` / `process.detail` / `process.kill` / `process.tree`
  真实读取 Linux `/proc`，非 Linux 显式 `E_UNSUPPORTED`。
- `process.list`：扫描 `/proc/<pid>/` 输出 pid/name/state/user/rss_kib/cpu_pct/cmdline；
  `sort ∈ cpu|mem|pid|name`（cpu/mem 降序、pid/name 升序），`limit` 1..500、缺省 50。
- CPU 采样零 ticker：只在 `list` 调用时更新进程内 tick 基线，按
  `(utime+stime 差 / CLK_TCK) / 间隔 × 100` 差分；首次采样 cpu_pct 全 0 并附说明。
- `process.detail`：status 关键字段（State/VmRSS/VmPeak/Threads）+ cmdline + exe/cwd
  readlink + btime/starttime 换算启动时间。
- `process.kill`：默认 TERM，白名单 TERM/KILL/HUP/INT；拒 pid≤1、自身、comm=kored、
  `KORE_PROTECTED_PIDS`；500ms 内确认存活并返回 `{killed,still_alive,signal}`。
- `process.tree`：ppid 反查建树，深度 ≤10、节点 ≤500，超限标记 `truncated`。
- 安全边界：非 root 读取/EPERM 标记 `restricted=true` 不中断请求；不支持任意 /proc
  路径；`KORE_PROCESS_MASK_ARGS`（默认 true）脱敏 `--password=` / `--token=` 等参数值。
- 测试：真实 /proc 单元测试（排序/截断/首次采样/护栏/真实 sleep kill/真实父子树/
  restricted fixture）、四类故障注入（kill/timeout/disk/cgroup）、真实二进制 E2E
  （list→detail→tree→kill→二次 list→pid1 拒绝→自身拒绝→脱敏→shutdown）。
- 构建：确定性打包（显式条目 + 固定 mtime/owner + gzip -9n），`size_bytes` 回填，
  两次 `make dist` 的 sha256 一致。
