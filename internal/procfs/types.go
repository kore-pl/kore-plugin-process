// Package procfs 实现 process 插件的方法面：真实读取 Linux /proc、CPU 差分成采样、
// 信号投递与进程树构建。
//
// 边界：真实实现只在 Linux 上生效，非 Linux 由 Runtime.Call 显式返回 E_UNSUPPORTED；
// 本包不启动任何后台 goroutine / ticker（G-19）——CPU 基线只在 process.list 调用时顺带更新。
package procfs

import (
	"fmt"
	"strings"
	"time"
)

// 采样、分页与安全边界常量。
const (
	// CLKTCK 是 Linux 用户态时钟频率（USER_HZ），容器与发行版默认 100。
	// 固定 100 而不是 cgo sysconf：CGO_ENABLED=0 下无 libc，且 USER_HZ=100 是 Linux ABI 事实。
	CLKTCK int64 = 100
	// DefaultProcRoot 是真实 /proc 挂载点；测试可指向临时目录。
	DefaultProcRoot = "/proc"
	// DefaultLimit 与 MaxLimit 是 process.list 的默认条数与硬上限。
	DefaultLimit = 50
	MaxLimit     = 500
	// MaxTreeDepth 与 MaxTreeNodes 是 process.tree 的深度与节点硬上限。
	MaxTreeDepth = 10
	MaxTreeNodes = 500

	// cmdlineLimit 是单进程 cmdline 的读取上限，防止异常超长命令行撑爆内存。
	cmdlineLimit = 64 << 10
	// killConfirmWindow 是 process.kill 发信号后确认存活状态的时间窗。
	killConfirmWindow = 500 * time.Millisecond
	// killProbeInterval 是该时间窗内的轮询间隔。
	killProbeInterval = 50 * time.Millisecond
)

// FaultKind 标识一次故障注入的类别（与 file / monitor 插件同一口径）。
type FaultKind string

// 四类故障注入类别。
const (
	FaultNone    FaultKind = ""
	FaultKill    FaultKind = "kill"
	FaultTimeout FaultKind = "timeout"
	FaultDisk    FaultKind = "disk"
	FaultCgroup  FaultKind = "cgroup"
)

// FaultError 是执行面注入的故障错误，classify 将其映射为协议错误码。
type FaultError struct {
	Kind    FaultKind
	Message string
}

// Error 实现 error 接口。
func (e *FaultError) Error() string { return fmt.Sprintf("%s: %s", e.Kind, e.Message) }

// NewFaultError 构造给定类别的故障错误，供故障注入 Source 使用。
func NewFaultError(kind FaultKind, message string) error {
	return &FaultError{Kind: kind, Message: message}
}

// ErrKind 是执行面业务错误的语义类别，classify 将其映射为协议错误码。
type ErrKind string

// 业务错误类别。
const (
	ErrInvalid     ErrKind = "invalid"
	ErrNotFound    ErrKind = "notfound"
	ErrPermission  ErrKind = "permission"
	ErrUnsupported ErrKind = "unsupported"
	ErrInternal    ErrKind = "internal"
)

// OpError 是执行面返回的可分类业务错误；Message 不得包含宿主绝对路径。
type OpError struct {
	Kind    ErrKind
	Message string
}

// Error 实现 error 接口。
func (e *OpError) Error() string { return string(e.Kind) + ": " + e.Message }

// NewOpError 构造 OpError。
func NewOpError(kind ErrKind, message string) *OpError {
	return &OpError{Kind: kind, Message: message}
}

// Signal 是 process.kill 允许投递的信号名（白名单，其它值 E_INVALID）。
type Signal string

// 允许的信号集合：默认 TERM，另允许 KILL / HUP / INT。
const (
	SignalTERM Signal = "TERM"
	SignalKILL Signal = "KILL"
	SignalHUP  Signal = "HUP"
	SignalINT  Signal = "INT"
)

// ParseSignal 把入参归一为白名单信号；空串取默认 TERM，非法值返回 false。
func ParseSignal(raw string) (Signal, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return SignalTERM, true
	}
	switch sig := Signal(strings.ToUpper(trimmed)); sig {
	case SignalTERM, SignalKILL, SignalHUP, SignalINT:
		return sig, true
	default:
		return "", false
	}
}

// Record 是单个进程的原始采集记录；Scan 填公共字段，Stat 额外填 exe/cwd/线程等详情。
// 字段为内部原始类型，对外编码时再格式化（user 名、时间、CPU 百分比）。
type Record struct {
	PID              int
	Name             string
	State            byte
	PPID             int
	UID              uint32
	Nice             int
	RSSKiB           int64
	VmPeakKiB        int64
	Threads          int
	UTicks           uint64
	STicks           uint64
	StartTicks       uint64
	Cmdline          string
	Exe              string
	Cwd              string
	Restricted       bool
	RestrictedReason string
}

// Ticks 返回该进程累计 CPU 时间（utime + stime，单位 tick）。
func (r Record) Ticks() uint64 { return r.UTicks + r.STicks }

// StateString 把 /proc/<pid>/stat 的状态字符转成字符串；缺失时返回空串。
func (r Record) StateString() string {
	if r.State == 0 {
		return ""
	}
	return string(r.State)
}

// Source 是 /proc 采集与信号 / 优先级投递的执行面；nil 时 Runtime 使用真实 Linux 实现。
// 故障注入测试用一个始终返回 FaultError 的实现覆盖五个方法。
type Source interface {
	// Scan 扫描全部可读进程的公共字段（不含 exe/cwd 等重字段）。
	Scan() ([]Record, error)
	// Stat 读取单个进程的详情字段。
	Stat(pid int) (Record, error)
	// Signal 向进程投递已校验的信号。
	Signal(pid int, sig Signal) error
	// SetNice 调整进程 nice 值（等价 renice(1)）。
	SetNice(pid int, value int) error
	// Probe 读取进程当前状态字符，用于 kill 后确认存活。
	Probe(pid int) (byte, error)
}
