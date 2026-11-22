package procfs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// killParams 是 process.kill 入参；signal 缺省 TERM。
type killParams struct {
	PID    int    `json:"pid"`
	Signal string `json:"signal"`
}

// killResult 是 process.kill 结构：killed 表示信号投递成功，still_alive 表示 500ms 后仍存活。
type killResult struct {
	Schema     string `json:"schema"`
	PID        int    `json:"pid"`
	Signal     string `json:"signal"`
	Comm       string `json:"comm"`
	Killed     bool   `json:"killed"`
	StillAlive bool   `json:"still_alive"`
	WaitedMS   int64  `json:"waited_ms"`
	Note       string `json:"note,omitempty"`
}

// callKill 校验护栏后向目标进程投递信号，并在 500ms 内确认存活状态。
func (r *Runtime) callKill(raw json.RawMessage) (json.RawMessage, *pluginapi.Error) {
	var p killParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	if p.PID <= 1 {
		return nil, invalid(fmt.Sprintf("拒绝操作 pid %d：pid≤1 受保护（init / 内核线程）", p.PID))
	}
	sig, ok := ParseSignal(p.Signal)
	if !ok {
		return nil, invalid("signal 必须是 TERM/KILL/HUP/INT 之一")
	}
	if perr := r.checkProtected(p.PID); perr != nil {
		return nil, perr
	}
	record, err := r.src.Stat(p.PID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, notFound(fmt.Sprintf("pid %d 不存在或已退出", p.PID))
		}
		return nil, classify(err)
	}
	if record.Name == "kored" {
		return nil, unauthorized("拒绝向 kore 主进程（comm=kored）发送信号")
	}
	if err := r.src.Signal(p.PID, sig); err != nil {
		return nil, signalError(err)
	}
	stillAlive, waited := r.confirmAlive(p.PID)
	result := killResult{
		Schema: "process.kill.v1", PID: p.PID, Signal: string(sig), Comm: record.Name,
		Killed: true, StillAlive: stillAlive, WaitedMS: waited,
	}
	if stillAlive {
		result.Note = fmt.Sprintf("信号已投递，但进程在 %dms 内仍存活（可能忽略该信号或正在退出）", waited)
	}
	return encode(result)
}

// checkProtected 检查插件自身与配置保护名单；命中直接拒绝，不读 /proc。
func (r *Runtime) checkProtected(pid int) *pluginapi.Error {
	if pid == r.opts.SelfPID {
		return unauthorized("拒绝向插件自身发送信号")
	}
	for _, protected := range r.opts.ProtectedPIDs {
		if pid == protected {
			return unauthorized(fmt.Sprintf("拒绝操作 pid %d：在 KORE_PROTECTED_PIDS 保护名单内", pid))
		}
	}
	return nil
}

// signalError 把投递失败按 errno 映射为协议错误码。
func signalError(err error) *pluginapi.Error {
	switch {
	case isNoSuchProcess(err) || errors.Is(err, fs.ErrNotExist):
		return notFound("进程不存在或已退出")
	case errors.Is(err, fs.ErrPermission):
		return unauthorized("权限不足：只能向同 uid 进程发送信号")
	}
	return classify(err)
}

// confirmAlive 在 500ms 窗口内轮询进程状态：进程消失或进入僵尸（Z）/ 已死（X）即判定不存活；
// 探测本身失败时保守视为仍存活，不让一次 EACCES 把结果伪造成"已杀"。
func (r *Runtime) confirmAlive(pid int) (bool, int64) {
	start := time.Now()
	for {
		state, err := r.src.Probe(pid)
		if err != nil {
			if isNoSuchProcess(err) || errors.Is(err, fs.ErrNotExist) {
				return false, time.Since(start).Milliseconds()
			}
		} else if state == 'Z' || state == 'X' || state == 'x' {
			return false, time.Since(start).Milliseconds()
		}
		if time.Since(start) >= killConfirmWindow {
			return true, time.Since(start).Milliseconds()
		}
		time.Sleep(killProbeInterval)
	}
}
