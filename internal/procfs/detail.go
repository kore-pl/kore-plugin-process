package procfs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// statusBlock 是 /proc/<pid>/status 的关键字段。
type statusBlock struct {
	State     string `json:"state"`
	VmRSSKiB  int64  `json:"vm_rss_kib"`
	VmPeakKiB int64  `json:"vm_peak_kib"`
	Threads   int    `json:"threads"`
}

// detailResult 是 process.detail 的结构。
type detailResult struct {
	Schema           string      `json:"schema"`
	PID              int         `json:"pid"`
	PPID             int         `json:"ppid"`
	Name             string      `json:"name"`
	State            string      `json:"state"`
	User             string      `json:"user"`
	UID              uint32      `json:"uid"`
	Nice             int         `json:"nice"`
	Cmdline          string      `json:"cmdline"`
	Exe              string      `json:"exe"`
	Cwd              string      `json:"cwd"`
	Status           statusBlock `json:"status"`
	StartTicks       uint64      `json:"start_ticks"`
	CLKTCK           int64       `json:"clk_tck"`
	StartedAt        string      `json:"started_at"`
	ElapsedS         int64       `json:"elapsed_s"`
	Restricted       bool        `json:"restricted"`
	RestrictedReason string      `json:"restricted_reason,omitempty"`
	Protected        bool        `json:"protected"`
	ProtectedReason  string      `json:"protected_reason,omitempty"`
}

// detailParams 是 process.detail 入参。
type detailParams struct {
	PID int `json:"pid"`
}

// callDetail 读取单进程详情：status 关键字段 + cmdline + exe/cwd 链接 + 启动时间。
func (r *Runtime) callDetail(raw json.RawMessage) (json.RawMessage, *pluginapi.Error) {
	var p detailParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	if p.PID <= 0 {
		return nil, invalid("pid 必须为正整数")
	}
	record, err := r.src.Stat(p.PID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, notFound(fmt.Sprintf("pid %d 不存在或已退出", p.PID))
		}
		return nil, classify(err)
	}
	startedAt, elapsed := r.startTime(record)
	reason := r.protectionReason(record)
	return encode(detailResult{
		Schema:           "process.detail.v1",
		PID:              record.PID,
		PPID:             record.PPID,
		Name:             record.Name,
		State:            record.StateString(),
		User:             r.users.lookup(record.UID),
		UID:              record.UID,
		Nice:             record.Nice,
		Cmdline:          maskCmdline(record.Cmdline, r.opts.MaskArgs),
		Exe:              record.Exe,
		Cwd:              record.Cwd,
		Status:           statusBlock{State: record.StateString(), VmRSSKiB: record.RSSKiB, VmPeakKiB: record.VmPeakKiB, Threads: record.Threads},
		StartTicks:       record.StartTicks,
		CLKTCK:           CLKTCK,
		StartedAt:        startedAt,
		ElapsedS:         elapsed,
		Restricted:       record.Restricted,
		RestrictedReason: record.RestrictedReason,
		Protected:        reason != "",
		ProtectedReason:  reason,
	})
}

// startTime 用 /proc/stat 的 btime + starttime ticks 换算启动墙钟时间与已运行秒数。
// btime 不可读时只回 start_ticks（不编造时间）。
func (r *Runtime) startTime(record Record) (string, int64) {
	btime, err := readBootTime(r.opts.ProcRoot)
	if err != nil {
		return "", 0
	}
	started := time.Unix(btime, 0).Add(time.Duration(record.StartTicks) * time.Second / time.Duration(CLKTCK))
	elapsed := int64(r.now().Sub(started).Seconds())
	if elapsed < 0 {
		elapsed = 0
	}
	return started.Format(time.RFC3339), elapsed
}
