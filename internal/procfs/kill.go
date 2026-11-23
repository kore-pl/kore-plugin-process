package procfs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// killParams 是 process.kill 入参：四层防误杀——
// ① confirm:true 显式确认；② 前端手输进程名（name）；③ 服务端拿 name 与
// /proc/<pid>/stat 的 comm 复核（防 PID 复用）；④ 保护名单（protect.go）。
// signal 缺省 TERM；KILL 属不可恢复，额外要求 force:true。
type killParams struct {
	PID     int    `json:"pid"`
	Signal  string `json:"signal"`
	Confirm bool   `json:"confirm"`
	Force   bool   `json:"force"`
	Name    string `json:"name"`
}

// killResult 是 process.kill 结构：killed 表示信号投递成功，still_alive 表示 500ms 后仍存活。
type killResult struct {
	Schema     string `json:"schema"`
	PID        int    `json:"pid"`
	Signal     string `json:"signal"`
	Name       string `json:"name"`
	Killed     bool   `json:"killed"`
	StillAlive bool   `json:"still_alive"`
	WaitedMS   int64  `json:"waited_ms"`
	Note       string `json:"note,omitempty"`
}

// callKill 校验四层护栏后向目标进程投递信号，并在 500ms 内确认存活状态。
func (r *Runtime) callKill(raw json.RawMessage) (json.RawMessage, *pluginapi.Error) {
	var p killParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	if p.PID < 1 {
		return nil, invalid(fmt.Sprintf("pid 必须为正整数（实际 %d）", p.PID))
	}
	if !p.Confirm {
		return nil, invalid("缺少 confirm:true：终止进程不可自动重试，必须显式确认")
	}
	sig, ok := ParseSignal(p.Signal)
	if !ok {
		return nil, invalid("signal 必须是 TERM/KILL/HUP/INT 之一")
	}
	if sig == SignalKILL && !p.Force {
		return nil, invalid("SIGKILL 不可恢复：需要 force:true 与 confirm:true 同时确认")
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return nil, invalid("缺少 name：需提供当前进程名以防 PID 复用误杀")
	}
	record, err := r.src.Stat(p.PID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, notFound(fmt.Sprintf("pid %d 不存在或已退出", p.PID))
		}
		return nil, classify(err)
	}
	if reason := r.protectionReason(record); reason != "" {
		return nil, unauthorized("拒绝终止：" + reason)
	}
	if p.Name != record.Name {
		return nil, conflict(fmt.Sprintf("进程名不符：pid %d 当前是 %q，入参是 %q（可能 PID 已复用），请刷新后重试", p.PID, record.Name, p.Name))
	}
	if err := r.src.Signal(p.PID, sig); err != nil {
		return nil, signalError(err)
	}
	stillAlive, waited := r.confirmAlive(p.PID)
	result := killResult{
		Schema: "process.kill.v2", PID: p.PID, Signal: string(sig), Name: record.Name,
		Killed: true, StillAlive: stillAlive, WaitedMS: waited,
	}
	if stillAlive {
		result.Note = fmt.Sprintf("信号已投递，但进程在 %dms 内仍存活（可能忽略该信号或正在退出）", waited)
	}
	return encode(result)
}

// trimName 去掉进程名首尾空白（kill / renice 复用）。
func trimName(name string) string { return strings.TrimSpace(name) }

// signalError 把投递失败按 errno 映射为协议错误码。
// EPERM 意味着目标进程属于其他用户（或受内核保护），按 ADR-0018 统一降级口径返回
// E_NEED_ROOT，message 内附可操作提示；保护名单类拒绝在投递前已由 protectionReason 拦截。
func signalError(err error) *pluginapi.Error {
	switch {
	case isNoSuchProcess(err) || errors.Is(err, fs.ErrNotExist):
		return notFound("进程不存在或已退出")
	case errors.Is(err, fs.ErrPermission):
		return needRoot("权限不足：目标进程属于其他用户。需 root 权限：请用 sudo 启动 kored")
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
