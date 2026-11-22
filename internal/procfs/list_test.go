//go:build linux

package procfs

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// listRun 调用 process.list 并解析成结构；失败直接终止测试。
func listRun(t *testing.T, rt *Runtime, params string) listResult {
	t.Helper()
	raw, perr := rt.Call("process.list", json.RawMessage(params))
	if perr != nil {
		t.Fatalf("process.list(%s) 失败: %+v", params, perr)
	}
	var result listResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("解析 list 结果失败: %v", err)
	}
	return result
}

// sampleFixture 造三条稳定记录；cpu 差分断言依赖它们的 tick 基线。
func sampleFixture() *fakeSource {
	return &fakeSource{records: []Record{
		{PID: 10, Name: "alpha", State: 'S', UID: 1000, RSSKiB: 100},
		{PID: 20, Name: "beta", State: 'R', UID: 1000, RSSKiB: 300},
		{PID: 30, Name: "Gamma", State: 'S', UID: 0, RSSKiB: 200},
	}}
}

// TestListFirstSampleThenCPU 首次调用 cpu_pct 全 0 并给出说明；第二次按 tick 差分。
func TestListFirstSampleThenCPU(t *testing.T) {
	src := sampleFixture()
	clock, advance := fixedClock(time.Unix(1700000000, 0))
	rt := testRuntime(t, src, clock)

	first := listRun(t, rt, `{"limit":500}`)
	if !first.FirstSample {
		t.Fatalf("首次调用 first_sample 应为 true: %+v", first)
	}
	if first.CPUNote == "" || !contains(first.CPUNote, "首次") {
		t.Fatalf("首次调用缺少说明: %q", first.CPUNote)
	}
	for i, wantPID := range []int{30, 20, 10} {
		if first.Items[i].PID != wantPID {
			t.Fatalf("首次采样 cpu 全 0 时应按 pid 降序，第 %d 位=%d，期望 %d", i, first.Items[i].PID, wantPID)
		}
	}
	for _, item := range first.Items {
		if item.CPUPercent != 0 {
			t.Fatalf("首次调用 pid %d cpu_pct=%v，期望 0", item.PID, item.CPUPercent)
		}
	}

	advance(time.Second)
	// 1 秒内：pid10 用 10 tick=10%，pid20 用 50 tick=50%，pid30 0。
	src.records[0].UTicks, src.records[0].STicks = 6, 4
	src.records[1].UTicks, src.records[1].STicks = 40, 10
	second := listRun(t, rt, `{"sort":"cpu","limit":500}`)
	if second.FirstSample {
		t.Fatal("第二次调用 first_sample 应为 false")
	}
	if second.CPUNote == "" || contains(second.CPUNote, "首次") {
		t.Fatalf("非首次调用说明不符: %q", second.CPUNote)
	}
	wantOrder := []int{20, 10, 30}
	wantCPU := map[int]float64{20: 50, 10: 10, 30: 0}
	for i, item := range second.Items {
		if item.PID != wantOrder[i] {
			t.Fatalf("cpu 排序第 %d 位 pid=%d，期望 %d", i, item.PID, wantOrder[i])
		}
		if item.CPUPercent != wantCPU[item.PID] {
			t.Fatalf("pid %d cpu_pct=%v，期望 %v", item.PID, item.CPUPercent, wantCPU[item.PID])
		}
	}
}

// TestListSorts 覆盖 cpu / mem / pid / name 四种排序。
func TestListSorts(t *testing.T) {
	src := sampleFixture()
	clock, advance := fixedClock(time.Unix(1700000000, 0))
	rt := testRuntime(t, src, clock)
	listRun(t, rt, `{}`) // 建基线
	advance(time.Second)
	src.records[0].UTicks = 10
	src.records[1].UTicks = 50

	cases := []struct {
		sort string
		want []int
	}{
		{"cpu", []int{20, 10, 30}},
		{"mem", []int{20, 30, 10}},
		{"pid", []int{10, 20, 30}},
		{"name", []int{10, 20, 30}},
	}
	for _, tc := range cases {
		result := listRun(t, rt, `{"sort":"`+tc.sort+`","limit":500}`)
		if result.Sort != tc.sort {
			t.Fatalf("sort 回显=%q，期望 %q", result.Sort, tc.sort)
		}
		if len(result.Items) != len(tc.want) {
			t.Fatalf("%s 排序条数=%d，期望 %d", tc.sort, len(result.Items), len(tc.want))
		}
		for i, item := range result.Items {
			if item.PID != tc.want[i] {
				t.Fatalf("%s 排序第 %d 位 pid=%d，期望 %d", tc.sort, i, item.PID, tc.want[i])
			}
		}
	}
}

// TestListLimit 默认 50、显式截断、越界拒绝。
func TestListLimit(t *testing.T) {
	rt := testRuntime(t, sampleFixture(), nil)
	defaulted := listRun(t, rt, `{}`)
	if defaulted.Limit != DefaultLimit || defaulted.Sort != SortCPU {
		t.Fatalf("默认 sort/limit=%q/%d，期望 cpu/%d", defaulted.Sort, defaulted.Limit, DefaultLimit)
	}
	limited := listRun(t, rt, `{"limit":2}`)
	if limited.Count != 2 || limited.Total != 3 || len(limited.Items) != 2 {
		t.Fatalf("limit=2 结果 count=%d total=%d items=%d", limited.Count, limited.Total, len(limited.Items))
	}
	for _, params := range []string{`{"limit":0}`, `{"limit":501}`} {
		if _, perr := rt.Call("process.list", json.RawMessage(params)); perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("%s 应返回 E_INVALID，实际 %+v", params, perr)
		}
	}
}

// TestListInvalidSort 非法排序键返回 E_INVALID。
func TestListInvalidSort(t *testing.T) {
	rt := testRuntime(t, sampleFixture(), nil)
	_, perr := rt.Call("process.list", json.RawMessage(`{"sort":"rss"}`))
	if perr == nil || perr.Code != pluginapi.E_INVALID {
		t.Fatalf("非法 sort 应返回 E_INVALID，实际 %+v", perr)
	}
}

// TestListMaskAndRestricted 列表层脱敏生效，受限进程逐条标记且不打断请求。
func TestListMaskAndRestricted(t *testing.T) {
	src := &fakeSource{records: []Record{
		{PID: 40, Name: "sleep", State: 'S', Cmdline: "sleep 300 --password=secret --name=ok"},
		{PID: 41, Name: "hidden", State: '?', Restricted: true, RestrictedReason: "status 不可读"},
	}}
	rt := testRuntime(t, src, nil)
	result := listRun(t, rt, `{"limit":500}`)
	if result.Count != 2 || !result.MaskedArgs {
		t.Fatalf("列表条数/masked_args 不符: count=%d masked=%v", result.Count, result.MaskedArgs)
	}
	byPID := make(map[int]ProcessEntry, 2)
	for _, item := range result.Items {
		byPID[item.PID] = item
	}
	if got := byPID[40].Cmdline; got != "sleep 300 --password=*** --name=ok" {
		t.Fatalf("cmdline 脱敏不符: %q", got)
	}
	if !byPID[41].Restricted || byPID[41].RestrictedReason == "" {
		t.Fatalf("受限进程未标记: %+v", byPID[41])
	}
}

// TestListNewProcessZero 基线之后新出现的进程 cpu_pct 为 0（没有上一次 tick 可减）。
func TestListNewProcessZero(t *testing.T) {
	src := sampleFixture()
	clock, advance := fixedClock(time.Unix(1700000000, 0))
	rt := testRuntime(t, src, clock)
	listRun(t, rt, `{}`)
	advance(time.Second)
	src.records = append(src.records, Record{PID: 50, Name: "newbie", State: 'S'})
	result := listRun(t, rt, `{"limit":500}`)
	for _, item := range result.Items {
		if item.PID == 50 && item.CPUPercent != 0 {
			t.Fatalf("新进程 cpu_pct=%v，期望 0", item.CPUPercent)
		}
	}
}

// contains 是 strings.Contains 的本地封装，避免测试文件重复 import。
func contains(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
