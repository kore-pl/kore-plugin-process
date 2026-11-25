# kore-plugin-process（进程管理插件）

## 它做什么

真实读取 Linux `/proc`，提供进程列表、单进程详情、信号投递、进程树与优先级调整（renice）。生产级。kill 与 renice 共用同一份进程保护名单。`process.list` 的 `cpu_pct` 用进程内 tick 快照做差分，**零后台 ticker**——只在 `list` 调用时顺带更新基线（G-19）。

## 方法

| 方法 | 入参 | 行为 |
| --- | --- | --- |
| `kore.ping` | `{}` | `{status,uptime_s,goroutines,rss_kb}`。 |
| `kore.reload` | `{}` | `{ok:true,config_digest:""}`（无状态空操作）。 |
| `kore.shutdown` | `{}` | `{ok:true}`，随后优雅退出。 |
| `process.list` | `sort?`、`limit?` | 进程列表，`cpu_pct` 由 tick 差分得出；返回 `masked_args` 标记脱敏项。 |
| `process.detail` | `{pid}` | 单进程详情。 |
| `process.kill` | `{pid, confirm, name, signal?, force?}` | 信号投递；受保护名单与二次确认约束。 |
| `process.renice` | `{pid, value, confirm, name?}` | 调整 nice 值；与 kill 共用保护名单。 |
| `process.tree` | `{root?}` | 进程树，含 `max_depth` / `max_nodes` / `truncated`。 |
| `process.ports` | `{pid?}` | 扫描监听端口与 fds。 |
| `process.history` / `process.trend` | `{}` / `{pid, window?}` | 历史采样点与单进程趋势。 |
| `process.watchdog.suggest` | `{pid?/name?}` | 生成 systemd 单元建议，标注边界。 |

## 安全边界

kill / renice 需要 `confirm=true` 与目标进程名，命中保护名单（自身、内核线程、主壳等）时拒绝。只读方法声明 `system:read`，写类方法为 `system:admin`。

## 安装与验证

从插件市场安装 `process`，或通过 `make dist` 产出 `dist/process-0.3.0.tar.gz` 导入。开发验证：`make check`（gofmt → go vet → go test -timeout 200s），含 boundary / fault 测试。
