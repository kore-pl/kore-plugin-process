//go:build linux

package procfs

import (
	"encoding/json"
	"fmt"
	"os"
	"syscall"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// reniceRun 调用 process.renice 并解析成结构。
func reniceRun(t *testing.T, rt *Runtime, params string) reniceResult {
	t.Helper()
	raw, perr := rt.Call("process.renice", json.RawMessage(params))
	if perr != nil {
		t.Fatalf("process.renice(%s) 失败: %+v", params, perr)
	}
	var result reniceResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("解析 renice 结果失败: %v", err)
	}
	return result
}

// reniceExpect 断言 renice 返回预期错误码与关键词。
func reniceExpect(t *testing.T, rt *Runtime, params, code, hint string) {
	t.Helper()
	_, perr := rt.Call("process.renice", json.RawMessage(params))
	if perr == nil || perr.Code != code {
		t.Fatalf("%s 错误=%+v，期望 %s", params, perr, code)
	}
	if !contains(perr.Message, hint) {
		t.Fatalf("%s 错误信息 %q 未含 %q", params, perr.Message, hint)
	}
}

// TestReniceRequiresConfirm 缺 confirm:true 拒绝（改变调度行为必须显式确认）。
func TestReniceRequiresConfirm(t *testing.T) {
	src := &fakeSource{records: []Record{{PID: 4242, Name: "sleep", State: 'S', Nice: 5, Cmdline: "sleep 300"}}}
	rt := testRuntime(t, src, nil)
	reniceExpect(t, rt, `{"pid":4242,"value":10}`, pluginapi.E_INVALID, "confirm")
	if len(src.niceCalls) != 0 {
		t.Fatalf("未确认不应调用 SetNice: %v", src.niceCalls)
	}
}

// TestReniceValueValidation 缺 value / 越界值一律 E_INVALID（nice 0..19）。
func TestReniceValueValidation(t *testing.T) {
	rt := testRuntime(t, &fakeSource{records: []Record{{PID: 4242, Name: "sleep", State: 'S', Nice: 5, Cmdline: "sleep 300"}}}, nil)
	reniceExpect(t, rt, `{"pid":4242,"confirm":true}`, pluginapi.E_INVALID, "value")
	for _, params := range []string{
		`{"pid":4242,"confirm":true,"value":-1}`,
		`{"pid":4242,"confirm":true,"value":20}`,
	} {
		reniceExpect(t, rt, params, pluginapi.E_INVALID, "nice")
	}
}

// TestReniceProtected 保护名单同样适用于 renice：kored / 内核线程 / 自身都拒绝。
func TestReniceProtected(t *testing.T) {
	rt := testRuntime(t, &fakeSource{records: []Record{{PID: 4242, Name: "kored", State: 'S', Cmdline: "kored serve"}}}, nil)
	reniceExpect(t, rt, `{"pid":4242,"value":10,"confirm":true}`, pluginapi.E_UNAUTHORIZED, "kored")
	rt = testRuntime(t, &fakeSource{records: []Record{{PID: 77, Name: "ksoftirqd/0", State: 'S'}}}, nil)
	reniceExpect(t, rt, `{"pid":77,"value":10,"confirm":true}`, pluginapi.E_UNAUTHORIZED, "内核线程")
}

// TestReniceNameMismatch PID 复用复核与 kill 同一实现。
func TestReniceNameMismatch(t *testing.T) {
	rt := testRuntime(t, &fakeSource{records: []Record{{PID: 4242, Name: "sleep", State: 'S', Nice: 5, Cmdline: "sleep 300"}}}, nil)
	reniceExpect(t, rt, `{"pid":4242,"value":10,"confirm":true,"name":"nginx"}`, pluginapi.E_CONFLICT, "复用")
}

// TestReniceRaiseNeedsRoot 只允许调低优先级（数值调大）：调小直接 E_NEED_ROOT，不触达执行面。
func TestReniceRaiseNeedsRoot(t *testing.T) {
	src := &fakeSource{records: []Record{{PID: 4242, Name: "sleep", State: 'S', Nice: 5, Cmdline: "sleep 300"}}}
	rt := testRuntime(t, src, nil)
	reniceExpect(t, rt, `{"pid":4242,"value":4,"confirm":true,"name":"sleep"}`, "E_NEED_ROOT", "root")
	if len(src.niceCalls) != 0 {
		t.Fatalf("调高优先级不应触达执行面: %v", src.niceCalls)
	}
}

// TestReniceLowerSucceeds 数值调大 / 持平均放行，previous/nice/changed 如实返回。
func TestReniceLowerSucceeds(t *testing.T) {
	src := &fakeSource{records: []Record{{PID: 4242, Name: "sleep", State: 'S', Nice: 5, Cmdline: "sleep 300"}}}
	rt := testRuntime(t, src, nil)
	result := reniceRun(t, rt, `{"pid":4242,"value":15,"confirm":true,"name":"sleep"}`)
	if !result.Changed || result.Previous != 5 || result.Nice != 15 || result.PID != 4242 || result.Name != "sleep" {
		t.Fatalf("结果=%+v", result)
	}
	if len(src.niceCalls) != 1 || src.niceCalls[0].value != 15 {
		t.Fatalf("SetNice 调用=%v", src.niceCalls)
	}
	same := reniceRun(t, rt, `{"pid":4242,"value":15,"confirm":true,"name":"sleep"}`)
	if same.Changed {
		t.Fatalf("同值调整 changed 应为 false: %+v", same)
	}
}

// TestReniceSetNiceErrors 执行面 EPERM → E_NEED_ROOT、ESRCH → E_NOTFOUND。
func TestReniceSetNiceErrors(t *testing.T) {
	denied := &fakeSource{records: []Record{{PID: 4242, Name: "other", State: 'S', Nice: 5, Cmdline: "other --serve"}}, niceErr: syscall.EPERM}
	reniceExpect(t, testRuntime(t, denied, nil), `{"pid":4242,"value":15,"confirm":true,"name":"other"}`, "E_NEED_ROOT", "root")
	gone := &fakeSource{records: []Record{{PID: 4242, Name: "other", State: 'S', Nice: 5, Cmdline: "other --serve"}}, niceErr: syscall.ESRCH}
	reniceExpect(t, testRuntime(t, gone, nil), `{"pid":4242,"value":15,"confirm":true,"name":"other"}`, pluginapi.E_NOTFOUND, "不存在")
}

// TestReniceNotFound 目标不存在返回 E_NOTFOUND。
func TestReniceNotFound(t *testing.T) {
	rt := testRuntime(t, &fakeSource{}, nil)
	reniceExpect(t, rt, `{"pid":4242,"value":10,"confirm":true,"name":"x"}`, pluginapi.E_NOTFOUND, "不存在")
}

// TestReniceRealSleeper 真实进程：detail 读当前 nice → 调大一位 → /proc 复核生效；调小被 E_NEED_ROOT 拒。
func TestReniceRealSleeper(t *testing.T) {
	sleeper := spawnRealSleep(t)
	pid := sleeper.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		_ = sleeper.Wait()
	})
	rt := NewRuntime(Options{SelfPID: os.Getpid(), MaskArgs: true}, nil)
	raw, perr := rt.Call("process.detail", json.RawMessage(fmt.Sprintf(`{"pid":%d}`, pid)))
	if perr != nil {
		t.Fatalf("detail 失败: %+v", perr)
	}
	var detail detailResult
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatalf("解析 detail 失败: %v", err)
	}
	target := detail.Nice
	if target < 19 {
		target++
	}
	result := reniceRun(t, rt, fmt.Sprintf(`{"pid":%d,"value":%d,"confirm":true,"name":"sleep"}`, pid, target))
	if !result.Changed || result.Previous != detail.Nice || result.Nice != target {
		t.Fatalf("结果=%+v 期望 %d→%d", result, detail.Nice, target)
	}
	info, err := parseStatFile(pid)
	if err != nil {
		t.Fatalf("读 /proc stat 失败: %v", err)
	}
	if info.Nice != target {
		t.Fatalf("/proc nice=%d，期望 %d", info.Nice, target)
	}
	// 回调优先级（数值变小）即使目标仍属自己也必须 E_NEED_ROOT——非特权用户不可调高。
	if target >= 1 {
		reniceExpect(t, rt, fmt.Sprintf(`{"pid":%d,"value":%d,"confirm":true,"name":"sleep"}`, pid, target-1), "E_NEED_ROOT", "root")
	}
}
