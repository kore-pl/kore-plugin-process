//go:build linux

package e2e

import (
	"encoding/json"
	"net"
	"testing"
	"time"
)

// listItem 是 process.list 的条目子集。
type listItem struct {
	PID        int     `json:"pid"`
	Name       string  `json:"name"`
	State      string  `json:"state"`
	User       string  `json:"user"`
	UID        uint32  `json:"uid"`
	Nice       int     `json:"nice"`
	RSSKiB     int64   `json:"rss_kib"`
	CPUPercent float64 `json:"cpu_pct"`
	Cmdline    string  `json:"cmdline"`
	Restricted bool    `json:"restricted"`
	Protected  bool    `json:"protected"`
}

// listResult 是 process.list 的结果子集。
type listResult struct {
	Schema      string     `json:"schema"`
	FirstSample bool       `json:"first_sample"`
	CPUNote     string     `json:"cpu_note"`
	Sort        string     `json:"sort"`
	Limit       int        `json:"limit"`
	Count       int        `json:"count"`
	Total       int        `json:"total"`
	MaskedArgs  bool       `json:"masked_args"`
	Items       []listItem `json:"items"`
}

// detailStatus 是 process.detail.status 子集。
type detailStatus struct {
	State     string `json:"state"`
	VmRSSKiB  int64  `json:"vm_rss_kib"`
	VmPeakKiB int64  `json:"vm_peak_kib"`
	Threads   int    `json:"threads"`
}

// detailResult 是 process.detail 的结果子集。
type detailResult struct {
	Schema     string       `json:"schema"`
	PID        int          `json:"pid"`
	PPID       int          `json:"ppid"`
	Name       string       `json:"name"`
	State      string       `json:"state"`
	User       string       `json:"user"`
	Nice       int          `json:"nice"`
	Cmdline    string       `json:"cmdline"`
	Exe        string       `json:"exe"`
	Cwd        string       `json:"cwd"`
	Status     detailStatus `json:"status"`
	StartTicks uint64       `json:"start_ticks"`
	CLKTCK     int64        `json:"clk_tck"`
	StartedAt  string       `json:"started_at"`
	ElapsedS   int64        `json:"elapsed_s"`
	Restricted bool         `json:"restricted"`
	Protected  bool         `json:"protected"`
}

// killResult 是 process.kill 的结果子集（v2：name 取代 comm）。
type killResult struct {
	Schema     string `json:"schema"`
	PID        int    `json:"pid"`
	Signal     string `json:"signal"`
	Name       string `json:"name"`
	Killed     bool   `json:"killed"`
	StillAlive bool   `json:"still_alive"`
	WaitedMS   int64  `json:"waited_ms"`
	Note       string `json:"note"`
}

// reniceResult 是 process.renice 的结果子集。
type reniceResult struct {
	Schema   string `json:"schema"`
	PID      int    `json:"pid"`
	Name     string `json:"name"`
	Previous int    `json:"previous"`
	Nice     int    `json:"nice"`
	Changed  bool   `json:"changed"`
}

// treeNode 是 process.tree 的节点子集（v2：children_count 取代 state）。
type treeNode struct {
	PID           int    `json:"pid"`
	PPID          int    `json:"ppid"`
	Name          string `json:"name"`
	Depth         int    `json:"depth"`
	ChildrenCount int    `json:"children_count"`
}

// treeResult 是 process.tree 的结果子集（v2：roots[] 取代 root）。
type treeResult struct {
	Schema    string     `json:"schema"`
	Roots     []int      `json:"roots"`
	Count     int        `json:"count"`
	Total     int        `json:"total"`
	Truncated bool       `json:"truncated"`
	MaxDepth  int        `json:"max_depth"`
	MaxNodes  int        `json:"max_nodes"`
	Nodes     []treeNode `json:"nodes"`
}

// parseList 解析 list 结果。
func parseList(t *testing.T, raw string) listResult {
	t.Helper()
	var result listResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("解析 list 失败: %v (%s)", err, raw)
	}
	return result
}

// parseDetail 解析 detail 结果。
func parseDetail(t *testing.T, raw string) detailResult {
	t.Helper()
	var result detailResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("解析 detail 失败: %v (%s)", err, raw)
	}
	return result
}

// parseKill 解析 kill 结果。
func parseKill(t *testing.T, raw string) killResult {
	t.Helper()
	var result killResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("解析 kill 失败: %v (%s)", err, raw)
	}
	return result
}

// parseTree 解析 tree 结果。
func parseTree(t *testing.T, raw string) treeResult {
	t.Helper()
	var result treeResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("解析 tree 失败: %v (%s)", err, raw)
	}
	return result
}

// parseRenice 解析 renice 结果。
func parseRenice(t *testing.T, raw string) reniceResult {
	t.Helper()
	var result reniceResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("解析 renice 失败: %v (%s)", err, raw)
	}
	return result
}

// findItem 在列表中查找 pid。
func findItem(items []listItem, pid int) (listItem, bool) {
	for _, item := range items {
		if item.PID == pid {
			return item, true
		}
	}
	return listItem{}, false
}

// pollListItem 反复以 limit=500 拉列表直到找到 pid；默认 cpu 排序下新起的低 CPU 进程
// 在 0 值区按 pid 降序靠前，500 条足以覆盖本机全部进程。
func pollListItem(t *testing.T, conn net.Conn, pid int) (listItem, bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		listed := parseList(t, mustCall(t, conn, "e2e-list-poll", "process.list", map[string]any{"limit": 500}))
		if item, ok := findItem(listed.Items, pid); ok {
			return item, true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return listItem{}, false
}

// itemsJSON 返回前 n 条条目的紧凑 JSON，作为可粘贴证据。
func itemsJSON(t *testing.T, items []listItem, n int) string {
	t.Helper()
	if len(items) > n {
		items = items[:n]
	}
	raw, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("序列化证据条目失败: %v", err)
	}
	return string(raw)
}
