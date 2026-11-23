package procfs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// reniceParams 是 process.renice 入参：value 取 0..19（数值越大优先级越低）。
// 非特权用户只允许把数值调大（降低优先级）；调小（提高优先级）需要 root。
// name 可选：提供时做与 kill 相同的 PID 复用复核。
type reniceParams struct {
	PID     int    `json:"pid"`
	Value   *int   `json:"value"`
	Confirm bool   `json:"confirm"`
	Name    string `json:"name"`
}

// reniceResult 是 process.renice 结构。
type reniceResult struct {
	Schema   string `json:"schema"`
	PID      int    `json:"pid"`
	Name     string `json:"name"`
	Previous int    `json:"previous"`
	Nice     int    `json:"nice"`
	Changed  bool   `json:"changed"`
}

// callRenice 校验护栏后调整进程 nice 值。保护名单与 kill 共用同一份策略：
// 调整 kored / 内核线程 / 其他 kore 组件的调度优先级同样是破坏性操作。
func (r *Runtime) callRenice(raw json.RawMessage) (json.RawMessage, *pluginapi.Error) {
	var p reniceParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	if p.PID < 1 {
		return nil, invalid(fmt.Sprintf("pid 必须为正整数（实际 %d）", p.PID))
	}
	if !p.Confirm {
		return nil, invalid("缺少 confirm:true：调整优先级会改变进程调度行为，必须显式确认")
	}
	if p.Value == nil {
		return nil, invalid("缺少 value：nice 取值 0..19（数值越大优先级越低）")
	}
	value := *p.Value
	if value < 0 || value > 19 {
		return nil, invalid(fmt.Sprintf("nice 必须在 0..19 之间（实际 %d）", value))
	}
	record, err := r.src.Stat(p.PID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, notFound(fmt.Sprintf("pid %d 不存在或已退出", p.PID))
		}
		return nil, classify(err)
	}
	if reason := r.protectionReason(record); reason != "" {
		return nil, unauthorized("拒绝调整优先级：" + reason)
	}
	if name := trimName(p.Name); name != "" && name != record.Name {
		return nil, conflict(fmt.Sprintf("进程名不符：pid %d 当前是 %q，入参是 %q（可能 PID 已复用），请刷新后重试", p.PID, record.Name, name))
	}
	if value < record.Nice {
		return nil, needRoot(fmt.Sprintf("调高优先级（nice %d → %d）需要 root 权限：请用 sudo 启动 kored", record.Nice, value))
	}
	if err := r.src.SetNice(p.PID, value); err != nil {
		switch {
		case isNoSuchProcess(err) || errors.Is(err, fs.ErrNotExist):
			return nil, notFound("进程不存在或已退出")
		case errors.Is(err, fs.ErrPermission):
			return nil, needRoot("权限不足：目标进程属于其他用户（或需要降低 nice 值）。需 root 权限：请用 sudo 启动 kored")
		}
		return nil, classify(err)
	}
	return encode(reniceResult{
		Schema: "process.renice.v1", PID: p.PID, Name: record.Name,
		Previous: record.Nice, Nice: value, Changed: value != record.Nice,
	})
}
