package procfs

import (
	"encoding/json"
	"runtime"
	"strings"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// Options 是 Runtime 的运行期配置：由 main 从环境变量组装，测试用零值 + 注入项。
type Options struct {
	// ProcRoot 是 procfs 根，真实运行取 /proc；测试可指向临时目录。
	ProcRoot string
	// SelfPID 是插件自身 pid；kill / renice 拒绝操作它。
	SelfPID int
	// ParentPID 是插件父进程 pid（生产环境即 kored）；保护名单一员。
	ParentPID int
	// SocketDir 是插件 socket 所在目录；可执行文件在此目录的进程视为 kore 组件。
	SocketDir string
	// SelfExe 是插件自身可执行文件路径；用于推断 kore 插件状态树前缀（<state>/plugins/），
	// 同根的其它插件进程因此受保护。空串关闭该检查。
	SelfExe string
	// MaskArgs 控制 cmdline 中敏感参数是否脱敏（默认 true）。
	MaskArgs bool
	// ProtectedPIDs 是额外保护名单（来自 KORE_PROTECTED_PIDS）。
	ProtectedPIDs []int
	// GOOS 为空时取 runtime.GOOS；测试可注入 "darwin" 验证 E_UNSUPPORTED。
	GOOS string
	// Now 为空时取 time.Now；测试注入假时钟验证 CPU 差分。
	Now func() time.Time
}

// normalize 填充默认值。
func (o Options) normalize() Options {
	if o.ProcRoot == "" {
		o.ProcRoot = DefaultProcRoot
	}
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// Runtime 是五个 process 方法的生命周期容器：持有 /proc 执行面、CPU 采样器与 uid 缓存，
// 由插件进程内唯一实例持有（宿主重启插件即重置采样基线）。
type Runtime struct {
	opts          Options
	src           Source
	sampler       *Sampler
	users         *userCache
	pluginsPrefix string // 由 SelfExe 推断的 kore 插件状态树前缀；空串关闭该保护检查
}

// NewRuntime 构造 Runtime；src 为 nil 时使用真实 Linux /proc 执行面。
func NewRuntime(opts Options, src Source) *Runtime {
	opts = opts.normalize()
	if src == nil {
		src = newProcSource(opts.ProcRoot)
	}
	return &Runtime{opts: opts, src: src, sampler: NewSampler(), users: newUserCache(), pluginsPrefix: pluginsPrefixOf(opts.SelfExe)}
}

// pluginsPrefix 从插件自身 exe 推断插件状态树前缀：
// <state>/plugins/process/0.3.0/bin/process → <state>/plugins/。
// exe 路径不含 /plugins/ 时返回空串（如开发环境直跑测试二进制），保护检查退化为按 comm 判定。
func pluginsPrefixOf(selfExe string) string {
	if selfExe == "" {
		return ""
	}
	at := strings.Index(selfExe, "/plugins/")
	if at < 0 {
		return ""
	}
	return selfExe[:at+len("/plugins/")]
}

// Call 执行一个 process 方法（方法名已是 process.<verb> 规范形式）。
func (r *Runtime) Call(method string, params json.RawMessage) (json.RawMessage, *pluginapi.Error) {
	if r.opts.GOOS != "linux" {
		return nil, unsupported("process 插件只在 Linux 提供真实实现（当前 " + r.opts.GOOS + "）")
	}
	switch method {
	case "process.list":
		return r.callList(params)
	case "process.detail":
		return r.callDetail(params)
	case "process.kill":
		return r.callKill(params)
	case "process.tree":
		return r.callTree(params)
	case "process.renice":
		return r.callRenice(params)
	default:
		return nil, notFound("未知方法: " + method)
	}
}

// now 返回本次调用的采样时刻。
func (r *Runtime) now() time.Time { return r.opts.Now() }

// decodeParams 校验 params 是 JSON 对象并反序列化到目标形状；缺省/null 视为空对象。
func decodeParams(raw json.RawMessage, target any) *pluginapi.Error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if raw[0] != '{' {
		return invalid("params 必须是 JSON 对象")
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return invalid("params 解析失败: " + err.Error())
	}
	return nil
}

// encode 把结果编码为 json.RawMessage；序列化失败归 E_INTERNAL。
func encode(v any) (json.RawMessage, *pluginapi.Error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, internalErr("序列化结果失败: " + err.Error())
	}
	return data, nil
}
