//go:build linux

package procfs

import (
	"encoding/json"
	"fmt"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// killRun 调用 process.kill 并解析成结构。
func killRun(t *testing.T, rt *Runtime, params string) killResult {
	t.Helper()
	raw, perr := rt.Call("process.kill", json.RawMessage(params))
	if perr != nil {
		t.Fatalf("process.kill(%s) 失败: %+v", params, perr)
	}
	var result killResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("解析 kill 结果失败: %v", err)
	}
	return result
}

// killExpect 断言 kill 返回预期错误码与关键词。
func killExpect(t *testing.T, rt *Runtime, params, code, hint string) {
	t.Helper()
	_, perr := rt.Call("process.kill", json.RawMessage(params))
	if perr == nil || perr.Code != code {
		t.Fatalf("%s 错误=%+v，期望 %s", params, perr, code)
	}
	if !contains(perr.Message, hint) {
		t.Fatalf("%s 错误信息 %q 未含 %q", params, perr.Message, hint)
	}
}

// TestKillRejectsPidOne 护栏一：pid ≤ 1 直接拒绝（含 0 / 负数 / init）。
func TestKillRejectsPidOne(t *testing.T) {
	rt := testRuntime(t, sampleFixture(), nil)
	for _, params := range []string{`{"pid":1}`, `{"pid":0}`, `{"pid":-3}`} {
		killExpect(t, rt, params, pluginapi.E_INVALID, "pid≤1")
	}
}

// TestKillRejectsSelf 护栏二：拒绝向插件自身发信号。
func TestKillRejectsSelf(t *testing.T) {
	rt := NewRuntime(Options{ProcRoot: t.TempDir(), SelfPID: 5000, MaskArgs: true, GOOS: "linux"}, &fakeSource{})
	killExpect(t, rt, `{"pid":5000}`, pluginapi.E_UNAUTHORIZED, "插件自身")
}

// TestKillRejectsKored 护栏三：comm=kored 的 kore 主进程拒绝。
func TestKillRejectsKored(t *testing.T) {
	src := &fakeSource{records: []Record{{PID: 4242, Name: "kored", State: 'S'}}}
	rt := testRuntime(t, src, nil)
	killExpect(t, rt, `{"pid":4242}`, pluginapi.E_UNAUTHORIZED, "kored")
	if len(src.signals) != 0 {
		t.Fatalf("拒绝后不应投递信号，实际 %v", src.signals)
	}
}

// TestKillRejectsProtectedConfig 护栏四：KORE_PROTECTED_PIDS 名单拒绝。
func TestKillRejectsProtectedConfig(t *testing.T) {
	rt := NewRuntime(Options{
		ProcRoot: t.TempDir(), SelfPID: 999999, MaskArgs: true, GOOS: "linux",
		ProtectedPIDs: []int{4243},
	}, &fakeSource{records: []Record{{PID: 4243, Name: "db", State: 'S'}}})
	killExpect(t, rt, `{"pid":4243}`, pluginapi.E_UNAUTHORIZED, "KORE_PROTECTED_PIDS")
}

// TestKillInvalidSignal 只允许 TERM/KILL/HUP/INT。
func TestKillInvalidSignal(t *testing.T) {
	src := &fakeSource{records: []Record{{PID: 4244, Name: "sleep", State: 'S'}}}
	rt := testRuntime(t, src, nil)
	killExpect(t, rt, `{"pid":4244,"signal":"USR1"}`, pluginapi.E_INVALID, "signal")
}

// TestKillNotFound 目标不存在返回 E_NOTFOUND。
func TestKillNotFound(t *testing.T) {
	rt := testRuntime(t, &fakeSource{}, nil)
	killExpect(t, rt, `{"pid":4242}`, pluginapi.E_NOTFOUND, "不存在")
}

// TestKillSignalErrors 投递失败按 errno 映射：EPERM → E_UNAUTHORIZED、ESRCH → E_NOTFOUND。
func TestKillSignalErrors(t *testing.T) {
	denied := &fakeSource{records: []Record{{PID: 4242, Name: "other", State: 'S'}}, signalErr: syscall.EPERM}
	killExpect(t, testRuntime(t, denied, nil), `{"pid":4242}`, pluginapi.E_UNAUTHORIZED, "同 uid")
	gone := &fakeSource{records: []Record{{PID: 4242, Name: "other", State: 'S'}}, signalErr: syscall.ESRCH}
	killExpect(t, testRuntime(t, gone, nil), `{"pid":4242}`, pluginapi.E_NOTFOUND, "不存在")
}

// TestKillSignalWhitelist 允许的信号能原样投递。
func TestKillSignalWhitelist(t *testing.T) {
	for _, tc := range []struct {
		signal string
		want   Signal
	}{
		{"", SignalTERM},
		{"TERM", SignalTERM},
		{"kill", SignalKILL},
		{"HUP", SignalHUP},
		{"INT", SignalINT},
	} {
		src := &fakeSource{records: []Record{{PID: 4245, Name: "sleep", State: 'S'}}, probeState: 'Z'}
		rt := testRuntime(t, src, nil)
		params := `{"pid":4245}`
		if tc.signal != "" {
			params = fmt.Sprintf(`{"pid":4245,"signal":%q}`, tc.signal)
		}
		result := killRun(t, rt, params)
		if !result.Killed || result.StillAlive {
			t.Fatalf("signal=%q 结果=%+v", tc.signal, result)
		}
		if len(src.signals) != 1 || src.signals[0] != tc.want {
			t.Fatalf("signal=%q 实际投递=%v，期望 %v", tc.signal, src.signals, tc.want)
		}
	}
}

// TestKillStillAlive 目标忽略信号时，500ms 窗口后如实返回 still_alive=true。
func TestKillStillAlive(t *testing.T) {
	src := &fakeSource{records: []Record{{PID: 4246, Name: "stubborn", State: 'S'}}, probeState: 'S'}
	rt := testRuntime(t, src, nil)
	result := killRun(t, rt, `{"pid":4246}`)
	if !result.Killed || !result.StillAlive {
		t.Fatalf("忽略信号的进程结果=%+v", result)
	}
	if result.WaitedMS < 450 {
		t.Fatalf("确认窗口 %dms 少于 450ms", result.WaitedMS)
	}
	if result.Note == "" {
		t.Fatal("still_alive=true 时应给出说明")
	}
}

// TestKillZombieCountsDead 僵尸进程视为已退出（避免把已死进程报成存活）。
func TestKillZombieCountsDead(t *testing.T) {
	src := &fakeSource{records: []Record{{PID: 4247, Name: "zombie", State: 'Z'}}, probeState: 'Z'}
	rt := testRuntime(t, src, nil)
	result := killRun(t, rt, `{"pid":4247}`)
	if !result.Killed || result.StillAlive {
		t.Fatalf("僵尸进程结果=%+v", result)
	}
}

// TestKillRealSleep 起一个真实长睡子进程，用默认 TERM 杀掉并确认 500ms 内不存活。
func TestKillRealSleep(t *testing.T) {
	sleeper := spawnSleeper(t, "sleep", "300")
	pid := sleeper.Process.Pid
	rt := NewRuntime(Options{SelfPID: os.Getpid(), MaskArgs: true}, nil)
	result := killRun(t, rt, fmt.Sprintf(`{"pid":%d}`, pid))
	if !result.Killed || result.StillAlive {
		t.Fatalf("kill sleep 结果=%+v", result)
	}
	if result.Signal != "TERM" || result.PID != pid {
		t.Fatalf("结果信号/pid=%q/%d", result.Signal, result.PID)
	}
	_ = sleeper.Wait() // 回收子进程；被信号终止时 Wait 返回 error，这里只关心收尸
	waitGone(t, pid, 2*time.Second)
}
