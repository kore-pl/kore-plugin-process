package procfs

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// ProcessEntry 是 process.list 返回的单条进程记录；字段显式列出，不使用 any / object。
// protected=true 表示该进程在保护名单内（前端据此加锁标并禁用终止 / 优先级操作）。
type ProcessEntry struct {
	PID              int     `json:"pid"`
	Name             string  `json:"name"`
	State            string  `json:"state"`
	User             string  `json:"user"`
	UID              uint32  `json:"uid"`
	Nice             int     `json:"nice"`
	RSSKiB           int64   `json:"rss_kib"`
	CPUPercent       float64 `json:"cpu_pct"`
	Cmdline          string  `json:"cmdline"`
	Restricted       bool    `json:"restricted"`
	RestrictedReason string  `json:"restricted_reason,omitempty"`
	Protected        bool    `json:"protected"`
	ProtectedReason  string  `json:"protected_reason,omitempty"`
}

// listResult 是 process.list 的结构；first_sample=true 时 cpu_pct 全 0。
type listResult struct {
	Schema      string         `json:"schema"`
	SampledAt   string         `json:"sampled_at"`
	FirstSample bool           `json:"first_sample"`
	CPUNote     string         `json:"cpu_note"`
	Sort        string         `json:"sort"`
	Limit       int            `json:"limit"`
	Count       int            `json:"count"`
	Total       int            `json:"total"`
	MaskedArgs  bool           `json:"masked_args"`
	Items       []ProcessEntry `json:"items"`
}

// listParams 是 process.list 入参：sort 缺省 cpu，limit 缺省 50。
type listParams struct {
	Sort  string `json:"sort"`
	Limit *int   `json:"limit"`
}

// callList 扫描 /proc、用当前调用更新 CPU 基线、排序并截断后返回列表。
func (r *Runtime) callList(raw json.RawMessage) (json.RawMessage, *pluginapi.Error) {
	var p listParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	sortKey := p.Sort
	if sortKey == "" {
		sortKey = SortCPU
	}
	if !validSort(sortKey) {
		return nil, invalid("sort 必须是 cpu/mem/pid/name 之一")
	}
	limit := DefaultLimit
	if p.Limit != nil {
		limit = *p.Limit
	}
	if limit < 1 || limit > MaxLimit {
		return nil, invalid(fmt.Sprintf("limit 必须在 1..%d 之间", MaxLimit))
	}
	records, err := r.src.Scan()
	if err != nil {
		return nil, classify(err)
	}
	now := r.now()
	cpu, first := r.sampler.Sample(now, records)
	sortRecords(records, sortKey, cpu)
	result := listResult{
		Schema:      "process.list.v1",
		SampledAt:   now.Format(time.RFC3339Nano),
		FirstSample: first,
		CPUNote:     cpuNote(first),
		Sort:        sortKey,
		Limit:       limit,
		Total:       len(records),
		MaskedArgs:  r.opts.MaskArgs,
	}
	if len(records) > limit {
		records = records[:limit]
	}
	result.Items = make([]ProcessEntry, 0, len(records))
	for _, record := range records {
		result.Items = append(result.Items, r.toEntry(record, cpu[record.PID]))
	}
	result.Count = len(result.Items)
	return encode(result)
}

// toEntry 把内部记录格式化为对外条目（用户名称、脱敏命令行、CPU 百分比、保护标记）。
func (r *Runtime) toEntry(record Record, cpuPercent float64) ProcessEntry {
	reason := r.protectionReason(record)
	return ProcessEntry{
		PID:              record.PID,
		Name:             record.Name,
		State:            record.StateString(),
		User:             r.users.lookup(record.UID),
		UID:              record.UID,
		Nice:             record.Nice,
		RSSKiB:           record.RSSKiB,
		CPUPercent:       cpuPercent,
		Cmdline:          maskCmdline(record.Cmdline, r.opts.MaskArgs),
		Restricted:       record.Restricted,
		RestrictedReason: record.RestrictedReason,
		Protected:        reason != "",
		ProtectedReason:  reason,
	}
}

// cpuNote 说明 cpu_pct 的口径；首次采样明确告知无基线、全 0。
func cpuNote(first bool) string {
	if first {
		return "首次采样：无上次快照，cpu_pct 全为 0；下一次 process.list 起按 utime+stime 差分计算"
	}
	return "cpu_pct = (utime+stime 差 / CLK_TCK) / 距上次 list 的秒数 × 100，单核归一，多核满载可超过 100"
}
