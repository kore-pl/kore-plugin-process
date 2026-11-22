# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
