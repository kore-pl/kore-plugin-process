# process 插件 v0.2.0 发版验收证据

- 日期：2026-11-23（Asia/Shanghai）
- 仓库：`/home/pt/Work/GolandProjects/kore-plugin-process`（未改动 `kore` 与 `kore-plugins` 任何文件；
  未改 `.kore-local.yaml` / 主实例运行态）
- 基线：HEAD `b40601f chore(release): bump v0.1.1`
- 发版提交：`67e99fd feat(process): 进程保护名单与 renice 优先级（0.2.0）`
- 任务板：team task-8

## 1. 读通结论（这批改动是否自洽 / 补了什么）

接手工作树为 **26 个未提交文件**（21 modified + 5 untracked），全量 `git diff` + 5 个新文件逐行读通。
**结论：主体自洽、无 TODO / FIXME / 未接线分支**（`grep -rn "TODO\|FIXME\|XXX" --include='*.go' .` 零命中，exit 1）。
发现并补完 4 处缺口（均为一致性缺口，不推翻实现）：

| # | 缺口 | 事实 | 补法 |
| --- | --- | --- | --- |
| 1 | **`raceprobe_test.go` 断言与实现/内核事实不一致**（真正跑挂的测试） | 原断言“exec 后 cmdline 恒非空”，首轮 `go test` 在 `i=1` 实测 `空 cmdline: name="sleep" state="R" restricted=false` → FAIL。真实瞬态存在，但实现的内核线程口径还有 `ppid ∈ {0,2}` 约束，**不会**误判该用户进程（见 `protect.go:isKernelThread`） | 改写为守护**真实安全性质**：空 cmdline 用户进程 `ppid` 不得为 0/2、`isKernelThread=false`、`protectionReason=""`；不再断言不成立的“恒非空”；另补**确定性单测**（`protect_test.go`：ppid=4242 的空 cmdline 记录不算内核线程、不受保护），不依赖瞬态是否被采样到 |
| 2 | helper 注释把瞬态后果写错 | `internal/procfs/helper_test.go`、`tests/e2e/helpers_test.go` 原注释称“直接 kill 会被内核线程口径误判（间歇 E_UNAUTHORIZED）”，与 ppid 守卫矛盾 | 改为准确理由：等 comm/argv 稳定后再做 name 复核与脱敏断言 |
| 3 | **README 仍停在 0.1.x 契约** | kill/tree 入参出参、`comm`→`name`、缺 `renice`、护栏表仍是 `pid≤1 → E_INVALID`、list/detail 缺 `nice`/`protected` | 按实现同步（协议表、kill 四层护栏、renice 段、tree v2 全树、九类护栏表、五个方法、测试清单）；`main.go` 包注释补“优先级调整” |
| 4 | 故障注入注释仍写“四个方法” | `tests/fault/*.go` 5 处 | 统一为“五个方法”（新增 renice），故障类别仍为四类 |

实现侧核对要点（均自洽）：

- `protect.go`：保护判定只依赖 `Record`（`/proc` 字段）与 `Options`（SelfPID / ParentPID / SocketDir / SelfExe /
  ProtectedPIDs），无网络或文件系统旁路；kill 与 renice 共用同一 `protectionReason`。
- `renice.go`：校验顺序 `pid<1 → confirm → value 0..19 → Stat → 保护名单 → name 复核 → 调高需 root →
  setpriority → EPERM/ESRCH 映射`；调高（数值调小）在触达执行面之前即 `E_NEED_ROOT`，测试用 `niceCalls` 证明未触达。
- `kill.go`：四层护栏（confirm / name 与 comm 复核 / KILL 需 force / 保护名单），schema v2，EPERM → `E_NEED_ROOT`。
- `tree.go`：`root` 缺省=全系统森林；`roots/total/children_count`；visited 防环；深度 10 / 节点 500 上限；
  单根与多根共用 `expandTree`，无死分支。
- `parse.go`：nice 取 stat 第 19 字段（索引 16），路径 `source.go` 轻量扫描也回填 exe 供保护名单识别；
  `platform_linux.go` 用 `syscall.Setpriority`，非 Linux 显式 `E_UNSUPPORTED` 不静默降级。
- `runtime.go` / `main.go`：`ParentPID=os.Getppid()`、`SocketDir=filepath.Dir(cfg.socket)`、`SelfExe=os.Executable()` 已接线。
- `methods.go:46` `case "list", "detail", "kill", "tree", "renice"` 与 `Runtime.Call` 的 5 个分发分支一一对应。
- `errors.go`：`codeNeedRoot = "E_NEED_ROOT"` 是**必要**的兼容写法——本仓 `go.mod` 依赖已发布
  `pluginapi v0.1.1`（模块缓存内无 `E_NEED_ROOT` 常量），而本地 `kore/pkg/pluginapi` 源码已收录；注释已说明 SDK 发版后替换回常量。

## 2. 契约核对（info.json / manifest.json / methods.go）

- `normalizeMethod` 实际业务方法：`list / detail / kill / tree / renice`（5 个），与 `Runtime.Call` 分支一致。
- **本仓 `info.json` 没有 `methods` 字段**；核对全部 24 个 `kore-plugin-*` 仓的 `info.json` 也**均无**
  `methods` 字段（协议文档里的 `methods` 是内核→插件的 hello 响应能力表，不是插件清单 schema）。
  → 结论：本仓既有惯例就是**不声明 methods**，沿用惯例，未发明新格式、未改 schema。
- `info.json` 与 `manifest.json` 的共有键 `id / version / protocol / requires_kore / capabilities` 逐项相等
  （`make dist` 内的 python 校验同样通过）；`requires_kore` 保持 `>=0.1.0 <0.2.0`（kore 协议未变）。
- 版本 bump：`info.json` / `manifest.json` → `0.2.0`；`CHANGELOG.md` 顶部新增 0.2.0 条目（中文，含保护名单、
  renice、kill/tree v2、e2e/fault 覆盖）。

## 3. 门禁命令与结果

工作目录 `/home/pt/Work/GolandProjects/kore-plugin-process`：

| 命令 | 退出码 | 关键输出 |
| --- | --- | --- |
| `gofmt -l .` | 0 | 空输出（无待格式化文件） |
| `GOWORK=off go vet ./...` | 0 | 无输出 |
| `GOWORK=off go test -count=1 -timeout 200s ./...` | 0 | `ok` ×4（主包 / internal/procfs / tests/e2e / tests/fault） |
| `GOWORK=off go test -race -count=1 -timeout 240s ./...` | 0 | `ok` ×4（含 raceprobe） |
| `GOWORK=off go test -count=2 -timeout 300s ./internal/procfs ./tests/e2e` | 0 | `ok` ×2（重复跑无异状） |

首轮（补缺口前）真实失败原文（作为缺口 1 的证据）：

```
--- FAIL: TestRaceProbeSpawnStat (0.00s)
    raceprobe_test.go:19: i=1 pid=4107168 空 cmdline: name="sleep" state="R" restricted=false reason=""
FAIL	github.com/pengtang1024/kore-plugin-process/internal/procfs	0.598s
```

补完后上表全绿。e2e 按本仓既有做法跑真实二进制 + 真实 `/proc`（Linux 本机），未引入隔离实例外的运行态改动。

## 4. 打包（确定性 / 两次一致）

`VERSION=0.2.0`，产物 `dist/process-0.2.0.tar.gz`：

| 轮次 | 命令 | 退出码 | sha256 | size_bytes（声明=实际） |
| --- | --- | --- | --- | --- |
| 第 1 次 | `make dist` | 0 | `b46b7b8704f1012dfd05d4833c01fa6b693c33bef0e61693b380b0d4afb1f88f` | 1328693 = 1328693 |
| 第 2 次 | `make dist` | 0 | `b46b7b8704f1012dfd05d4833c01fa6b693c33bef0e61693b380b0d4afb1f88f` | 1328693 = 1328693 |
| 干净归档 | `git archive HEAD \| tar -x -C /tmp/... && make dist` | 0 | `b46b7b8704f1012dfd05d4833c01fa6b693c33bef0e61693b380b0d4afb1f88f` | 1328693 |

两次包体 `cmp` 逐字节一致（`BYTE_IDENTICAL=yes`）；干净归档副本 sha 也一致。
**dist sha256 = `b46b7b8704f1012dfd05d4833c01fa6b693c33bef0e61693b380b0d4afb1f88f`，size_bytes = 1328693。**
临时归档目录与首轮副本已删除。

## 5. 提交 / tag

- 提交前 `git status --short` 逐项核对；**逐个精确 `git add <path>`（34 个路径，无 `git add .`）**：
  29 个 modified + 5 个新增（protect.go / protect_test.go / raceprobe_test.go / renice.go / renice_test.go）。
- commit `67e99fd20997cbfe72192d322cb669092d9422e1`，`34 files changed, 1352 insertions(+), 210 deletions(-)`。
- tag：`v0.2.0` 为 annotated tag（与既有 `v0.1.1` 同为 `tag` 对象），tag 对象
  `1f4a4098d13c30ce1b947d12176684bd86641677` → commit `67e99fd`；message `process v0.2.0`。
- 与任务卡的偏差（已在回报中说明）：任务卡要求单条 `feat(process): …（0.2.0）` 提交，而
  `docs/RELEASE.md` 建议 release 元数据单独 `chore(release)` 提交——按任务卡执行单条提交；
  tag 采用仓库既有 annotated 惯例（等价于 `git tag v0.2.0` 的目标）。

推送（origin 先、gitee 后，无 force、无移动既有 tag）：

| 远程 | refs/heads/main | refs/tags/v0.2.0 |
| --- | --- | --- |
| origin（GitHub） | `67e99fd`（`b40601f..67e99fd`） | `1f4a409`（new tag） |
| gitee（镜像） | `67e99fd` | `1f4a409`（new tag） |

## 6. Release 与资产复核

- release：<https://github.com/pengtang1024/kore-plugin-process/releases/tag/v0.2.0>（`gh release create` exit 0，非 draft）
- 资产：`process-0.2.0.tar.gz`，size 1328693，GitHub 侧 digest
  `sha256:b46b7b8704f1012dfd05d4833c01fa6b693c33bef0e61693b380b0d4afb1f88f`
- 下载复核：

```
curl -sL -w '%{http_code}' -o /tmp/dl-process-0.2.0.tar.gz \
  https://github.com/pengtang1024/kore-plugin-process/releases/download/v0.2.0/process-0.2.0.tar.gz
HTTP_CODE=200
b46b7b8704f1012dfd05d4833c01fa6b693c33bef0e61693b380b0d4afb1f88f  /tmp/dl-process-0.2.0.tar.gz
b46b7b8704f1012dfd05d4833c01fa6b693c33bef0e61693b380b0d4afb1f88f  dist/process-0.2.0.tar.gz
1328693
ASSET_IDENTICAL=yes
```

下载副本已删除。GitHub API 余量在推送前为 4974/5000，**未触发限流**。

## 7. 未过项 / 备注

- 本轮无失败门禁；无 BLOCKED 项。
- 按任务卡禁止项，**未更新 `kore-plugins` 索引**（版本 / sha256 / size_bytes / download_url / versions.lock.json
  仍指向 0.1.1），需索引 owner 在允许的写域内跟进；本仓 release 资产与 dist 已就绪。
- Gitee 仅做分支 + tag 镜像推送（均成功）；未创建 Gitee release 资产（无 Gitee release token，按项目规则
  Gitee 只保留最新 5 个 release，由镜像流程处理），记 WARN 不阻塞。
- 本证据文件在 release 之后单独提交（docs），因此不在 `v0.2.0` tag 内；tag 仍指向发版提交 `67e99fd`，
  dist 由该提交确定性复现。
